package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"

	"github.com/rustic-ai/forge/forge-go/control"
	"github.com/rustic-ai/forge/forge-go/helper/logging"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/rustic-ai/forge/forge-go/scheduler"
	"github.com/rustic-ai/forge/forge-go/secrets"
	"github.com/rustic-ai/forge/forge-go/telemetry"
)

func registerNode(ctx context.Context, serverURL string, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/nodes/register", serverURL), bytes.NewBuffer(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("server returned non-2xx status during registration: %d", resp.StatusCode)
	}
	return nil
}

func deregisterNode(ctx context.Context, serverURL, nodeID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/nodes/%s", serverURL, nodeID), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("server returned unexpected status during deregistration: %d", resp.StatusCode)
	}
	return nil
}

func StartClient(ctx context.Context, config *ClientConfig) error {
	log := logging.FromContext(ctx, slog.Default()).With("node_id", config.NodeID)

	if config.CPUs <= 0 {
		config.CPUs = runtime.NumCPU()
	}
	if config.Memory <= 0 {
		config.Memory = 8192
	}
	if config.GPUs < 0 {
		config.GPUs = 0
	}

	log.Info("Starting Forge client daemon",
		"server_url", config.ServerURL,
		"redis_url", config.RedisURL,
		"cpus", config.CPUs,
		"memory_mb", config.Memory,
		"gpus", config.GPUs,
	)

	if config.RedisURL == "" {
		return fmt.Errorf("redis URL is required for distributed client mode")
	}

	rdb := redis.NewClient(&redis.Options{Addr: config.RedisURL})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("failed to connect to redis at %s: %w", config.RedisURL, err)
	}

	reqPayload := struct {
		NodeID   string                     `json:"node_id"`
		Capacity scheduler.ResourceCapacity `json:"capacity"`
	}{
		NodeID: config.NodeID,
		Capacity: scheduler.ResourceCapacity{
			CPUs:   config.CPUs,
			Memory: config.Memory,
			GPUs:   config.GPUs,
		},
	}
	body, _ := json.Marshal(reqPayload)
	if err := registerNode(ctx, config.ServerURL, body); err != nil {
		return fmt.Errorf("failed to register with server: %w", err)
	}

	reg, err := registry.Load("")
	if err != nil {
		return fmt.Errorf("failed to load agent registry: %w", err)
	}
	if injectFS := os.Getenv("FORGE_INJECT_FS"); injectFS != "" {
		for _, fsEntry := range strings.Split(injectFS, ",") {
			parts := strings.SplitN(strings.TrimSpace(fsEntry), ":", 2)
			mode := "rw"
			if len(parts) == 2 {
				mode = parts[1]
			}
			for _, className := range reg.ClassNames() {
				_ = reg.InjectFilesystem(className, registry.FilesystemPermission{Path: parts[0], Mode: mode})
			}
		}
	}
	if injectNet := os.Getenv("FORGE_INJECT_NET"); injectNet != "" {
		nets := strings.Split(injectNet, ",")
		for i := range nets {
			nets[i] = strings.TrimSpace(nets[i])
		}
		for _, className := range reg.ClassNames() {
			_ = reg.InjectNetwork(className, nets)
		}
	}
	sec := secrets.DefaultProvider()
	supervisorFactory := buildOrgSupervisorFactory(rdb, config.DefaultSupervisor, config.DataDir)
	nodeQueueKey := "forge:control:node:" + config.NodeID
	queueHandler := control.NewControlQueueHandlerWithQueueFactory(rdb, reg, sec, supervisorFactory, nil, nodeQueueKey)
	if err := queueHandler.Start(ctx); err != nil {
		return fmt.Errorf("failed to start node queue listener: %w", err)
	}
	defer queueHandler.Stop()

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		client := &http.Client{Timeout: 3 * time.Second}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				hbURL := fmt.Sprintf("%s/nodes/%s/heartbeat", config.ServerURL, config.NodeID)
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, hbURL, nil)
				if r, err := client.Do(req); err == nil {
					status := r.StatusCode
					r.Body.Close()
					if status >= 200 && status < 300 {
						log.Debug("Sent heartbeat", "node_id", config.NodeID)
						continue
					}
					if status == http.StatusNotFound {
						if err := registerNode(ctx, config.ServerURL, body); err != nil {
							log.Warn("Node not found during heartbeat and re-registration failed", "error", err, "node_id", config.NodeID)
						} else {
							log.Info("Node re-registered after heartbeat miss", "node_id", config.NodeID)
						}
						continue
					}
					log.Warn("Server returned non-2xx heartbeat status", "status", status, "node_id", config.NodeID)
				} else {
					log.Warn("Failed to send heartbeat to server", "error", err)
				}
			}
		}
	}()

	go func() {
		metricsTicker := time.NewTicker(15 * time.Second)
		defer metricsTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-metricsTicker.C:
				if cpuPercent, err := cpu.Percent(0, false); err == nil && len(cpuPercent) > 0 {
					telemetry.NodeCPUUtilization.WithLabelValues(config.NodeID).Set(cpuPercent[0])
				}
				if virtMem, err := mem.VirtualMemory(); err == nil {
					telemetry.NodeRAMBytes.WithLabelValues(config.NodeID).Set(float64(virtMem.Used))
				}
				if diskUsage, err := disk.Usage("/"); err == nil {
					telemetry.NodeDiskFreeBytes.WithLabelValues(config.NodeID).Set(float64(diskUsage.Free))
				}
			}
		}
	}()

	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", promhttp.Handler())
	metricsMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "ok"}`))
	})
	metricsMux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "ready"}`))
	})

	metricsServer := &http.Server{
		Addr:    config.MetricsAddr,
		Handler: metricsMux,
	}

	go func() {
		log.Info("Starting client metrics server", "address", config.MetricsAddr)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("Metrics server failed", "error", err)
		}
	}()

	log.Info("Forge client node registered and ready, awaiting workloads", "node_id", config.NodeID)

	<-ctx.Done()

	log.Info("Forge client shutting down.")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := deregisterNode(shutdownCtx, config.ServerURL, config.NodeID); err != nil {
		log.Warn("Failed to deregister node during shutdown", "error", err)
	}
	_ = metricsServer.Shutdown(shutdownCtx)

	return nil
}
