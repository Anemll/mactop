# mactop ANE diagnostic — M5 Max / macOS 27

**Status:** root cause identified directly on the M5 Max (Mac17,6, Apple M5 Max,
18 cores, 128 GB, macOS 27). This supersedes the earlier "renamed-channel"
hypothesis (H1) — that hypothesis is **wrong**.

---

## 1. Symptom

`mactop` reports **ANE utilization = 0%** (and ANE bandwidth 0) on M5 Max, even
under on-device ANE inference.

## 2. Root cause (confirmed)

mactop derives ANE utilization from **PMP performance-floor residency counters**
(`internal/app/ioreport.m`). On this M5 Max, **the entire PMP IOReport group
returns zero channels** to a non-root process. There is nothing to match,
rename, or aggregate — the channels are simply not present.

Evidence — `mactop --dump-debug` on the M5 Max:

```
--- IOReport Channel Groups ---
  Energy Model              [OK]  364 channels
  GPU Stats                 [OK]  140 channels
  CPU Stats                 [OK]  130 channels
  AMC Stats                 [OK]  223 channels
  PMP                       [OK]    0 channels   <-- empty
  CLPC                      [OK]    0 channels   <-- empty
  ODS                       [OK]    0 channels   <-- empty
  Performance Statistics    [OK]    0 channels   <-- empty
```

Evidence — full channel dump (`mactop --dump-ioreport`, 634 channels): the
**only** ANE-named channel anywhere is

```
Energy Model | (no subgroup) | ANE0 | mJ
```

…which is a single-value energy counter that reads **0 mJ / 0.00 W** (a dead
power counter — see §6), not a utilization signal.

Evidence — `ane_scan` (this folder), non-root:

```
GROUP PMP                    : 0 channels (group present but EMPTY ...)
GROUP CLPC                   : 0 channels (group present but EMPTY ...)
GROUP ODS                    : 0 channels (group present but EMPTY ...)
GROUP Performance Statistics : 0 channels (group present but EMPTY ...)
GROUP Energy Model           : 364 channels, 1 ANE-named  (ANE0, mJ, single-value)
=== summary: 1 ANE-named channel(s) across all scanned groups ===
```

So `aneActive` stays exactly 0% because the loop that would set it
(`isAneFloorChannel || isAneEngineStateChannel`) never sees a single PMP
channel — not because the names changed.

**Confirmed under real ANE load.** Driving the on-device Apple Foundation
Models system model — `fm respond --model system --stream 'make a game of Space
invaders in PyGame'`, which produced a full ~3.3 KB response — and sampling
during generation: PMP still **0 channels** and `Energy Model / ANE0` still
**0.00 W**. The counters do not move under genuine ANE inference, so this is not
a threshold, idle-floor, or timing issue — the telemetry is absent, period.

## 3. Why the earlier H1 (renamed/split channels) was wrong

H1 assumed the M5 Max die renames `ANE-AF-BW` / `ANE-DCS-BW` or splits the ANE
across `ANE0`/`ANE1`, so the exact-`strcmp` matcher would miss them. But the PMP
group is **empty**, so there are no candidate channels under any name. Replacing
`strcmp` with structural matching would change nothing here.

## 4. The one open question — privilege vs. platform

We could not test root autonomously (sudo needs a password). The decisive test:

```sh
cd docs/m5max_ane_diag
./run_ane_diag.sh          # runs a non-root scan, then a sudo scan
# or just:
sudo ./ane_scan 2000
```

Interpretation of the PMP line under root:

- **PMP gains `ANE-*-BW` channels under root** → PMP is *privilege-gated* on
  macOS 27. This is a problem for mactop specifically, because mactop is a
  **no-sudo** tool by design (README: "No sudo required"). The fix is then about
  graceful degradation / an alternative non-root signal, not channel matching.
- **PMP is still 0 under root** → macOS 27 / M5 Max no longer exposes the PMP
  performance-floor telemetry at all. The PMP-based ANE-% path is dead on this
  platform and needs a different data source.

Either way, structural channel matching alone does **not** fix M5 Max.

## 5a. Fix shipped — IORegistry ANE power-state fallback

Implemented in `internal/app/ioreport.m` (`samplePowerMetrics`). The Apple Neural
Engine driver (IOClass `H11ANEIn`) publishes `IOPowerManagement.CurrentPowerState`
in the IORegistry — **0 = idle, 1 = powered** — and that property is **readable
without root**. mactop now samples it ~20×/second across the measurement window
and uses the duty cycle as `aneActive` **only when no PMP ANE utilization channel
is present** (so chips that do expose PMP keep their higher-resolution
floor-residency signal untouched).

Verified on this M5 Max via `mactop --headless`:

| state | `ane_active` before | `ane_active` after |
|-------|--------------------|--------------------|
| idle  | 0                  | 0                  |
| under `fm` on-device inference | 0 | ~100 |

**Important caveat — the signal is system-wide and binary.** `CurrentPowerState`
reflects the whole ANE power domain, not just the user's process. macOS runs
several background ML services that use the ANE — `photoanalysisd`,
`mediaanalysisd`, `spotlightknowledged`, `siriknowledged` — and while any of them
is active the state stays 1. On this machine, with photo/media analysis running,
the idle baseline measured **100% over 35s** even with no user inference; when
the system is genuinely quiet it cleanly reads 0 (verified: 0/120 at first boot
sample, and a 1→0 decay within ~5s after load). So this is best understood as
"the ANE hardware is powered/in use (by anything)" rather than "this app's ANE
load." Truthful for a hardware monitor, but it can sit near 100% during
background macOS ML activity. There is no non-root way to attribute ANE use to a
specific process or to get a finer-grained load number on this platform.

Characteristics / limits:
- `MaxPowerState` is 1 (binary), so the reading is coarse: ~100% while the ANE is
  powered, 0% when idle — not a fine-grained load %.
- The power domain has a **~5s cool-down tail** after the last inference, so the
  reading lingers near 100% for a few seconds after activity stops. Acceptable
  for a live monitor and far better than the constant 0% it replaced.
- ANE **bandwidth** (`ane_read/write_bw`) and **power** (`ane_power`) remain 0:
  no non-root byte-count or energy source exists on this build (the `Energy
  Model / ANE0` counter is dead). Only utilization is recoverable.
- No finer non-root counter exists: `AppleT6050ANEHAL` and `H1xANELoadBalancer`
  IORegistry nodes carry only static device info (e.g. `NumANEs=1`).

Helpers added: `copyAneService()` (matches `H11ANEIn`) and `readAnePowerState()`.

## 5. Other fix paths (original analysis; superseded by 5a for non-root)

1. **Detect the empty-PMP case and stop pretending.** In the ANE path, if the
   PMP group yields 0 channels, mark ANE utilization as *unavailable* (e.g. show
   `ANE: n/a` instead of a misleading `0%`). Cheap, honest, ships immediately.
2. **If root unlocks PMP:** keep the no-sudo default but, when running as root,
   use PMP; document that ANE-% requires root on macOS 27. (Still pair with the
   structural-matching cleanup below so future dies that *do* expose PMP under
   different names are covered.)
3. **Alternative non-root ANE signal.** Investigate whether the `Energy Model /
   ANE0` channel ever produces non-zero energy on a build where the counter is
   alive, or whether a CLPC/ODS/Performance-Statistics channel (also empty here)
   carries ANE residency on other configs.
4. **Structural matching (the originally proposed cleanup — still worth doing,
   just not sufficient).** Replace exact `strcmp` with: any `ANE*`-prefixed
   channel in a `*Floor` subgroup (util) or `*BW` subgroup (bandwidth),
   aggregated across engine instances, with the idle floor detected generically
   (lowest state) rather than the hard-coded `VMIN`/`F1`/`0%` list. Mirror sites
   in `internal/app/ioreport.m`:
   - `~1720` — `isAneFloorChannel` / `isAneEngineStateChannel` in the
     `--dump-debug` PMP ANE scan.
   - `~2906` — the same classification in the live metrics loop (drives
     `metrics.aneActive`).
   - `~2951` — the `AF BW` bandwidth matcher.
   The hard-coded idle-state list is mirrored at `~1742`, `~2926`.

## 6. ANE power = 0.00 W is a *separate* issue

`Energy Model / ANE0` reports `0.0000 W` (raw 0 mJ). On macOS 27 / M5, every
energy-unit channel except the nJ `GPU Energy` reads 0 — ANE/CPU/DRAM power is
not exposed to user space on this build. mactop already reports the raw 0 rather
than fabricating a value (`--dump-debug` documents this). This is an OS-level
dead-counter situation, independent of the PMP-empty utilization bug above.

## 7. Files in this folder

- `M5MAX_ANE_DIAGNOSTIC.md` — this write-up.
- `ane_scan.m` — standalone scanner: probes PMP/CLPC/ODS/Performance-Statistics/
  Energy-Model/CPU/GPU/AMC for ANE channels, dumps per-state residencies, flags
  which ones mactop's current matcher would accept, and prints both the
  hard-coded-idle and generic-floor active %%.
- `run_ane_diag.sh` — builds the scanner, runs a non-root scan then a root scan,
  writes `ane_diag_<host>.txt`.
- `ane_scan_M5Max_nonroot.txt` — captured non-root output from this M5 Max.

Build manually:

```sh
clang -O2 -fobjc-arc -framework Foundation -framework IOKit \
      -framework CoreFoundation -lIOReport ane_scan.m -o ane_scan
```
