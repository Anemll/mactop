package app

/*
#cgo LDFLAGS: -framework CoreFoundation -framework IOKit
#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/ps/IOPowerSources.h>
#include <IOKit/ps/IOPSKeys.h>
#include <stdlib.h>
#include <string.h>

// Returns 1 if a battery power source is present, fills out values via pointers.
// percent = -1 when not available. charging = 1 if charging, 0 otherwise.
// state buffer should be at least 32 bytes.
static int mactop_get_battery(int *percent, int *charging, char *state, int state_len) {
    *percent = -1;
    *charging = 0;
    if (state && state_len > 0) state[0] = '\0';

    CFTypeRef info = IOPSCopyPowerSourcesInfo();
    if (!info) return 0;
    CFArrayRef list = IOPSCopyPowerSourcesList(info);
    if (!list) {
        CFRelease(info);
        return 0;
    }

    int found = 0;
    CFIndex count = CFArrayGetCount(list);
    for (CFIndex i = 0; i < count; i++) {
        CFDictionaryRef ps = IOPSGetPowerSourceDescription(info, CFArrayGetValueAtIndex(list, i));
        if (!ps) continue;

        CFStringRef type = CFDictionaryGetValue(ps, CFSTR(kIOPSTypeKey));
        if (!type || CFStringCompare(type, CFSTR(kIOPSInternalBatteryType), 0) != kCFCompareEqualTo) {
            continue;
        }

        CFNumberRef cap = CFDictionaryGetValue(ps, CFSTR(kIOPSCurrentCapacityKey));
        CFNumberRef max = CFDictionaryGetValue(ps, CFSTR(kIOPSMaxCapacityKey));
        if (cap && max) {
            int c = 0, m = 0;
            CFNumberGetValue(cap, kCFNumberIntType, &c);
            CFNumberGetValue(max, kCFNumberIntType, &m);
            if (m > 0) *percent = (c * 100) / m;
        }

        CFBooleanRef isCharging = CFDictionaryGetValue(ps, CFSTR(kIOPSIsChargingKey));
        if (isCharging && CFBooleanGetValue(isCharging)) *charging = 1;

        CFStringRef st = CFDictionaryGetValue(ps, CFSTR(kIOPSPowerSourceStateKey));
        if (st && state && state_len > 0) {
            CFStringGetCString(st, state, state_len, kCFStringEncodingUTF8);
        }

        found = 1;
        break;
    }

    CFRelease(list);
    CFRelease(info);
    return found;
}
*/
import "C"

import (
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/metaspartan/mactop/v2/internal/i18n"
)

// BatteryInfo describes the current internal battery state.
type BatteryInfo struct {
	Present   bool   `json:"present" yaml:"present" xml:"Present" toon:"present"`
	Percent   int    `json:"percent" yaml:"percent" xml:"Percent" toon:"percent"`
	Charging  bool   `json:"charging" yaml:"charging" xml:"Charging" toon:"charging"`
	OnACPower bool   `json:"on_ac_power" yaml:"on_ac_power" xml:"OnACPower" toon:"on_ac_power"`
	State     string `json:"state" yaml:"state" xml:"State" toon:"state"`
}

var (
	hasBatteryCached  bool
	hasBatteryOnce    sync.Once
	hasBatteryPresent bool
)

// GetBatteryInfo returns the current battery state, or Present=false if no battery.
func GetBatteryInfo() BatteryInfo {
	var percent C.int
	var charging C.int
	var stateBuf [32]C.char
	res := C.mactop_get_battery(&percent, &charging, &stateBuf[0], C.int(len(stateBuf)))
	if res == 0 {
		return BatteryInfo{}
	}
	state := C.GoString(&stateBuf[0])
	return BatteryInfo{
		Present:   true,
		Percent:   int(percent),
		Charging:  charging == 1,
		OnACPower: strings.EqualFold(state, "AC Power"),
		State:     state,
	}
}

// HasBattery reports whether the host has an internal battery (MacBook/laptop).
// Cached after first call.
func HasBattery() bool {
	hasBatteryOnce.Do(func() {
		hasBatteryPresent = GetBatteryInfo().Present
		hasBatteryCached = true
	})
	return hasBatteryPresent
}

// batteryStateLabel returns the localized state string for a battery.
func batteryStateLabel(bat BatteryInfo) string {
	switch {
	case bat.Charging:
		return i18n.T("Info_BatteryCharging")
	case bat.OnACPower:
		return i18n.T("Info_BatteryAC")
	default:
		return i18n.T("Info_BatteryDischarging")
	}
}

// formatBatteryLine returns "Battery: 87% (charging)" or empty string if no battery.
func formatBatteryLine() string {
	bat := GetBatteryInfo()
	if !bat.Present {
		return ""
	}
	return fmt.Sprintf("%s: %d%% (%s)", i18n.T("Info_Battery"), bat.Percent, batteryStateLabel(bat))
}

// avoid "imported and not used" if a future build prunes battery usage
var _ = unsafe.Pointer(nil)
