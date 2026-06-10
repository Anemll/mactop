package app

import (
	"os"

	ui "github.com/metaspartan/gotui/v5"
)

// fanControlWriteFailed records whether the most recent fan-control SMC write
// failed (or had no effect). SMC fan writes require root — without sudo every
// write returns kIOReturnNotPrivileged — and newer macOS can also reject them.
// Surfacing this lets the UI explain why fan control "does nothing" instead of
// silently swallowing the error.
var fanControlWriteFailed bool

// fanControlHasRoot reports whether mactop is running with the privileges
// required to write SMC fan keys.
func fanControlHasRoot() bool { return os.Geteuid() == 0 }

// noteFanWrites records the outcome of a batch of fan-control writes: failed if
// any returned an error.
func noteFanWrites(errs ...error) {
	for _, e := range errs {
		if e != nil {
			fanControlWriteFailed = true
			return
		}
	}
	fanControlWriteFailed = false
}

func toggleInfoLayout() {
	renderMutex.Lock()
	if currentConfig.DefaultLayout == LayoutInfo {
		if lastActiveLayout != "" {
			currentConfig.DefaultLayout = lastActiveLayout
		} else {
			currentConfig.DefaultLayout = LayoutDefault
		}
		for i, layout := range layoutOrder {
			if layout == currentConfig.DefaultLayout {
				currentLayoutNum = i
				break
			}
		}
	} else {
		lastActiveLayout = currentConfig.DefaultLayout
		currentConfig.DefaultLayout = LayoutInfo
		for i, layout := range layoutOrder {
			if layout == LayoutInfo {
				currentLayoutNum = i
				break
			}
		}
	}
	applyLayout(currentConfig.DefaultLayout)
	w, h := ui.TerminalDimensions()
	drawScreen(w, h)
	renderMutex.Unlock()
}

func toggleFanLayout() {
	renderMutex.Lock()
	if currentConfig.DefaultLayout == LayoutFan {
		if lastActiveLayout != "" {
			currentConfig.DefaultLayout = lastActiveLayout
		} else {
			currentConfig.DefaultLayout = LayoutDefault
		}
		for i, layout := range layoutOrder {
			if layout == currentConfig.DefaultLayout {
				currentLayoutNum = i
				break
			}
		}
	} else {
		lastActiveLayout = currentConfig.DefaultLayout
		currentConfig.DefaultLayout = LayoutFan
		infoScrollOffset = 0
		for i, layout := range layoutOrder {
			if layout == LayoutFan {
				currentLayoutNum = i
				break
			}
		}
	}
	applyLayout(currentConfig.DefaultLayout)
	updateInfoUI()
	w, h := ui.TerminalDimensions()
	drawScreen(w, h)
	renderMutex.Unlock()
}

// cleanupFanControl resets fans to auto mode on application exit
// to prevent fans from being stuck in manual mode.
func cleanupFanControl() {
	if fanControl {
		_ = ResetFansToAuto()
		for k := range pendingFanTargets {
			delete(pendingFanTargets, k)
		}
	}
}

// pendingFanTargets tracks the last-written target RPM per fan ID,
// so rapid keypresses accumulate correctly between metric refreshes.
var pendingFanTargets = make(map[int]int)

const fanRPMStep = 100

func handleFanSpeedAdjust(key string) {
	renderMutex.Lock()
	defer renderMutex.Unlock()

	if len(lastCPUMetrics.Fans) == 0 {
		return
	}

	var firstErr error
	rec := func(e error) {
		if e != nil && firstErr == nil {
			firstErr = e
		}
	}
	for _, fan := range lastCPUMetrics.Fans {
		_ = SetFanForceTest(true)  // best-effort: Ftst is absent on Apple Silicon
		rec(SetFanMode(fan.ID, 1)) // forced mode

		// Use pending target if available, otherwise fall back to last known
		baseline, ok := pendingFanTargets[fan.ID]
		if !ok {
			baseline = fan.TargetRPM
		}
		if key == "+" || key == "=" {
			baseline += fanRPMStep
		} else {
			baseline -= fanRPMStep
		}
		// Clamp to fan min/max range
		if baseline < fan.MinRPM {
			baseline = fan.MinRPM
		}
		if baseline > fan.MaxRPM {
			baseline = fan.MaxRPM
		}
		pendingFanTargets[fan.ID] = baseline
		rec(SetFanTarget(fan.ID, baseline))
	}
	noteFanWrites(firstErr)
	updateInfoUI()
	w, h := ui.TerminalDimensions()
	drawScreen(w, h)
}

func handleFanAutoToggle() {
	renderMutex.Lock()
	defer renderMutex.Unlock()

	if len(lastCPUMetrics.Fans) == 0 {
		return
	}

	// Check if any fan is currently in manual mode
	anyManual := false
	for _, fan := range lastCPUMetrics.Fans {
		if fan.Mode != 0 {
			anyManual = true
			break
		}
	}

	var firstErr error
	rec := func(e error) {
		if e != nil && firstErr == nil {
			firstErr = e
		}
	}
	if anyManual {
		// Any fan is manual → set ALL to auto
		for _, fan := range lastCPUMetrics.Fans {
			rec(SetFanMode(fan.ID, 0))
		}
		_ = SetFanForceTest(false) // best-effort: Ftst is absent on Apple Silicon
		for k := range pendingFanTargets {
			delete(pendingFanTargets, k)
		}
	} else {
		// All fans are auto → set ALL to manual
		_ = SetFanForceTest(true) // best-effort: Ftst is absent on Apple Silicon
		for _, fan := range lastCPUMetrics.Fans {
			rec(SetFanMode(fan.ID, 1))
		}
	}
	noteFanWrites(firstErr)
	updateInfoUI()
	w, h := ui.TerminalDimensions()
	drawScreen(w, h)
}

func handleFanSetMin() {
	renderMutex.Lock()
	defer renderMutex.Unlock()

	var firstErr error
	rec := func(e error) {
		if e != nil && firstErr == nil {
			firstErr = e
		}
	}
	for _, fan := range lastCPUMetrics.Fans {
		_ = SetFanForceTest(true) // best-effort: Ftst is absent on Apple Silicon
		rec(SetFanMode(fan.ID, 1))
		rec(SetFanTarget(fan.ID, fan.MinRPM))
		pendingFanTargets[fan.ID] = fan.MinRPM
	}
	noteFanWrites(firstErr)
	updateInfoUI()
	w, h := ui.TerminalDimensions()
	drawScreen(w, h)
}

func handleFanSetMax() {
	renderMutex.Lock()
	defer renderMutex.Unlock()

	var firstErr error
	rec := func(e error) {
		if e != nil && firstErr == nil {
			firstErr = e
		}
	}
	for _, fan := range lastCPUMetrics.Fans {
		_ = SetFanForceTest(true) // best-effort: Ftst is absent on Apple Silicon
		rec(SetFanMode(fan.ID, 1))
		rec(SetFanTarget(fan.ID, fan.MaxRPM))
		pendingFanTargets[fan.ID] = fan.MaxRPM
	}
	noteFanWrites(firstErr)
	updateInfoUI()
	w, h := ui.TerminalDimensions()
	drawScreen(w, h)
}

func handleFanResetAuto() {
	renderMutex.Lock()
	defer renderMutex.Unlock()

	noteFanWrites(ResetFansToAuto())
	for k := range pendingFanTargets {
		delete(pendingFanTargets, k)
	}
	updateInfoUI()
	w, h := ui.TerminalDimensions()
	drawScreen(w, h)
}

func handleThemeCycle(step int) {
	renderMutex.Lock()
	w, h := ui.TerminalDimensions()
	updateLayout(w, h)
	cycleTheme(step)
	renderMutex.Unlock()
	renderMutex.Lock()
	updateProcessList()
	w, h = ui.TerminalDimensions()
	drawScreen(w, h)
	renderMutex.Unlock()
}

func handleLayoutCycle(step int) {
	renderMutex.Lock()
	cycleLayout(step)
	renderMutex.Unlock()
	saveConfig()
	renderMutex.Lock()
	w, h := ui.TerminalDimensions()
	drawScreen(w, h)
	renderMutex.Unlock()
}

// handleLayoutSwitchTo jumps directly to the given layout (toggling back to
// the previous one when pressed again), mirroring handleLayoutCycle.
func handleLayoutSwitchTo(layoutName string) {
	renderMutex.Lock()
	switchToLayout(layoutName)
	renderMutex.Unlock()
	saveConfig()
	renderMutex.Lock()
	w, h := ui.TerminalDimensions()
	drawScreen(w, h)
	renderMutex.Unlock()
}

func handleBackgroundCycle(step int) {
	renderMutex.Lock()
	cycleBackground(step)
	w, h := ui.TerminalDimensions()
	drawScreen(w, h)
	renderMutex.Unlock()
}

func toggleFreeze() {
	renderMutex.Lock()
	isFrozen = !isFrozen
	updateProcessList() // To redraw title with [FROZEN]
	w, h := ui.TerminalDimensions()
	drawScreen(w, h)
	renderMutex.Unlock()
}
