// Copyright (c) 2024-2026 Carsen Klock under MIT License
// arch_check.go — fail fast on non–Apple Silicon hardware.
//
// mactop's metrics pipeline depends on IOReport channel groups (AMC Stats,
// PMP, Energy Model, GPU Stats, CPU Stats, etc.) that only exist on Apple
// Silicon SoCs. On Intel Macs those channels either return no data or block
// the IOReport subscription, leaving the user staring at the "Loading…"
// screen forever. This module detects the architecture at startup so we can
// exit with a clear, actionable message instead of hanging.
package app

import (
	"fmt"
	"os"
	"runtime"
)

// isAppleSilicon reports whether the host CPU is an Apple Silicon (arm64)
// chip. We trust runtime.GOARCH first (the binary itself is arm64-only when
// built for Apple Silicon), then cross-check sysctl hw.optional.arm64 so
// universal/Rosetta builds still detect the underlying hardware correctly.
func isAppleSilicon() bool {
	if runtime.GOARCH == "arm64" {
		return true
	}
	// Rosetta or amd64 build: ask the kernel about the real hardware.
	// hw.optional.arm64 is 1 on Apple Silicon, 0 (or missing) on Intel.
	if v, err := sysctlIntByName("hw.optional.arm64"); err == nil && v == 1 {
		return true
	}
	return false
}

// requireAppleSilicon prints a friendly error and exits with status 1 when
// the host is not Apple Silicon. It is safe to call early in Run() — after
// handleLegacyFlags (so --version / --help still work) but before any
// IOReport / SMC / HID initialization that would otherwise block.
func requireAppleSilicon() {
	if isAppleSilicon() {
		return
	}
	fmt.Fprintln(os.Stderr, "mactop: this tool requires an Apple Silicon Mac (M1 or newer).")
	fmt.Fprintf(os.Stderr, "        Detected architecture: %s. Intel Macs are not supported because\n", runtime.GOARCH)
	fmt.Fprintln(os.Stderr, "        the IOReport / AMC / PMP power and bandwidth channels mactop")
	fmt.Fprintln(os.Stderr, "        depends on do not exist on x86_64 hardware.")
	os.Exit(1)
}
