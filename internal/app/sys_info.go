package app

/*
#include <sys/types.h>
#include <sys/sysctl.h>
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/metaspartan/mactop/v2/internal/i18n"
)

var (
	cachedSOCInfoResult SystemInfo
	socInfoOnce         sync.Once
)

type VolumeInfo struct {
	Name      string
	Total     float64
	Used      float64
	Available float64
	UsedPct   float64
}

func getVolumes() []VolumeInfo {
	var volumes []VolumeInfo
	partitions, err := GetNativePartitions(false)
	if err != nil {
		return volumes
	}

	excludedVolumes := map[string]bool{
		"/Volumes/Recovery":   true,
		"/Volumes/Preboot":    true,
		"/Volumes/VM":         true,
		"/Volumes/Update":     true,
		"/Volumes/xarts":      true,
		"/Volumes/iSCPreboot": true,
		"/Volumes/Hardware":   true,
	}

	seen := make(map[string]bool)
	for _, p := range partitions {
		if seen[p.Device] {
			continue
		}
		if !strings.HasPrefix(p.Mountpoint, "/Volumes/") && p.Mountpoint != "/" {
			continue
		}

		excluded := false
		for k := range excludedVolumes {
			if strings.Contains(p.Mountpoint, k) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		usage, err := GetNativeDiskUsage(p.Mountpoint)
		if err != nil || usage.Total == 0 {
			continue
		}
		seen[p.Device] = true
		var name string
		if p.Mountpoint == "/" {
			name = "Mac HD"
		} else {
			name = strings.TrimPrefix(p.Mountpoint, "/Volumes/")
		}
		if len(name) > 12 {
			name = name[:12]
		}
		volumes = append(volumes, VolumeInfo{
			Name:      name,
			Total:     float64(usage.Total) / 1e9,
			Used:      float64(usage.Used) / 1e9,
			Available: float64(usage.Free) / 1e9,
			UsedPct:   usage.UsedPercent,
		})
	}
	return volumes
}

func getSOCInfo() SystemInfo {
	socInfoOnce.Do(func() {
		cachedSOCInfoResult = computeSOCInfo()
	})
	return cachedSOCInfoResult
}

func computeSOCInfo() SystemInfo {
	cpuInfoDict := getCPUInfo()

	// Use authoritative core counts from BuildCoreLabels which matches the gauge
	// and accurately cross-references IORegistry with sysctl perflevels.
	_, eCount, pCount, sCount, _ := BuildCoreLabels()

	// Fallback: if BuildCoreLabels failed (IORegistry unavailable), use sysctl directly
	if eCount == 0 && pCount == 0 && sCount == 0 {
		coreTiers := getPerfLevelCores()
		eCount = coreTiers["E"]
		pCount = coreTiers["P"]
		sCount = coreTiers["S"]
	}

	coreCount, _ := strconv.Atoi(cpuInfoDict["machdep.cpu.core_count"])
	gpuCoreCountStr := getGPUCores()
	gpuCoreCount, _ := strconv.Atoi(gpuCoreCountStr)
	if gpuCoreCount == 0 && gpuCoreCountStr != "?" {
	}

	return SystemInfo{
		Name:         cpuInfoDict["machdep.cpu.brand_string"],
		CoreCount:    coreCount,
		ECoreCount:   eCount,
		PCoreCount:   pCount,
		SCoreCount:   sCount,
		GPUCoreCount: gpuCoreCount,
	}
}

// sysctlStringByName reads a sysctl string value directly via the C API,
// avoiding the overhead of spawning an external process.
func sysctlStringByName(name string) (string, error) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	var size C.size_t
	if C.sysctlbyname(cName, nil, &size, nil, 0) != 0 {
		return "", fmt.Errorf("sysctl size query failed for %s", name)
	}
	buf := C.malloc(size)
	defer C.free(buf)
	if C.sysctlbyname(cName, buf, &size, nil, 0) != 0 {
		return "", fmt.Errorf("sysctl value query failed for %s", name)
	}
	return C.GoString((*C.char)(buf)), nil
}

// sysctlIntByName reads a sysctl integer value directly via the C API.
func sysctlIntByName(name string) (int, error) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	var val C.int
	size := C.size_t(unsafe.Sizeof(val))
	if C.sysctlbyname(cName, unsafe.Pointer(&val), &size, nil, 0) != 0 {
		return 0, fmt.Errorf("sysctl int query failed for %s", name)
	}
	return int(val), nil
}

func getCPUInfo() map[string]string {
	cpuInfoDict := make(map[string]string)

	brand, err := sysctlStringByName("machdep.cpu.brand_string")
	if err != nil {
		stderrLogger.Fatalf("failed to get CPU brand string: %v", err)
	}
	cpuInfoDict["machdep.cpu.brand_string"] = brand

	coreCount, err := sysctlIntByName("machdep.cpu.core_count")
	if err != nil {
		stderrLogger.Fatalf("failed to get CPU core count: %v", err)
	}
	cpuInfoDict["machdep.cpu.core_count"] = strconv.Itoa(coreCount)

	return cpuInfoDict
}

// getPerfLevelCores dynamically queries sysctl hw.perflevel* to discover
// core types and counts. Returns a map: "E" -> count, "P" -> count, "S" -> count.
// Works across all M-series chips without hardcoding perflevel indices.
func getPerfLevelCores() map[string]int {
	result := map[string]int{"E": 0, "P": 0, "S": 0}

	// Get number of performance levels via direct sysctl (no subprocess)
	nperflevels, err := sysctlIntByName("hw.nperflevels")
	if err != nil || nperflevels == 0 {
		return getPerfLevelCoresLegacy()
	}

	// Query each perflevel for its name and core count via direct sysctl
	for i := 0; i < nperflevels; i++ {
		name, err := sysctlStringByName(fmt.Sprintf("hw.perflevel%d.name", i))
		if err != nil {
			continue
		}
		count, err := sysctlIntByName(fmt.Sprintf("hw.perflevel%d.logicalcpu", i))
		if err != nil {
			continue
		}

		// Map perflevel names to core type letters
		switch {
		case strings.HasPrefix(name, "Super"):
			result["S"] += count
		case strings.HasPrefix(name, "Performance"):
			result["P"] += count
		case strings.HasPrefix(name, "Efficiency"):
			result["E"] += count
		default:
			// Unknown tier — treat as P-core for safety
			result["P"] += count
		}
	}

	return result
}

// getPerfLevelCoresLegacy is the fallback for systems without hw.nperflevels
func getPerfLevelCoresLegacy() map[string]int {
	result := map[string]int{"E": 0, "P": 0, "S": 0}

	pVal, err := sysctlIntByName("hw.perflevel0.logicalcpu")
	if err == nil {
		result["P"] = pVal
	}
	eVal, err := sysctlIntByName("hw.perflevel1.logicalcpu")
	if err == nil {
		result["E"] = eVal
	}
	return result
}

func getGPUCores() string {
	count := GetGPUCoreCountFast()
	if count > 0 {
		return strconv.Itoa(count)
	}

	data, err := GetGlobalProfilerData()
	if err != nil {
		stderrLogger.Printf("failed to get global profiler data: %v", err)
		return "?"
	}

	for _, display := range data.DisplayItems {
		if display.Cores != "" {
			return display.Cores
		}
	}
	return "?"
}

// getTotalRAMGB returns the total installed system memory in gigabytes.
func getTotalRAMGB() int {
	memBytes, err := sysctlIntByName("hw.memsize")
	if err != nil || memBytes <= 0 {
		return 0
	}
	return memBytes / (1024 * 1024 * 1024)
}

// GetGPUMaxFreqMHz returns a reasonable nominal maximum GPU frequency
// for the current SoC. This is used to compute frequency-adjusted
// "effective" GPU load in the history_soc layout.
func GetGPUMaxFreqMHz() int {
	// Prefer the real per-chip maximum read from hardware (pmgr voltage
	// states) — correct on every chip including ones newer than the static
	// table below (which has no M5 entries and a generic default).
	if hw := GetMaxGPUFrequency(); hw > 0 {
		return hw
	}

	model := cachedSOCInfoResult.Name
	if model == "" {
		model = getCPUInfo()["machdep.cpu.brand_string"]
	}

	// Approximate nominal max GPU clocks (MHz) based on public data.
	// These are conservative estimates for the highest bin of each family.
	switch {
	case strings.Contains(model, "M4 Max"):
		return 1800
	case strings.Contains(model, "M4 Pro"):
		return 1700
	case strings.Contains(model, "M4"):
		return 1600
	case strings.Contains(model, "M3 Max"):
		return 1600
	case strings.Contains(model, "M3 Pro"):
		return 1500
	case strings.Contains(model, "M3"):
		return 1400
	case strings.Contains(model, "M2 Ultra"), strings.Contains(model, "M2 Max"):
		return 1400
	case strings.Contains(model, "M2 Pro"):
		return 1350
	case strings.Contains(model, "M2"):
		return 1300
	case strings.Contains(model, "M1 Ultra"), strings.Contains(model, "M1 Max"):
		return 1300
	case strings.Contains(model, "M1 Pro"):
		return 1296
	default:
		// Safe default for unknown / future chips
		return 1400
	}
}

type thermalStateLevel int

// Thermal pressure levels mirror macOS's OSThermalPressureLevel scale, exposed
// via the "com.apple.system.thermalpressurelevel" notification and reported by
// `powermetrics --samplers thermal` as "Current pressure level". Using the same
// taxonomy (rather than the coarser 4-state NSProcessInfoThermalState) lets
// mactop's thermal reading match powermetrics exactly.
const (
	thermalStateUnknown  thermalStateLevel = -1
	thermalStateNominal  thermalStateLevel = 0
	thermalStateModerate thermalStateLevel = 1
	thermalStateHeavy    thermalStateLevel = 2
	thermalStateTrapping thermalStateLevel = 3
	thermalStateSleeping thermalStateLevel = 4
)

// getThermalStateLevel reports the current system thermal pressure.
//
// It uses NSProcessInfo.thermalState (via getSocThermalState), which is the
// supported thermal-pressure API on Apple Silicon and matches what the OS and
// `powermetrics --samplers thermal` report. The previous implementation read
// the `machdep.xcpm.cpu_thermal_level` sysctl, but that OID is Intel-only
// (xcpm = Intel power management) and does not exist on Apple Silicon — the
// lookup always failed, pinning the reading to "Nominal" on every M-series
// Mac regardless of actual thermal pressure (issue #71).
//
// NSProcessInfoThermalState values map directly onto our enum:
// Nominal=0, Fair=1, Serious=2, Critical=3.
func getThermalStateLevel() thermalStateLevel {
	switch getSocThermalState() {
	case 0:
		return thermalStateNominal
	case 1:
		return thermalStateModerate
	case 2:
		return thermalStateHeavy
	case 3:
		return thermalStateTrapping
	case 4:
		return thermalStateSleeping
	default:
		return thermalStateUnknown
	}
}

func thermalStateString(level thermalStateLevel) string {
	switch level {
	case thermalStateNominal:
		return i18n.T("Metrics_ThermalNominal")
	case thermalStateModerate:
		return i18n.T("Metrics_ThermalModerate")
	case thermalStateHeavy:
		return i18n.T("Metrics_ThermalHeavy")
	case thermalStateTrapping:
		return i18n.T("Metrics_ThermalTrapping")
	case thermalStateSleeping:
		return i18n.T("Metrics_ThermalSleeping")
	default:
		return i18n.T("Metrics_ThermalUnknown")
	}
}

// thermalStateThrottled reports whether the OS is applying thermal mitigation,
// i.e. any pressure above Nominal (Moderate and up). Used to flag the CPU as
// thermally constrained.
func thermalStateThrottled(level thermalStateLevel) bool {
	return level >= thermalStateModerate
}

func getThermalStateString() (string, bool) {
	level := getThermalStateLevel()
	return thermalStateString(level), thermalStateThrottled(level)
}
