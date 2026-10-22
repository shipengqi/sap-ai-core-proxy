package compress

import (
	"strings"
	"sync"
)

// Process-wide settings.
//
// They are process-wide because the proxy builds its handlers once at startup
// and every surface would otherwise have to thread a flag through: the switch is
// global by nature ("turn compression off everywhere, now"), so one guarded
// variable is the honest representation. Reads take an RLock, so toggling at
// runtime cannot race with a request.
var (
	settingsMu sync.RWMutex
	enabled    bool
	skipped    = map[string]bool{}
)

// Configure sets the kill switch and the strategies the escape valve turns off.
// It is called once from main; tests call it to exercise the path.
func Configure(on bool, skipStrategies []string) {
	settingsMu.Lock()
	defer settingsMu.Unlock()

	enabled = on
	skipped = make(map[string]bool, len(skipStrategies))
	for _, s := range skipStrategies {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" {
			skipped[s] = true
		}
	}
}

// Enabled reports whether request compression runs at all. When it is false
// the whole path is skipped: no parsing, no counters, no log line.
func Enabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return enabled
}

// Skip reports whether one strategy is turned off by the escape valve, e.g.
// COMPRESS_LOGS_SKIP=log.
func Skip(strategy string) bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return skipped[strings.ToLower(strings.TrimSpace(strategy))]
}
