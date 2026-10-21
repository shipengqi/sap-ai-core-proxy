package diagnostics

import "regexp"

// Volatile patterns are the classic prefix-busters: a timestamp or a random id
// embedded in the system prompt changes on every request, so the provider sees
// a different prefix every time and can never serve it from cache.
//
// The scan only reports counts. It never records the matched text, because the
// matched text is prompt content.
var (
	isoTimestampRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)
	uuidV4Re       = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}`)
	longHexRe      = regexp.MustCompile(`\b[0-9a-f]{32,}\b`)
	plainDateRe    = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
)

// Volatile kinds, used as label values. Stable.
const (
	VolatileTimestamp = "iso_timestamp"
	VolatileUUID      = "uuid_v4"
	VolatileLongHex   = "long_hex"
	VolatileDate      = "plain_date"
)

// volatileCap bounds the matches counted per kind per request. A single long
// log file can contain thousands of timestamps; counting them all would make
// the regex the most expensive part of the request path for no extra insight.
const volatileCap = 10

// scanVolatile counts volatile patterns across the given regions.
//
// Plain dates are counted only where they are not part of an ISO timestamp,
// otherwise every timestamp would also be reported as a date and inflate that
// counter by the timestamp count.
func scanVolatile(regions ...[]byte) map[string]int {
	counts := make(map[string]int, 4)
	for _, region := range regions {
		if len(region) == 0 {
			continue
		}

		countCapped(counts, VolatileTimestamp, isoTimestampRe.FindAllIndex(region, volatileCap))
		countCapped(counts, VolatileUUID, uuidV4Re.FindAllIndex(region, volatileCap))
		countCapped(counts, VolatileLongHex, longHexRe.FindAllIndex(region, volatileCap))

		timestamps := isoTimestampRe.FindAllIndex(region, -1)
		for _, idx := range plainDateRe.FindAllIndex(region, -1) {
			if counts[VolatileDate] >= volatileCap {
				break
			}
			if insideAny(idx, timestamps) {
				continue
			}
			counts[VolatileDate]++
		}
	}
	return counts
}

// countCapped adds len(matches) to counts[kind], stopping at volatileCap.
func countCapped(counts map[string]int, kind string, matches [][]int) {
	for range matches {
		if counts[kind] >= volatileCap {
			return
		}
		counts[kind]++
	}
}

// insideAny reports whether idx falls inside any of the outer ranges.
func insideAny(idx []int, outer [][]int) bool {
	for _, o := range outer {
		if idx[0] >= o[0] && idx[1] <= o[1] {
			return true
		}
	}
	return false
}
