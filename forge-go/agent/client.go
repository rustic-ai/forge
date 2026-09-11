package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/rustic-ai/forge/forge-go/api"
	"github.com/rustic-ai/forge/forge-go/control"
	"github.com/rustic-ai/forge/forge-go/dependencies"
	"github.com/rustic-ai/forge/forge-go/helper/logging"
	"github.com/rustic-ai/forge/forge-go/infraevents"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/rustic-ai/forge/forge-go/scheduler"
	"github.com/rustic-ai/forge/forge-go/secrets"
	"github.com/rustic-ai/forge/forge-go/supervisor"
	"github.com/rustic-ai/forge/forge-go/telemetry"
)

const embeddedWorkloadShutdownTimeout = 10 * time.Second

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
	defer func() { _ = resp.Body.Close() }()
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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("server returned unexpected status during deregistration: %d", resp.StatusCode)
	}
	return nil
}

type NodeHeartbeatPayload struct {
	ReadyDependencyProfiles []string `json:"ready_dependency_profiles"`
}

func StartClient(ctx context.Context, config *ClientConfig) error {
	log := logging.FromContext(ctx, slog.Default()).With("node_id", config.NodeID)
	if err := dependencies.ValidateMode(config.DependencyPrewarmMode); err != nil {
		return err
	}
	sec := config.SecretProvider
	if sec == nil {
		var names []string
		var unsafe bool
		var err error
		sec, names, unsafe, err = secrets.NewProviderChain(config.SecretProviders)
		if err != nil {
			return fmt.Errorf("configure secret providers: %w", err)
		}
		if unsafe {
			log.Warn("UNSAFE SECRET PROVIDERS ENABLED; runtime credentials may be read outside the OS keychain", "providers", strings.Join(names, ","))
		}
		defer sec.Clear()
	}

	if config.CPUs <= 0 {
		config.CPUs = runtime.NumCPU()
	}
	if config.Memory <= 0 {
		config.Memory = 8192
	}
	if config.GPUs < 0 {
		config.GPUs = 0
	}
	readyProfiles := append([]string{}, config.ReadyDependencyProfiles...)
	if config.ReadinessProvider != nil {
		var err error
		readyProfiles, err = config.ReadinessProvider()
		if err != nil {
			return fmt.Errorf("evaluate dependency readiness: %w", err)
		}
	} else if !config.ReadinessProvided {
		var err error
		readyProfiles, err = api.ReadyDependencyProfileKeys(config.DependencyConfig, func(name string) (bool, error) {
			_, err := sec.Resolve(ctx, name)
			if errors.Is(err, secrets.ErrSecretNotFound) {
				return false, nil
			}
			return err == nil, err
		})
		if err != nil {
			return fmt.Errorf("evaluate dependency readiness: %w", err)
		}
	}

	log.Info("Starting Forge client daemon",
		"server_url", config.ServerURL,
		"redis_url", config.RedisURL,
		"nats_url", config.NATSUrl,
		"cpus", config.CPUs,
		"memory_mb", config.Memory,
		"gpus", config.GPUs,
	)

	var controlPlane control.ControlPlane
	var statusStore supervisor.AgentStatusStore
	var msgBackend messaging.Backend
	if config.NATSUrl != "" {
		_ = os.Setenv("NATS_URL", config.NATSUrl)
		nc, err := nats.Connect(config.NATSUrl)
		if err != nil {
			return fmt.Errorf("failed to connect to NATS at %s: %w", config.NATSUrl, err)
		}
		defer nc.Close()
		natsCP, err := control.NewNATSControlTransport(nc)
		if err != nil {
			return fmt.Errorf("failed to create NATS control transport: %w", err)
		}
		natsStatus, err := supervisor.NewNATSAgentStatusStore(nc)
		if err != nil {
			return fmt.Errorf("failed to create NATS agent status store: %w", err)
		}
		natsBackend, err := messaging.NewNATSBackend(nc)
		if err != nil {
			return fmt.Errorf("failed to create NATS messaging backend: %w", err)
		}
		controlPlane = natsCP
		statusStore = natsStatus
		msgBackend = natsBackend
	} else if config.RedisURL != "" {
		rdb := redis.NewClient(&redis.Options{Addr: config.RedisURL})
		defer func() { _ = rdb.Close() }()
		if err := rdb.Ping(ctx).Err(); err != nil {
			return fmt.Errorf("failed to connect to redis at %s: %w", config.RedisURL, err)
		}
		controlPlane = control.NewRedisControlTransport(rdb)
		statusStore = supervisor.NewRedisAgentStatusStore(rdb)
		msgBackend = messaging.NewRedisBackend(rdb)
	} else {
		return fmt.Errorf("either --redis or --nats URL is required for distributed client mode")
	}

	metricsListener, err := net.Listen("tcp", config.MetricsAddr)
	if err != nil {
		return fmt.Errorf("start client metrics listener on %s: %w", config.MetricsAddr, err)
	}
	defer func() { _ = metricsListener.Close() }()

	reqPayload := struct {
		NodeID                  string                     `json:"node_id"`
		Capacity                scheduler.ResourceCapacity `json:"capacity"`
		ReadyDependencyProfiles []string                   `json:"ready_dependency_profiles"`
		Capabilities            []string                   `json:"capabilities"`
	}{
		NodeID:                  config.NodeID,
		ReadyDependencyProfiles: readyProfiles,
		Capabilities: func() []string {
			if dependencies.SupportsRuntimePreparation(config.DependencyPrewarmMode, registry.UVPython()) {
				return []string{protocol.RuntimePreparationV1Capability}
			}
			return []string{}
		}(),
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

	reg, err := registry.Load("", config.OAuthManager)
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
	infraPublisher, err := infraevents.NewPublisher(msgBackend)
	if err != nil {
		return fmt.Errorf("failed to create infra event publisher: %w", err)
	}
	var dependencyPrewarmer *dependencies.Coordinator
	if dependencies.Enabled(config.DependencyPrewarmMode) {
		dependencyPrewarmer, err = dependencies.NewCoordinator(dependencies.Config{
			Context: ctx, Registry: reg, Publisher: infraPublisher, NodeID: config.NodeID,
		})
		if err != nil {
			return fmt.Errorf("initialize dependency prewarmer: %w", err)
		}
	}
	supervisorFactory := buildOrgSupervisorFactory(statusStore, config.DefaultSupervisor, config.DefaultTransport, msgBackend, infraPublisher, config.DataDir, config.AttachProcessTree, config.ZMQBridgeMode)
	nodeQueueKey := "forge:control:node:" + config.NodeID
	queueHandler := control.NewControlQueueHandlerWithQueueFactory(controlPlane, reg, sec, supervisorFactory, nil, nodeQueueKey,
		control.WithStatusStore(statusStore),
		control.WithNodeID(config.NodeID),
		control.WithInfraEventPublisher(infraPublisher),
		control.WithStopAgentsOnExit(config.StopAgentsOnExit),
		control.WithDependencyPrewarmer(dependencyPrewarmer),
	)
	if err := queueHandler.Start(ctx); err != nil {
		return fmt.Errorf("failed to start node queue listener: %w", err)
	}
	if dependencyPrewarmer != nil {
		dependencyPrewarmer.WarmPython()
	}

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		client := &http.Client{Timeout: 3 * time.Second}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if config.ReadinessProvider != nil {
					updated, err := config.ReadinessProvider()
					if err != nil {
						log.Error("Failed to update dependency readiness; reporting no ready profiles", "error", err)
						readyProfiles = nil
					} else {
						readyProfiles = updated
					}
				}
				hbURL := fmt.Sprintf("%s/nodes/%s/heartbeat", config.ServerURL, config.NodeID)
				hbBody, _ := json.Marshal(NodeHeartbeatPayload{ReadyDependencyProfiles: readyProfiles})
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, hbURL, bytes.NewReader(hbBody))
				req.Header.Set("Content-Type", "application/json")
				if r, err := client.Do(req); err == nil {
					status := r.StatusCode
					_ = r.Body.Close()
					if status >= 200 && status < 300 {
						log.Debug("Sent heartbeat", "node_id", config.NodeID)
						continue
					}
					if status == http.StatusNotFound {
						reqPayload.ReadyDependencyProfiles = readyProfiles
						registrationBody, _ := json.Marshal(reqPayload)
						if err := registerNode(ctx, config.ServerURL, registrationBody); err != nil {
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
					telemetry.SetNodeCPUUtilization(config.NodeID, cpuPercent[0])
				}
				if virtMem, err := mem.VirtualMemory(); err == nil {
					telemetry.SetNodeRAMBytes(config.NodeID, float64(virtMem.Used))
				}
				if diskUsage, err := disk.Usage("/"); err == nil {
					telemetry.SetNodeDiskFreeBytes(config.NodeID, float64(diskUsage.Free))
				}
			}
		}
	}()

	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", telemetry.PrometheusHandler())
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
		log.Info("Starting client metrics server", "address", metricsListener.Addr().String())
		if err := metricsServer.Serve(metricsListener); err != nil && err != http.ErrServerClosed {
			log.Error("Metrics server failed", "error", err)
		}
	}()

	log.Info("Forge client node registered and ready, awaiting workloads", "node_id", config.NodeID)

	<-ctx.Done()

	log.Info("Forge client shutting down.")
	log.Info("Stopping embedded client workloads before client exit.")
	workloadShutdownCtx, cancelWorkloadShutdown := context.WithTimeout(
		context.Background(),
		embeddedWorkloadShutdownTimeout,
	)
	workloadShutdownErr := queueHandler.StopWithContext(workloadShutdownCtx)
	cancelWorkloadShutdown()
	if workloadShutdownErr != nil {
		log.Error("Embedded client workload shutdown failed.", "error", workloadShutdownErr)
	} else {
		log.Info("Embedded client workloads stopped.")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := deregisterNode(shutdownCtx, config.ServerURL, config.NodeID); err != nil {
		log.Warn("Failed to deregister node during shutdown", "error", err)
	}
	_ = metricsServer.Shutdown(shutdownCtx)

	return workloadShutdownErr
}
