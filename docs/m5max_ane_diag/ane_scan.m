// ane_scan.m — standalone IOReport ANE channel scanner for mactop ANE detection.
//
// Probes every relevant IOReport group for ANE telemetry and dumps each
// matching channel's raw per-state residency deltas over a sampling window,
// the same way internal/app/ioreport.m reads them. Use this to discover what a
// given chip / macOS build actually exposes for the ANE — and, crucially,
// whether the PMP performance-floor channels mactop's ANE %% depends on are
// present at all.
//
// No sudo required (but run it BOTH with and without sudo to see whether PMP
// is privilege-gated — see docs).  Build & run:
//   clang -O2 -fobjc-arc -framework Foundation -framework IOKit \
//         -framework CoreFoundation -lIOReport ane_scan.m -o ane_scan
//   ./ane_scan            # 1000 ms window
//   ./ane_scan 2000       # 2000 ms window
//   sudo ./ane_scan 2000  # does root unlock PMP?
//
// Key things the output answers:
//   * Does the PMP group return any channels? (M5 Max / macOS 27 non-root: 0.)
//   * What SUBGROUP / NAME does each ANE channel use? mactop hard-codes
//     NAME == "ANE-AF-BW"/"ANE-DCS-BW" (util) and SUBGROUP == "AF BW" (bw).
//   * Per-state residencies + the idle floor (VMIN/F1 on M5 base).

#import <Foundation/Foundation.h>
#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#include <unistd.h>

typedef struct IOReportSubscriptionRef *IOReportSubscriptionRef;
extern CFDictionaryRef IOReportCopyChannelsInGroup(CFStringRef, CFStringRef, uint64_t, uint64_t, uint64_t);
extern IOReportSubscriptionRef IOReportCreateSubscription(void *, CFMutableDictionaryRef, CFMutableDictionaryRef *, uint64_t, CFTypeRef);
extern CFDictionaryRef IOReportCreateSamples(IOReportSubscriptionRef, CFMutableDictionaryRef, CFTypeRef);
extern CFDictionaryRef IOReportCreateSamplesDelta(CFDictionaryRef, CFDictionaryRef, CFTypeRef);
extern CFStringRef IOReportChannelGetGroup(CFDictionaryRef);
extern CFStringRef IOReportChannelGetSubGroup(CFDictionaryRef);
extern CFStringRef IOReportChannelGetChannelName(CFDictionaryRef);
extern CFStringRef IOReportChannelGetUnitLabel(CFDictionaryRef);
extern int32_t IOReportStateGetCount(CFDictionaryRef);
extern CFStringRef IOReportStateGetNameForIndex(CFDictionaryRef, int32_t);
extern int64_t IOReportStateGetResidency(CFDictionaryRef, int32_t);

static const char *kGroups[] = {
  "PMP", "CLPC", "ODS", "Performance Statistics",
  "Energy Model", "CPU Stats", "GPU Stats", "AMC Stats",
};
static const int kGroupCount = sizeof(kGroups) / sizeof(kGroups[0]);

static void cfstr(CFStringRef s, char *buf, size_t n) {
  buf[0] = 0;
  if (s) CFStringGetCString(s, buf, n, kCFStringEncodingUTF8);
}

static bool isHardCodedIdleState(const char *sn) {
  return strcmp(sn, "OFF") == 0 || strcmp(sn, "IDLE") == 0 ||
         strcmp(sn, "DOWN") == 0 || strcmp(sn, "SLEEP") == 0 ||
         strcmp(sn, "VMIN") == 0 || strcmp(sn, "F1") == 0 ||
         strcmp(sn, "0%") == 0;
}

// Returns number of ANE channels printed for this group.
static int scanGroup(const char *group, long durationMs) {
  CFStringRef gref = CFStringCreateWithCString(NULL, group, kCFStringEncodingUTF8);
  CFDictionaryRef chans = IOReportCopyChannelsInGroup(gref, NULL, 0, 0, 0);
  if (!chans) {
    printf("GROUP %-22s : copy failed (NULL)\n", group);
    CFRelease(gref);
    return 0;
  }
  // The real channel count is the length of the IOReportChannels array, NOT
  // CFDictionaryGetCount (which counts the one wrapper key). mactop reports
  // this same number.
  CFArrayRef chArr = CFDictionaryGetValue(chans, CFSTR("IOReportChannels"));
  CFIndex declared = chArr ? CFArrayGetCount(chArr) : 0;
  CFIndex total = CFDictionaryGetCount(chans);
  CFMutableDictionaryRef mut =
      CFDictionaryCreateMutableCopy(kCFAllocatorDefault, total, chans);
  CFRelease(chans);

  if (declared == 0) {
    printf("GROUP %-22s : 0 channels (group present but EMPTY for this process — "
           "nothing to subscribe to)\n", group);
    CFRelease(mut);
    CFRelease(gref);
    return 0;
  }

  CFMutableDictionaryRef subSys = NULL;
  IOReportSubscriptionRef sub = IOReportCreateSubscription(NULL, mut, &subSys, 0, NULL);
  if (!sub) {
    printf("GROUP %-22s : %ld channels declared, but standalone subscription FAILED "
           "(some groups only subscribe when merged with others)\n", group, (long)declared);
    CFRelease(mut);
    CFRelease(gref);
    return 0;
  }

  CFDictionaryRef s1 = IOReportCreateSamples(sub, mut, NULL);
  usleep((useconds_t)durationMs * 1000);
  CFDictionaryRef s2 = IOReportCreateSamples(sub, mut, NULL);
  CFDictionaryRef delta = (s1 && s2) ? IOReportCreateSamplesDelta(s1, s2, NULL) : NULL;
  CFArrayRef arr = delta ? CFDictionaryGetValue(delta, CFSTR("IOReportChannels")) : NULL;
  CFIndex cnt = arr ? CFArrayGetCount(arr) : 0;

  // Count ANE channels first so the header is informative.
  int aneCount = 0;
  for (CFIndex i = 0; i < cnt; i++) {
    CFDictionaryRef ch = (CFDictionaryRef)CFArrayGetValueAtIndex(arr, i);
    if (!ch) continue;
    char name[256];
    cfstr(IOReportChannelGetChannelName(ch), name, sizeof(name));
    if (strstr(name, "ANE") || strstr(name, "ane")) aneCount++;
  }
  printf("GROUP %-22s : %ld channels, %d ANE-named\n", group, (long)cnt, aneCount);

  for (CFIndex i = 0; i < cnt; i++) {
    CFDictionaryRef ch = (CFDictionaryRef)CFArrayGetValueAtIndex(arr, i);
    if (!ch) continue;
    char name[256], subg[64], unit[32];
    cfstr(IOReportChannelGetChannelName(ch), name, sizeof(name));
    if (strstr(name, "ANE") == NULL && strstr(name, "ane") == NULL) continue;
    cfstr(IOReportChannelGetSubGroup(ch), subg, sizeof(subg));
    cfstr(IOReportChannelGetUnitLabel(ch), unit, sizeof(unit));
    int32_t sc = IOReportStateGetCount(ch);

    bool matchUtil =
        (strcmp(name, "ANE-AF-BW") == 0 || strcmp(name, "ANE-DCS-BW") == 0) &&
        strstr(subg, "Floor") != NULL;
    bool matchEngine = (strcmp(name, "ANE0") == 0) &&
        (strstr(subg, "Floor") || strstr(subg, "Fast-Die") || strstr(subg, "CE") ||
         strstr(subg, "SOC") || strstr(subg, "Util"));
    bool matchBW = strcmp(subg, "AF BW") == 0 && strstr(name, "RD+WR") == NULL &&
        (strstr(name, "RD") || strstr(name, "WR"));

    printf("  CHANNEL subgroup=%-14s name=%-14s unit=%-5s states=%d  [mactop: util=%s engine=%s bw=%s]\n",
           subg[0] ? subg : "(none)", name, unit[0] ? unit : "(none)", sc,
           matchUtil ? "Y" : "-", matchEngine ? "Y" : "-", matchBW ? "Y" : "-");

    if (sc <= 1) { printf("    (single-value channel, not multi-state)\n"); continue; }

    int64_t tot = 0, actHard = 0, lowestRes = 0;
    for (int32_t s = 0; s < sc; s++) tot += IOReportStateGetResidency(ch, s);
    if (tot == 0) { printf("    TOTAL residency 0 (idle/dead this window)\n"); continue; }
    for (int32_t s = 0; s < sc; s++) {
      int64_t r = IOReportStateGetResidency(ch, s);
      char sn[64]; cfstr(IOReportStateGetNameForIndex(ch, s), sn, sizeof(sn));
      if (s == 0) lowestRes = r;
      if (!isHardCodedIdleState(sn[0] ? sn : "?")) actHard += r;
      printf("    [%d] %-10s %14lld (%.2f%%)%s\n", s, sn[0] ? sn : "?", (long long)r,
             (double)r / (double)tot * 100.0,
             isHardCodedIdleState(sn[0] ? sn : "?") ? "  <idle>" : "");
    }
    printf("    active(hard-coded idle list)=%.2f%%   active(generic: minus lowest)=%.2f%%\n",
           (double)actHard / (double)tot * 100.0,
           (double)(tot - lowestRes) / (double)tot * 100.0);
  }

  CFRelease(mut);
  CFRelease(gref);
  return aneCount;
}

int main(int argc, char **argv) {
  @autoreleasepool {
    long durationMs = (argc > 1) ? atol(argv[1]) : 1000;
    if (durationMs <= 0) durationMs = 1000;

    char host[256] = {0};
    gethostname(host, sizeof(host));
    printf("=== ane_scan: IOReport ANE channel scan ===\n");
    printf("host: %s   window: %ld ms   euid: %d %s\n\n",
           host, durationMs, geteuid(), geteuid() == 0 ? "(root)" : "(non-root)");

    int totalAne = 0;
    for (int g = 0; g < kGroupCount; g++) {
      totalAne += scanGroup(kGroups[g], durationMs);
    }

    printf("\n=== summary: %d ANE-named channel(s) across all scanned groups ===\n", totalAne);
    if (totalAne == 0) {
      printf("No ANE channels reachable. mactop's ANE %% cannot be computed on this\n");
      printf("build/process. If PMP shows '0 channels', re-run with sudo to test\n");
      printf("whether the performance-floor telemetry is privilege-gated.\n");
    }
    return 0;
  }
}
