#!/usr/bin/env bash
# run_ane_diag.sh — one-command ANE telemetry capture for mactop.
#
# Builds ./ane_scan, captures the IOReport ANE channel map at non-root, then
# (the decisive test) re-captures as root to see whether the PMP performance-
# floor channels mactop's ANE %% depends on are merely privilege-gated or truly
# absent. Writes ane_diag_<host>.txt.
#
#   cd docs/m5max_ane_diag && ./run_ane_diag.sh
#   # then send back ane_diag_<host>.txt
#
# The root pass will prompt for your password (sudo). That comparison is the
# whole point on M5 Max / macOS 27, where PMP returns 0 channels for a non-root
# process — see M5MAX_ANE_DIAGNOSTIC.md.

set -u
cd "$(dirname "$0")"

HOST="$(hostname -s)"
OUT="ane_diag_${HOST}.txt"
WINDOW_MS=2000

log() { printf '%s\n' "$*" | tee -a "$OUT"; }

: > "$OUT"
log "=== mactop ANE diagnostic capture ==="
log "host:        $HOST"
log "date:        $(date)"
log "sw_vers:     $(sw_vers -productName 2>/dev/null) $(sw_vers -productVersion 2>/dev/null) ($(sw_vers -buildVersion 2>/dev/null))"
log "machine:     $(sysctl -n machdep.cpu.brand_string 2>/dev/null)"
log "model:       $(sysctl -n hw.model 2>/dev/null)"
log ""

# 1. Build the scanner if needed.
if [ ! -x ./ane_scan ] || [ ane_scan.m -nt ane_scan ]; then
  log "[build] compiling ane_scan.m ..."
  if ! clang -O2 -fobjc-arc -framework Foundation -framework IOKit \
        -framework CoreFoundation -lIOReport ane_scan.m -o ane_scan 2> >(tee -a "$OUT" >&2); then
    log "[build] FAILED — see errors above"
    exit 1
  fi
  log "[build] ok"
fi
log ""

# 2. Non-root scan.
log "########## SCAN 1: NON-ROOT (${WINDOW_MS}ms) ##########"
./ane_scan "$WINDOW_MS" | tee -a "$OUT"
log ""

# 3. Root scan — the decisive privilege test.
log "########## SCAN 2: ROOT (${WINDOW_MS}ms) ##########"
log "(sudo will prompt for your password; this tests whether PMP is privilege-gated)"
if sudo ./ane_scan "$WINDOW_MS" | tee -a "$OUT"; then
  :
else
  log "(root scan skipped or sudo unavailable)"
fi
log ""

log "=== done ==="
log "Output written to: $(pwd)/$OUT"
log "Compare the PMP line between the two scans:"
log "  * If PMP gains ANE-*-BW channels under root  -> privilege-gated (mactop is no-sudo)."
log "  * If PMP is still 0 under root               -> platform/OS change; PMP retired here."
