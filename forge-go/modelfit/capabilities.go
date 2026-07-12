package modelfit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

const (
	modelFitLlamaBinaryEnv     = "FORGE_MODELFIT_LLAMA_BINARY"
	modelFitRuntimeCacheEnv    = "FORGE_MODELFIT_RUNTIME_CACHE"
	defaultRuntimeProbeTimeout = 3 * time.Second
	defaultRuntimeCacheSubpath = "cache/modelfit-runtime-probe.json"
)

type runtimeProbeCache struct {
	GOOS          string                   `json:"goos"`
	GOARCH        string                   `json:"goarch"`
	BinaryPath    string                   `json:"binary_path,omitempty"`
	BinarySize    int64                    `json:"binary_size,omitempty"`
	BinaryMtimeNs int64                    `json:"binary_mtime_ns,omitempty"`
	Runtime       RuntimeCapabilityProfile `json:"runtime"`
}

type windowsVideoController struct {
	Name       string      `json:"Name"`
	AdapterRAM interface{} `json:"AdapterRAM"`
}

var (
	virtualMemoryFn      = mem.VirtualMemory
	cpuInfoFn            = cpu.Info
	nvidiaSMIFunc        = defaultNvidiaSMI
	macGPUProfileFunc    = defaultMacGPUProfile
	rocmGPUDevicesFunc   = defaultROCMGPUDevices
	linuxPCIGPUDevicesFn = defaultLinuxPCIGPUDevices
	windowsGPUDevicesFn  = defaultWindowsGPUDevices
	lookPathFn           = exec.LookPath
	statFn               = os.Stat
	runtimeProbeFunc     = defaultRuntimeProbe
)

func DetectSystemProfile() (SystemProfile, error) {
	return detectSystemProfile(context.Background())
}

func detectSystemProfile(ctx context.Context) (SystemProfile, error) {
	hardware, err := detectHardwareProfile()
	if err != nil {
		return SystemProfile{}, err
	}
	runtimeProfile, err := detectRuntimeCapabilities(ctx, hardware)
	if err != nil {
		return mergeSystemProfile(hardware, RuntimeCapabilityProfile{
			SelectedBackend: BackendCPU,
			Confidence:      DetectionConfidenceUnknown,
			ProbeSupported:  true,
			ReasonCodes:     []DiagnosticReason{ReasonRuntimeProbeFailed},
		}), nil
	}
	return mergeSystemProfile(hardware, runtimeProfile), nil
}

func detectHardwareProfile() (HardwareProfile, error) {
	vm, err := virtualMemoryFn()
	if err != nil {
		return HardwareProfile{}, fmt.Errorf("detect memory: %w", err)
	}

	hardware := HardwareProfile{
		TotalRAMBytes:     vm.Total,
		AvailableRAMBytes: vm.Available,
		CPUCores:          runtime.NumCPU(),
		GPUs:              []GPUDevice{},
	}
	if info, err := cpuInfoFn(); err == nil && len(info) > 0 {
		hardware.CPUName = strings.TrimSpace(info[0].ModelName)
	}

	switch runtime.GOOS {
	case "darwin":
		hardware.GPUs = append(hardware.GPUs, detectDarwinGPUs(hardware)...)
	case "windows":
		hardware.GPUs = append(hardware.GPUs, windowsGPUDevicesFn()...)
	default:
		hardware.GPUs = append(hardware.GPUs, detectLinuxGPUs(hardware)...)
	}

	for i := range hardware.GPUs {
		hardware.GPUs[i].ReasonCodes = normalizeReasonCodes(hardware.GPUs[i].ReasonCodes)
	}
	return hardware, nil
}

func detectDarwinGPUs(hardware HardwareProfile) []GPUDevice {
	gpus := []GPUDevice{}
	if runtime.GOARCH == "arm64" {
		gpus = append(gpus, GPUDevice{
			ID:                "apple-gpu-0",
			Vendor:            "apple",
			Name:              "Apple GPU",
			BackendCandidates: []Backend{BackendMetal},
			Integrated:        true,
			UnifiedMemory:     true,
		})
		hardware.UnifiedMemory = true
	}
	if name, count, err := macGPUProfileFunc(); err == nil && count > 0 {
		for i := 0; i < count; i++ {
			if len(gpus) > i {
				gpus[i].Name = name
				continue
			}
			gpus = append(gpus, GPUDevice{
				ID:                fmt.Sprintf("darwin-gpu-%d", i),
				Vendor:            inferVendor(name),
				Name:              name,
				BackendCandidates: []Backend{BackendMetal},
				Integrated:        runtime.GOARCH == "arm64",
				Discrete:          runtime.GOARCH != "arm64",
				UnifiedMemory:     runtime.GOARCH == "arm64",
			})
		}
	}
	return gpus
}

func detectLinuxGPUs(hardware HardwareProfile) []GPUDevice {
	gpus := []GPUDevice{}
	if name, count, totalVRAMBytes, err := nvidiaSMIFunc(); err == nil && count > 0 {
		perGPU := totalVRAMBytes / uint64(count)
		for i := 0; i < count; i++ {
			gpus = append(gpus, GPUDevice{
				ID:                fmt.Sprintf("nvidia-%d", i),
				Vendor:            "nvidia",
				Name:              name,
				BackendCandidates: []Backend{BackendCUDA, BackendVulkan},
				TotalMemoryBytes:  perGPU,
				Discrete:          true,
			})
		}
	}
	gpus = appendGPUDevices(gpus, rocmGPUDevicesFunc()...)
	gpus = appendGPUDevices(gpus, linuxPCIGPUDevicesFn()...)
	if hasHybridNVIDIA(gpus) {
		for i := range gpus {
			if gpus[i].Vendor == "nvidia" && gpus[i].Discrete {
				gpus[i].ReasonCodes = append(gpus[i].ReasonCodes, ReasonHybridGPUPresentOffload)
			}
		}
	}
	return gpus
}

func detectRuntimeCapabilities(ctx context.Context, hardware HardwareProfile) (RuntimeCapabilityProfile, error) {
	profile := RuntimeCapabilityProfile{
		SelectedBackend: BackendCPU,
		Confidence:      DetectionConfidenceUnknown,
		ProbeSupported:  true,
	}

	binaryPath := strings.TrimSpace(os.Getenv(modelFitLlamaBinaryEnv))
	if binaryPath == "" {
		if found, err := lookPathFn("llama-server"); err == nil {
			binaryPath = found
		}
	}
	profile.LlamaBinaryPath = binaryPath
	if binaryPath == "" {
		profile.ReasonCodes = []DiagnosticReason{ReasonRuntimeBinaryMissing}
		return finalizeRuntimeProfile(profile, hardware), nil
	}

	info, err := statFn(binaryPath)
	if err != nil {
		profile.ReasonCodes = []DiagnosticReason{ReasonRuntimeBinaryMissing}
		return finalizeRuntimeProfile(profile, hardware), nil
	}
	profile.BinaryFound = true

	cachePath := strings.TrimSpace(os.Getenv(modelFitRuntimeCacheEnv))
	if cachePath == "" {
		cachePath = forgepath.Resolve(defaultRuntimeCacheSubpath)
	}
	if cached, ok := readRuntimeProbeCache(cachePath, binaryPath, info); ok {
		cached.ProbeCached = true
		return finalizeRuntimeProfile(cached, hardware), nil
	}

	probed, err := runtimeProbeFunc(ctx, binaryPath)
	if err != nil {
		profile.ReasonCodes = []DiagnosticReason{ReasonRuntimeProbeFailed}
		writeRuntimeProbeCache(cachePath, binaryPath, info, profile)
		return finalizeRuntimeProfile(profile, hardware), nil
	}
	writeRuntimeProbeCache(cachePath, binaryPath, info, probed)
	return finalizeRuntimeProfile(probed, hardware), nil
}

func finalizeRuntimeProfile(profile RuntimeCapabilityProfile, hardware HardwareProfile) RuntimeCapabilityProfile {
	profile.ReasonCodes = append([]DiagnosticReason(nil), profile.ReasonCodes...)
	if len(profile.UsableAccelerators) == 0 {
		profile.RuntimeAvailable = false
		if len(profile.ReasonCodes) == 0 {
			profile.ReasonCodes = append(profile.ReasonCodes, ReasonNoRuntimeDevices)
		}
		if hasVendor(hardware.GPUs, "nvidia") {
			profile.ReasonCodes = append(profile.ReasonCodes, ReasonNVIDIAPresentRuntimeCPUOnly)
		}
		if hasVendor(hardware.GPUs, "amd") {
			profile.ReasonCodes = append(profile.ReasonCodes, ReasonAMDPresentRuntimeCPUOnly)
			if !containsReason(profile.ReasonCodes, ReasonAMDDetectedRocmUnavailable) {
				profile.ReasonCodes = append(profile.ReasonCodes, ReasonAMDDetectedRocmUnavailable)
			}
		}
		if hasVendor(hardware.GPUs, "intel") {
			profile.ReasonCodes = append(profile.ReasonCodes, ReasonIntelPresentRuntimeCPUOnly)
		}
		profile.SelectedBackend = BackendCPU
		profile.Confidence = maxConfidence(profile.Confidence, DetectionConfidenceHeuristic)
		return normalizeRuntimeProfile(profile)
	}

	profile.RuntimeAvailable = true
	if profile.SelectedBackend == "" {
		profile.SelectedBackend = profile.UsableAccelerators[0].Backend
	}
	profile.Confidence = maxConfidence(profile.Confidence, DetectionConfidenceProbe)
	return normalizeRuntimeProfile(profile)
}

func normalizeRuntimeProfile(profile RuntimeCapabilityProfile) RuntimeCapabilityProfile {
	profile.ReasonCodes = normalizeReasonCodes(profile.ReasonCodes)
	for i := range profile.UsableAccelerators {
		profile.UsableAccelerators[i].ReasonCodes = normalizeReasonCodes(profile.UsableAccelerators[i].ReasonCodes)
	}
	return profile
}

func mergeSystemProfile(hardware HardwareProfile, runtimeProfile RuntimeCapabilityProfile) SystemProfile {
	mergedGPUs := append([]GPUDevice(nil), hardware.GPUs...)
	var totalVRAM uint64
	for i := range mergedGPUs {
		totalVRAM += mergedGPUs[i].TotalMemoryBytes
		for _, accel := range runtimeProfile.UsableAccelerators {
			if mergedGPUs[i].ID == accel.ID {
				mergedGPUs[i].RuntimeUsable = true
				mergedGPUs[i].RuntimeBackend = accel.Backend
				mergedGPUs[i].ReasonCodes = normalizeReasonCodes(append(mergedGPUs[i].ReasonCodes, accel.ReasonCodes...))
			}
		}
	}

	profile := SystemProfile{
		TotalRAMBytes:             hardware.TotalRAMBytes,
		AvailableRAMBytes:         hardware.AvailableRAMBytes,
		CPUCores:                  hardware.CPUCores,
		HasGPU:                    len(mergedGPUs) > 0,
		GPUCount:                  len(mergedGPUs),
		TotalVRAMBytes:            totalVRAM,
		Backend:                   runtimeProfile.SelectedBackend,
		UnifiedMemory:             hardware.UnifiedMemory,
		CPUName:                   hardware.CPUName,
		RuntimeUsableAcceleration: runtimeProfile.RuntimeAvailable,
		Confidence:                runtimeProfile.Confidence,
		ReasonCodes:               append([]DiagnosticReason(nil), runtimeProfile.ReasonCodes...),
		Hardware: HardwareProfile{
			TotalRAMBytes:     hardware.TotalRAMBytes,
			AvailableRAMBytes: hardware.AvailableRAMBytes,
			CPUCores:          hardware.CPUCores,
			CPUName:           hardware.CPUName,
			UnifiedMemory:     hardware.UnifiedMemory,
			GPUs:              mergedGPUs,
		},
		Runtime: runtimeProfile,
	}
	if len(runtimeProfile.UsableAccelerators) > 0 {
		selected := runtimeProfile.UsableAccelerators[0]
		profile.GPUName = selected.Name
		profile.SelectedAcceleratorID = selected.ID
		profile.UnifiedMemory = profile.UnifiedMemory || selected.UnifiedMemory
	} else if len(mergedGPUs) > 0 {
		profile.GPUName = mergedGPUs[0].Name
		profile.Backend = BackendCPU
	}
	return profile
}

func defaultROCMGPUDevices() []GPUDevice {
	cmd := exec.Command("rocm-smi", "--showproductname", "--showmeminfo", "vram", "--json")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var payload map[string]map[string]interface{}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil
	}
	gpus := []GPUDevice{}
	for key, values := range payload {
		name := firstString(values, "Card Series", "Card series", "Card Model")
		memBytes := parseMemoryBytes(firstString(values, "VRAM Total Memory (B)", "VRAM Total Memory", "VRAM Total Used Memory (B)"))
		if name == "" && memBytes == 0 {
			continue
		}
		gpus = append(gpus, GPUDevice{
			ID:                "rocm-" + sanitizeDeviceID(key),
			Vendor:            "amd",
			Name:              name,
			BackendCandidates: []Backend{BackendROCM, BackendVulkan},
			TotalMemoryBytes:  memBytes,
			Discrete:          true,
			ReasonCodes:       []DiagnosticReason{ReasonAMDDetectedRocmUnavailable},
		})
	}
	return gpus
}

func defaultLinuxPCIGPUDevices() []GPUDevice {
	cmd := exec.Command("lspci")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(string(out), "\n")
	gpus := []GPUDevice{}
	for _, line := range lines {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "vga compatible controller") &&
			!strings.Contains(lower, "3d controller") &&
			!strings.Contains(lower, "display controller") {
			continue
		}
		vendor := inferVendor(line)
		if vendor == "unknown" || vendor == "nvidia" || vendor == "amd" {
			continue
		}
		name := strings.TrimSpace(line)
		discrete := strings.Contains(lower, "arc")
		integrated := !discrete && vendor == "intel"
		reasons := []DiagnosticReason{}
		if integrated {
			reasons = append(reasons, ReasonIntelIntegratedSharedMemory)
		}
		gpus = append(gpus, GPUDevice{
			ID:                "pci-" + sanitizeDeviceID(strings.SplitN(line, " ", 2)[0]),
			Vendor:            vendor,
			Name:              name,
			BackendCandidates: []Backend{BackendVulkan, BackendSYCL},
			Integrated:        integrated,
			Discrete:          discrete,
			UnifiedMemory:     integrated,
			ReasonCodes:       reasons,
		})
	}
	return gpus
}

func defaultWindowsGPUDevices() []GPUDevice {
	cmd := exec.Command("powershell", "-NoProfile", "-Command", "Get-CimInstance Win32_VideoController | Select-Object Name,AdapterRAM | ConvertTo-Json -Compress")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var single windowsVideoController
	if err := json.Unmarshal(out, &single); err == nil && single.Name != "" {
		return []GPUDevice{gpuFromWindowsController(single, 0)}
	}
	var many []windowsVideoController
	if err := json.Unmarshal(out, &many); err != nil {
		return nil
	}
	gpus := make([]GPUDevice, 0, len(many))
	for i, controller := range many {
		if controller.Name == "" {
			continue
		}
		gpus = append(gpus, gpuFromWindowsController(controller, i))
	}
	return gpus
}

func gpuFromWindowsController(controller windowsVideoController, index int) GPUDevice {
	vendor := inferVendor(controller.Name)
	discrete := vendor == "nvidia" || strings.Contains(strings.ToLower(controller.Name), "arc")
	integrated := vendor == "intel" && !strings.Contains(strings.ToLower(controller.Name), "arc")
	backends := []Backend{BackendUnknown}
	switch vendor {
	case "nvidia":
		backends = []Backend{BackendCUDA, BackendVulkan}
	case "amd":
		backends = []Backend{BackendVulkan, BackendROCM}
	case "intel":
		backends = []Backend{BackendVulkan, BackendSYCL}
	}
	reasons := []DiagnosticReason{}
	if integrated {
		reasons = append(reasons, ReasonIntelIntegratedSharedMemory)
	}
	return GPUDevice{
		ID:                fmt.Sprintf("windows-gpu-%d", index),
		Vendor:            vendor,
		Name:              strings.TrimSpace(controller.Name),
		BackendCandidates: backends,
		TotalMemoryBytes:  parseUint64Any(controller.AdapterRAM),
		Integrated:        integrated,
		Discrete:          discrete,
		UnifiedMemory:     integrated,
		ReasonCodes:       reasons,
	}
}

func defaultNvidiaSMI() (string, int, uint64, error) {
	cmd := exec.Command("nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader,nounits")
	out, err := cmd.Output()
	if err != nil {
		return "", 0, 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return "", 0, 0, errors.New("no nvidia gpus found")
	}
	var total uint64
	var name string
	count := 0
	for _, line := range lines {
		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}
		count++
		if name == "" {
			name = strings.TrimSpace(parts[0])
		}
		memMB, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil {
			continue
		}
		total += memMB * 1024 * 1024
	}
	if count == 0 {
		return "", 0, 0, errors.New("no parseable nvidia gpu rows")
	}
	return name, count, total, nil
}

func defaultMacGPUProfile() (string, int, error) {
	cmd := exec.Command("system_profiler", "SPDisplaysDataType", "-json")
	out, err := cmd.Output()
	if err != nil {
		return "", 0, err
	}
	var payload struct {
		Displays []map[string]any `json:"SPDisplaysDataType"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return "", 0, err
	}
	if len(payload.Displays) == 0 {
		return "", 0, errors.New("no mac gpu devices")
	}
	name, _ := payload.Displays[0]["sppci_model"].(string)
	return strings.TrimSpace(name), len(payload.Displays), nil
}

func defaultRuntimeProbe(ctx context.Context, binaryPath string) (RuntimeCapabilityProfile, error) {
	probeCtx, cancel := context.WithTimeout(ctx, defaultRuntimeProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, binaryPath, "--list-devices")
	out, err := cmd.Output()
	if err != nil {
		return RuntimeCapabilityProfile{
			LlamaBinaryPath: binaryPath,
			BinaryFound:     true,
			ProbeSupported:  true,
			SelectedBackend: BackendCPU,
			Confidence:      DetectionConfidenceHeuristic,
			ReasonCodes:     []DiagnosticReason{ReasonRuntimeProbeFailed},
		}, nil
	}

	lines := parseLlamaDeviceLines(string(out))
	accels := make([]UsableAccelerator, 0, len(lines))
	selectedBackend := BackendCPU
	for idx, line := range lines {
		accel := buildAcceleratorFromProbeLine(line, idx)
		if accel.Backend != BackendUnknown && selectedBackend == BackendCPU {
			selectedBackend = accel.Backend
		}
		accels = append(accels, accel)
	}
	if len(accels) == 0 {
		return RuntimeCapabilityProfile{
			LlamaBinaryPath: binaryPath,
			BinaryFound:     true,
			ProbeSupported:  true,
			SelectedBackend: BackendCPU,
			Confidence:      DetectionConfidenceProbe,
			ProbeLines:      lines,
			ReasonCodes:     []DiagnosticReason{ReasonNoRuntimeDevices},
			CheckedAt:       time.Now().UTC().Format(time.RFC3339),
		}, nil
	}
	if selectedBackend == BackendCPU {
		selectedBackend = accels[0].Backend
	}
	return RuntimeCapabilityProfile{
		LlamaBinaryPath:    binaryPath,
		BinaryFound:        true,
		ProbeSupported:     true,
		RuntimeAvailable:   true,
		SelectedBackend:    selectedBackend,
		Confidence:         DetectionConfidenceProbe,
		UsableAccelerators: accels,
		ProbeLines:         lines,
		ReasonCodes:        []DiagnosticReason{ReasonRuntimeDeviceDetected},
		CheckedAt:          time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func parseLlamaDeviceLines(stdout string) []string {
	lines := strings.Split(stdout, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.Contains(line, ":") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func buildAcceleratorFromProbeLine(line string, idx int) UsableAccelerator {
	label := line
	name := line
	if parts := strings.SplitN(line, ":", 2); len(parts) == 2 {
		label = strings.TrimSpace(parts[0])
		name = strings.TrimSpace(parts[1])
	}
	vendor := inferVendor(name)
	backend := inferBackendFromProbeLine(label + " " + name)
	return UsableAccelerator{
		ID:            sanitizeDeviceID(label),
		Vendor:        vendor,
		Name:          name,
		Backend:       backend,
		Integrated:    vendor == "intel" && !strings.Contains(strings.ToLower(name), "arc"),
		Discrete:      vendor == "nvidia" || vendor == "amd" || strings.Contains(strings.ToLower(name), "arc"),
		UnifiedMemory: vendor == "apple" || (vendor == "intel" && !strings.Contains(strings.ToLower(name), "arc")),
	}
}

func inferBackendFromProbeLine(line string) Backend {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "cuda"):
		return BackendCUDA
	case strings.Contains(lower, "rocm"):
		return BackendROCM
	case strings.Contains(lower, "metal"):
		return BackendMetal
	case strings.Contains(lower, "vulkan"):
		return BackendVulkan
	case strings.Contains(lower, "sycl"):
		return BackendSYCL
	case strings.Contains(lower, "nvidia"):
		return BackendCUDA
	case strings.Contains(lower, "apple"):
		return BackendMetal
	case strings.Contains(lower, "amd"), strings.Contains(lower, "intel"):
		return BackendVulkan
	default:
		return BackendUnknown
	}
}

func inferVendor(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "nvidia"):
		return "nvidia"
	case strings.Contains(lower, "amd"), strings.Contains(lower, "radeon"), strings.Contains(lower, "advanced micro devices"):
		return "amd"
	case strings.Contains(lower, "intel"):
		return "intel"
	case strings.Contains(lower, "apple"):
		return "apple"
	default:
		return "unknown"
	}
}

func hasHybridNVIDIA(gpus []GPUDevice) bool {
	return hasVendor(gpus, "nvidia") && hasVendor(gpus, "intel")
}

func hasVendor(gpus []GPUDevice, vendor string) bool {
	for _, gpu := range gpus {
		if gpu.Vendor == vendor {
			return true
		}
	}
	return false
}

func appendGPUDevices(existing []GPUDevice, candidates ...GPUDevice) []GPUDevice {
	for _, candidate := range candidates {
		duplicate := false
		for _, current := range existing {
			if current.ID == candidate.ID || (current.Vendor == candidate.Vendor && current.Name == candidate.Name && current.TotalMemoryBytes == candidate.TotalMemoryBytes) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			existing = append(existing, candidate)
		}
	}
	return existing
}

func normalizeReasonCodes(items []DiagnosticReason) []DiagnosticReason {
	out := make([]DiagnosticReason, 0, len(items))
	seen := map[DiagnosticReason]struct{}{}
	for _, item := range items {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	slices.Sort(out)
	return out
}

func containsReason(items []DiagnosticReason, target DiagnosticReason) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func maxConfidence(a, b DetectionConfidence) DetectionConfidence {
	weight := map[DetectionConfidence]int{
		DetectionConfidenceUnknown:   0,
		DetectionConfidenceHeuristic: 1,
		DetectionConfidenceStrong:    2,
		DetectionConfidenceProbe:     3,
	}
	if weight[b] > weight[a] {
		return b
	}
	return a
}

func parseMemoryBytes(raw string) uint64 {
	raw = strings.TrimSpace(strings.TrimSuffix(raw, " B"))
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err == nil {
		return v
	}
	fields := strings.Fields(raw)
	if len(fields) > 0 {
		if parsed, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
			return parsed
		}
	}
	return 0
}

func parseUint64Any(v interface{}) uint64 {
	switch value := v.(type) {
	case float64:
		return uint64(value)
	case int64:
		return uint64(value)
	case int:
		return uint64(value)
	case string:
		return parseMemoryBytes(value)
	default:
		return 0
	}
}

func firstString(values map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if raw, ok := values[key]; ok {
			if str, ok := raw.(string); ok && strings.TrimSpace(str) != "" {
				return strings.TrimSpace(str)
			}
		}
	}
	return ""
}

func sanitizeDeviceID(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	raw = strings.ReplaceAll(raw, " ", "-")
	raw = strings.ReplaceAll(raw, "/", "-")
	raw = strings.ReplaceAll(raw, ":", "-")
	raw = strings.ReplaceAll(raw, ".", "-")
	if raw == "" {
		return "device"
	}
	return raw
}

func readRuntimeProbeCache(path, binaryPath string, info os.FileInfo) (RuntimeCapabilityProfile, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return RuntimeCapabilityProfile{}, false
	}
	var cached runtimeProbeCache
	if err := json.Unmarshal(raw, &cached); err != nil {
		return RuntimeCapabilityProfile{}, false
	}
	if cached.GOOS != runtime.GOOS || cached.GOARCH != runtime.GOARCH {
		return RuntimeCapabilityProfile{}, false
	}
	if cached.BinaryPath != binaryPath || cached.BinarySize != info.Size() || cached.BinaryMtimeNs != info.ModTime().UnixNano() {
		return RuntimeCapabilityProfile{}, false
	}
	return cached.Runtime, true
}

func writeRuntimeProbeCache(path, binaryPath string, info os.FileInfo, profile RuntimeCapabilityProfile) {
	if strings.TrimSpace(path) == "" {
		return
	}
	cache := runtimeProbeCache{
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		BinaryPath:    binaryPath,
		BinarySize:    info.Size(),
		BinaryMtimeNs: info.ModTime().UnixNano(),
		Runtime:       profile,
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, data, 0o644)
}
