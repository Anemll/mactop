package app

import (
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func startPrometheusServer(port string) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(cpuUsage)
	registry.MustRegister(ecoreUsage)
	registry.MustRegister(pcoreUsage)
	registry.MustRegister(gpuUsage)
	registry.MustRegister(gpuFreqMHz)
	registry.MustRegister(powerUsage)
	registry.MustRegister(socTemp)
	registry.MustRegister(gpuTemp)
	registry.MustRegister(thermalState)
	registry.MustRegister(memoryUsage)
	registry.MustRegister(networkSpeed)
	registry.MustRegister(diskIOSpeed)
	registry.MustRegister(diskIOPS)
	registry.MustRegister(tbNetworkSpeed)
	registry.MustRegister(rdmaAvailable)
	registry.MustRegister(scoreUsage)
	registry.MustRegister(dramBandwidth)
	registry.MustRegister(cpuCoreUsage)
	registry.MustRegister(systemInfoGauge)
	registry.MustRegister(fanRPM)
	registry.MustRegister(tempSensorGauge)

	initializePrometheusSeries(getSOCInfo())

	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})

	http.Handle("/metrics", handler)
	go func() {
		err := http.ListenAndServe(":"+port, nil)
		if err != nil {
			stderrLogger.Printf("Failed to start Prometheus metrics server: %v\n", err)
		}
	}()
}

type prometheusMetricsSnapshot struct {
	SystemInfo   SystemInfo
	CPUMetrics   CPUMetrics
	GPUMetrics   GPUMetrics
	Memory       MemoryMetrics
	TBNetStats   []ThunderboltNetStats
	RDMAStatus   RDMAStatus
	ThermalLevel thermalStateLevel
}

func initializePrometheusSeries(sysInfo SystemInfo) {
	updatePrometheusSystemInfo(sysInfo)

	for _, component := range []string{"cpu", "gpu", "ane", "dram", "gpu_sram", "system", "total"} {
		powerUsage.With(prometheus.Labels{"component": component}).Set(0)
	}
	for _, memoryType := range []string{"used", "total", "swap_used", "swap_total"} {
		memoryUsage.With(prometheus.Labels{"type": memoryType}).Set(0)
	}
	for _, direction := range []string{"upload", "download"} {
		networkSpeed.With(prometheus.Labels{"direction": direction}).Set(0)
	}
	for _, operation := range []string{"read", "write"} {
		diskIOSpeed.With(prometheus.Labels{"operation": operation}).Set(0)
		diskIOPS.With(prometheus.Labels{"operation": operation}).Set(0)
	}
	for _, direction := range []string{"read", "write", "combined"} {
		dramBandwidth.With(prometheus.Labels{"direction": direction}).Set(0)
	}
	for _, direction := range []string{"upload", "download"} {
		tbNetworkSpeed.With(prometheus.Labels{"direction": direction}).Set(0)
	}
	for i := 0; i < sysInfo.CoreCount; i++ {
		cpuCoreUsage.With(prometheus.Labels{"core": fmt.Sprintf("%d", i), "type": coreTypeForIndex(i, sysInfo)}).Set(0)
	}
}

func normalizeSocMetricsPower(m SocMetrics) SocMetrics {
	componentSum := m.TotalPower
	totalPower := m.SystemPower
	if totalPower < componentSum {
		totalPower = componentSum
	}
	m.SystemPower = totalPower - componentSum
	m.TotalPower = totalPower
	return m
}

func cpuMetricsFromSoc(m SocMetrics, coreUsages []float64, avgUsage float64, throttled bool) CPUMetrics {
	return CPUMetrics{
		CPUW:            m.CPUPower,
		GPUW:            m.GPUPower,
		ANEW:            m.ANEPower,
		DRAMW:           m.DRAMPower,
		GPUSRAMW:        m.GPUSRAMPower,
		SystemW:         m.SystemPower,
		PackageW:        m.TotalPower,
		Throttled:       throttled,
		CPUTemp:         float64(m.CPUTemp),
		GPUTemp:         float64(m.GPUTemp),
		EClusterActive:  int(m.EClusterActive),
		PClusterActive:  int(m.PClusterActive),
		EClusterFreqMHz: int(m.EClusterFreqMHz),
		PClusterFreqMHz: int(m.PClusterFreqMHz),
		SClusterActive:  int(m.SClusterActive),
		SClusterFreqMHz: int(m.SClusterFreqMHz),
		DRAMReadBW:      m.DRAMReadBW,
		DRAMWriteBW:     m.DRAMWriteBW,
		DRAMBWCombined:  m.DRAMBWCombined,
		Fans:            m.Fans,
		TempSensors:     m.TempSensors,
		CoreUsages:      coreUsages,
		AvgUsage:        avgUsage,
	}
}

func gpuMetricsFromSoc(m SocMetrics) GPUMetrics {
	return GPUMetrics{
		FreqMHz:       int(m.GPUFreqMHz),
		ActivePercent: m.GPUActive,
		Power:         m.GPUPower + m.GPUSRAMPower,
		Temp:          m.GPUTemp,
	}
}

func averageCPUUsage(coreUsages []float64) float64 {
	if len(coreUsages) == 0 {
		return 0
	}
	total := 0.0
	for _, usage := range coreUsages {
		total += usage
	}
	return total / float64(len(coreUsages))
}

func averageCoreRange(coreUsages []float64, start, count int) float64 {
	if count <= 0 || start < 0 || len(coreUsages) < start+count {
		return 0
	}
	total := 0.0
	for _, usage := range coreUsages[start : start+count] {
		total += usage
	}
	return total / float64(count)
}

func calculateCoreAveragesForSystem(coreUsages []float64, sysInfo SystemInfo) (ecoreAvg, pcoreAvg, scoreAvg float64) {
	ecoreAvg = averageCoreRange(coreUsages, 0, sysInfo.ECoreCount)
	pcoreAvg = averageCoreRange(coreUsages, sysInfo.ECoreCount, sysInfo.PCoreCount)
	scoreAvg = averageCoreRange(coreUsages, sysInfo.ECoreCount+sysInfo.PCoreCount, sysInfo.SCoreCount)
	return ecoreAvg, pcoreAvg, scoreAvg
}

func coreTypeForIndex(index int, sysInfo SystemInfo) string {
	if index < sysInfo.ECoreCount {
		return "e"
	}
	if index < sysInfo.ECoreCount+sysInfo.PCoreCount {
		return "p"
	}
	return "s"
}

func prometheusThermalStateValue(level thermalStateLevel) float64 {
	switch level {
	case thermalStateFair:
		return 1
	case thermalStateSerious:
		return 2
	case thermalStateCritical:
		return 3
	default:
		return 0
	}
}

func updatePrometheusSystemInfo(sysInfo SystemInfo) {
	systemInfoGauge.With(prometheus.Labels{
		"model":          sysInfo.Name,
		"core_count":     fmt.Sprintf("%d", sysInfo.CoreCount),
		"e_core_count":   fmt.Sprintf("%d", sysInfo.ECoreCount),
		"p_core_count":   fmt.Sprintf("%d", sysInfo.PCoreCount),
		"s_core_count":   fmt.Sprintf("%d", sysInfo.SCoreCount),
		"gpu_core_count": fmt.Sprintf("%d", sysInfo.GPUCoreCount),
	}).Set(1)
}

func publishPrometheusMetrics(snapshot prometheusMetricsSnapshot) {
	updatePrometheusSystemInfo(snapshot.SystemInfo)

	cpuMetrics := snapshot.CPUMetrics
	totalUsage := cpuMetrics.AvgUsage
	if len(cpuMetrics.CoreUsages) > 0 {
		totalUsage = averageCPUUsage(cpuMetrics.CoreUsages)
	}
	ecoreAvg, pcoreAvg, scoreAvg := calculateCoreAveragesForSystem(cpuMetrics.CoreUsages, snapshot.SystemInfo)

	cpuUsage.Set(totalUsage)
	ecoreUsage.Set(ecoreAvg)
	pcoreUsage.Set(pcoreAvg)
	scoreUsage.Set(scoreAvg)
	powerUsage.With(prometheus.Labels{"component": "cpu"}).Set(cpuMetrics.CPUW)
	powerUsage.With(prometheus.Labels{"component": "gpu"}).Set(cpuMetrics.GPUW)
	powerUsage.With(prometheus.Labels{"component": "ane"}).Set(cpuMetrics.ANEW)
	powerUsage.With(prometheus.Labels{"component": "dram"}).Set(cpuMetrics.DRAMW)
	powerUsage.With(prometheus.Labels{"component": "gpu_sram"}).Set(cpuMetrics.GPUSRAMW)
	powerUsage.With(prometheus.Labels{"component": "system"}).Set(cpuMetrics.SystemW)
	powerUsage.With(prometheus.Labels{"component": "total"}).Set(cpuMetrics.PackageW)
	socTemp.Set(cpuMetrics.CPUTemp)
	gpuTemp.Set(cpuMetrics.GPUTemp)
	thermalState.Set(prometheusThermalStateValue(snapshot.ThermalLevel))
	dramBandwidth.With(prometheus.Labels{"direction": "read"}).Set(cpuMetrics.DRAMReadBW)
	dramBandwidth.With(prometheus.Labels{"direction": "write"}).Set(cpuMetrics.DRAMWriteBW)
	dramBandwidth.With(prometheus.Labels{"direction": "combined"}).Set(cpuMetrics.DRAMBWCombined)

	memoryUsage.With(prometheus.Labels{"type": "used"}).Set(float64(snapshot.Memory.Used) / 1024 / 1024 / 1024)
	memoryUsage.With(prometheus.Labels{"type": "total"}).Set(float64(snapshot.Memory.Total) / 1024 / 1024 / 1024)
	memoryUsage.With(prometheus.Labels{"type": "swap_used"}).Set(float64(snapshot.Memory.SwapUsed) / 1024 / 1024 / 1024)
	memoryUsage.With(prometheus.Labels{"type": "swap_total"}).Set(float64(snapshot.Memory.SwapTotal) / 1024 / 1024 / 1024)

	for i, usage := range cpuMetrics.CoreUsages {
		cpuCoreUsage.With(prometheus.Labels{"core": fmt.Sprintf("%d", i), "type": coreTypeForIndex(i, snapshot.SystemInfo)}).Set(usage)
	}

	gpuUsage.Set(snapshot.GPUMetrics.ActivePercent)
	gpuFreqMHz.Set(float64(snapshot.GPUMetrics.FreqMHz))

	updatePrometheusThunderbolt(snapshot.TBNetStats, snapshot.RDMAStatus)
	updatePrometheusSensors(cpuMetrics.Fans, cpuMetrics.TempSensors)
}

func updatePrometheusThunderbolt(tbStats []ThunderboltNetStats, rdmaStatus RDMAStatus) {
	var totalBytesIn, totalBytesOut float64
	for _, stat := range tbStats {
		totalBytesIn += stat.BytesInPerSec
		totalBytesOut += stat.BytesOutPerSec
	}
	tbNetworkSpeed.With(prometheus.Labels{"direction": "download"}).Set(totalBytesIn)
	tbNetworkSpeed.With(prometheus.Labels{"direction": "upload"}).Set(totalBytesOut)
	if rdmaStatus.Available {
		rdmaAvailable.Set(1)
	} else {
		rdmaAvailable.Set(0)
	}
}

func publishPrometheusNetDiskMetrics(metrics NetDiskMetrics) {
	networkSpeed.With(prometheus.Labels{"direction": "upload"}).Set(metrics.OutBytesPerSec)
	networkSpeed.With(prometheus.Labels{"direction": "download"}).Set(metrics.InBytesPerSec)
	diskIOSpeed.With(prometheus.Labels{"operation": "read"}).Set(metrics.ReadKBytesPerSec * 1024)
	diskIOSpeed.With(prometheus.Labels{"operation": "write"}).Set(metrics.WriteKBytesPerSec * 1024)
	diskIOPS.With(prometheus.Labels{"operation": "read"}).Set(metrics.ReadOpsPerSec)
	diskIOPS.With(prometheus.Labels{"operation": "write"}).Set(metrics.WriteOpsPerSec)
}

func GetCPUPercentages() ([]float64, error) {
	currentTimes, err := GetCPUUsage()
	if err != nil {
		return nil, err
	}
	if firstRun {
		lastCPUTimes = currentTimes
		firstRun = false
		return make([]float64, len(currentTimes)), nil
	}
	percentages := make([]float64, len(currentTimes))
	for i := range currentTimes {
		totalDelta := (currentTimes[i].User - lastCPUTimes[i].User) +
			(currentTimes[i].System - lastCPUTimes[i].System) +
			(currentTimes[i].Idle - lastCPUTimes[i].Idle) +
			(currentTimes[i].Nice - lastCPUTimes[i].Nice)

		activeDelta := (currentTimes[i].User - lastCPUTimes[i].User) +
			(currentTimes[i].System - lastCPUTimes[i].System) +
			(currentTimes[i].Nice - lastCPUTimes[i].Nice)

		if totalDelta > 0 {
			percentages[i] = (activeDelta / totalDelta) * 100.0
		}
		if percentages[i] < 0 {
			percentages[i] = 0
		} else if percentages[i] > 100 {
			percentages[i] = 100
		}
	}
	lastCPUTimes = currentTimes
	return percentages, nil
}

func getNetDiskMetrics() NetDiskMetrics {
	var metrics NetDiskMetrics

	netDiskMutex.Lock()
	defer netDiskMutex.Unlock()

	now := time.Now()
	elapsed := now.Sub(lastNetDiskTime).Seconds()
	if elapsed <= 0 {
		elapsed = 1
	}

	// Native Network Metrics
	netMap, err := GetNativeNetworkMetrics()
	if err == nil {
		var totalNet NativeNetMetric
		for _, iface := range netMap {
			totalNet.BytesRecv += iface.BytesRecv
			totalNet.BytesSent += iface.BytesSent
			totalNet.PacketsRecv += iface.PacketsRecv
			totalNet.PacketsSent += iface.PacketsSent
		}

		if lastNetDiskTime.IsZero() {
			lastNetStats = totalNet
		} else {
			metrics.InBytesPerSec = float64(totalNet.BytesRecv-lastNetStats.BytesRecv) / elapsed
			metrics.OutBytesPerSec = float64(totalNet.BytesSent-lastNetStats.BytesSent) / elapsed
			metrics.InPacketsPerSec = float64(totalNet.PacketsRecv-lastNetStats.PacketsRecv) / elapsed
			metrics.OutPacketsPerSec = float64(totalNet.PacketsSent-lastNetStats.PacketsSent) / elapsed
		}
		lastNetStats = totalNet
	}

	// Native Disk Metrics
	diskMap, err := GetNativeDiskMetrics()
	if err == nil {
		var totalDisk NativeDiskMetric
		for _, d := range diskMap {
			totalDisk.ReadBytes += d.ReadBytes
			totalDisk.WriteBytes += d.WriteBytes
			totalDisk.ReadOps += d.ReadOps
			totalDisk.WriteOps += d.WriteOps
		}

		if !lastNetDiskTime.IsZero() {
			metrics.ReadKBytesPerSec = float64(totalDisk.ReadBytes-lastDiskStats.ReadBytes) / elapsed / 1024
			metrics.WriteKBytesPerSec = float64(totalDisk.WriteBytes-lastDiskStats.WriteBytes) / elapsed / 1024
			metrics.ReadOpsPerSec = float64(totalDisk.ReadOps-lastDiskStats.ReadOps) / elapsed
			metrics.WriteOpsPerSec = float64(totalDisk.WriteOps-lastDiskStats.WriteOps) / elapsed
		}
		lastDiskStats = totalDisk
	}

	lastNetDiskTime = now
	return metrics
}

func collectNetDiskMetrics(done chan struct{}, netdiskMetricsChan chan NetDiskMetrics) {
	for {
		start := time.Now()

		netdiskMetrics := getNetDiskMetrics()
		publishPrometheusNetDiskMetrics(netdiskMetrics)
		select {
		case <-done:
			return
		case netdiskMetricsChan <- netdiskMetrics:
		default:
		}

		elapsed := time.Since(start)
		sleepTime := time.Duration(updateInterval)*time.Millisecond - elapsed
		if sleepTime > 0 {
			select {
			case <-time.After(sleepTime):
			case <-interruptChan:
			}
		}
	}
}

// dispatchMetrics sends metrics to channels without blocking, checking done for exit.
func dispatchMetrics(done chan struct{}, cpuCh chan CPUMetrics, gpuCh chan GPUMetrics,
	tbCh chan []ThunderboltNetStats, triggerCh chan struct{},
	cpu CPUMetrics, gpu GPUMetrics, tb []ThunderboltNetStats) bool {
	select {
	case <-done:
		return true
	case cpuCh <- cpu:
	default:
	}
	select {
	case gpuCh <- gpu:
	default:
	}
	select {
	case tbCh <- tb:
	default:
	}
	select {
	case triggerCh <- struct{}{}:
	default:
	}
	return false
}

func collectMetrics(done chan struct{}, cpumetricsChan chan CPUMetrics, gpumetricsChan chan GPUMetrics, tbNetStatsChan chan []ThunderboltNetStats, triggerProcessCollectionChan chan struct{}) {
	// Pre-calculate static info
	sysInfo := getSOCInfo()
	maxGPUFreq := GetMaxGPUFrequency()
	var maxFP32TFLOPs float64
	if maxGPUFreq > 0 && sysInfo.GPUCoreCount > 0 {
		maxFP32TFLOPs = float64(sysInfo.GPUCoreCount) * float64(maxGPUFreq) * 0.000256
	}

	for {
		start := time.Now()

		sampleDuration := updateInterval
		if sampleDuration < 100 {
			sampleDuration = 100
		}

		m := normalizeSocMetricsPower(sampleSocMetrics(sampleDuration / 2))

		thermalLevel := getThermalStateLevel()
		thermalStr := thermalStateString(thermalLevel)
		throttled := thermalStateThrottled(thermalLevel)
		rdmaStatus := CheckRDMAAvailable()
		rdmaStat := rdmaStatus.Status

		coreUsages, _ := GetCPUPercentages()
		avgUsage := averageCPUUsage(coreUsages)
		cpuMetrics := cpuMetricsFromSoc(m, coreUsages, avgUsage, throttled)
		gpuMetrics := gpuMetricsFromSoc(m)
		tbNetStats := GetThunderboltNetStats()
		publishPrometheusMetrics(prometheusMetricsSnapshot{
			SystemInfo:   sysInfo,
			CPUMetrics:   cpuMetrics,
			GPUMetrics:   gpuMetrics,
			Memory:       getMemoryMetrics(),
			TBNetStats:   tbNetStats,
			RDMAStatus:   rdmaStatus,
			ThermalLevel: thermalLevel,
		})

		if dispatchMetrics(done, cpumetricsChan, gpumetricsChan, tbNetStatsChan, triggerProcessCollectionChan, cpuMetrics, gpuMetrics, tbNetStats) {
			return
		}

		// Push to menubar worker — snapshot net metrics under lock to avoid race
		if menubar {
			renderMutex.Lock()
			nd := lastNetDiskMetrics
			renderMutex.Unlock()
			pushMenuBarMetricsToWorker(m, cpuMetrics, gpuMetrics, nd, sysInfo, maxFP32TFLOPs, cpuMetrics.AvgUsage, thermalStr, rdmaStat)
		}

		// Push to overlay worker
		if overlay {
			renderMutex.Lock()
			nd := lastNetDiskMetrics
			renderMutex.Unlock()
			pushOverlayMetrics(m, cpuMetrics, gpuMetrics, nd, sysInfo, maxFP32TFLOPs, cpuMetrics.AvgUsage, thermalStr, rdmaStat)
		}

		elapsed := time.Since(start)
		sleepTime := time.Duration(updateInterval)*time.Millisecond - elapsed
		if sleepTime > 0 {
			select {
			case <-time.After(sleepTime):
			case <-interruptChan:
			}
		}
	}
}

func updatePrometheusSensors(fans []FanInfo, sensors []TempSensor) {
	for _, fan := range fans {
		fanRPM.With(prometheus.Labels{"fan_id": fmt.Sprintf("%d", fan.ID), "fan_name": fan.Name}).Set(float64(fan.ActualRPM))
	}
	for _, sensor := range sensors {
		tempSensorGauge.With(prometheus.Labels{"key": sensor.Key, "name": sensor.Name}).Set(sensor.Value)
	}
}

func collectProcessMetrics(done chan struct{}, processMetricsChan chan []ProcessMetrics, triggerChan chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-triggerChan:
			renderMutex.Lock()
			sysPct := lastGPUMetrics.ActivePercent
			renderMutex.Unlock()

			if processes, err := getProcessList(sysPct); err == nil {
				processMetricsChan <- processes
			} else {
				stderrLogger.Printf("Error getting process list: %v\n", err)
			}
		}
	}
}

func getMemoryMetrics() MemoryMetrics {
	native, err := GetNativeMemoryMetrics()
	if err != nil {
		stderrLogger.Printf("Error getting native memory metrics: %v\n", err)
		return MemoryMetrics{}
	}
	return MemoryMetrics{
		Total:     native.Total,
		Used:      native.Used,
		Available: native.Available,
		SwapTotal: native.SwapTotal,
		SwapUsed:  native.SwapUsed,
	}
}
