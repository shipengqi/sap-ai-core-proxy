package compress

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Adaptive budget bounds. The budget caps how many *scored* lines survive
// besides the must-keep set; it never caps errors.
const (
	// budgetRatio: signal-bearing lines in a real tool dump are roughly 5-15%
	// of it (a few failures inside hundreds of progress lines), so 10% leaves
	// headroom while still cutting a 1000-line dump to ~100 lines.
	budgetRatio = 0.10
	// budgetMin stops the budget from gutting a small log where the scored
	// lines are nearly all of it.
	budgetMin = 20
	// budgetMax bounds absolute cost: a 20k-line dump must not produce a
	// 2k-line payload, however high the ratio comes out.
	budgetMax = 200
	// contextRadius is the number of lines kept either side of a kept line, so
	// an error is never shown without its neighbour lines.
	contextRadius = 2
	// maxStackLines bounds one stack region. Longer regions keep their head and
	// tail, which is where the interesting frames are.
	maxStackLines = 60
)

// Line scores. Only the ordering matters for the budget pass: every line with a
// score above zero is a candidate, and the highest score is filled first.
const (
	scoreError   = 100
	scoreFail    = 90
	scoreSummary = 90 // 60 base + 30 summary bonus
	scoreWarn    = 40
	scoreStack   = 30
	// scoreStackBonus lifts a frame that belongs to a traceback that ends in an
	// exception above an unrelated indented line.
	scoreStackBonus = 40
	scoreInfo       = 5
)

// line is one line plus its terminator, so the text can be rebuilt byte for
// byte. Keeping the terminator is what makes \r\n input survive.
type line struct {
	text string
	term string
}

// splitLines splits text into lines that carry their own terminator. The
// concatenation of text+term for every element is the input, exactly.
func splitLines(text string) []line {
	var lines []line
	for rest := text; rest != ""; {
		i := strings.IndexByte(rest, '\n')
		if i < 0 {
			lines = append(lines, line{text: rest})
			break
		}
		body := rest[:i]
		term := "\n"
		if len(body) > 0 && body[len(body)-1] == '\r' {
			body = body[:len(body)-1]
			term = "\r\n"
		}
		lines = append(lines, line{text: body, term: term})
		rest = rest[i+1:]
	}
	return lines
}

// joinLines is the inverse of splitLines.
func joinLines(lines []line) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.text)
		b.WriteString(l.term)
	}
	return b.String()
}

// lineKind is what a line is, which decides whether it survives.
type lineKind int

const (
	kindOther lineKind = iota
	kindInfo
	kindStack
	kindWarn
	kindSummary
	kindFail
	kindError
)

// region is a half-open-by-inclusion span of lines [start, end] that forms one
// stack trace.
type region struct {
	start int
	end   int
}

// CompressLog compresses log-shaped text: it keeps errors, failures, stacks
// attached to an exception, warnings and summary lines, drops the rest, and
// marks every dropped span with a counter line.
//
// It is deterministic: the same input always yields the same output. Selection
// never iterates a map for output purposes, and ties break on line number.
func CompressLog(text string) string {
	lines := splitLines(text)
	n := len(lines)
	if n < 2 {
		return text
	}

	regions := findStackRegions(lines)
	kinds := classifyLines(lines, regions)
	regionOf := regionIndex(lines, regions)
	regionErr := make([]bool, len(regions))
	for i, r := range regions {
		regionErr[i] = regionHasError(lines, kinds, r)
	}

	keep := make([]bool, n)
	kept := 0
	mark := func(i int) {
		if i < 0 || i >= n || keep[i] {
			return
		}
		keep[i] = true
		kept++
	}

	// 1. The must-keep set: errors, failures and summaries are never dropped,
	// whatever the budget says. Losing an error line is the one failure mode
	// this feature must not have.
	for i := 0; i < n; i++ {
		switch kinds[i] {
		case kindError, kindFail, kindSummary:
			mark(i)
		}
	}

	// 2. Whole stack regions that belong to an exception, plus the exception
	// line that terminates a Python traceback.
	for i, r := range regions {
		if !regionErr[i] {
			continue
		}
		if r.end-r.start+1 > maxStackLines {
			for j := r.start; j < r.start+maxStackLines/2; j++ {
				mark(j)
			}
			for j := r.end - maxStackLines/2 + 1; j <= r.end; j++ {
				mark(j)
			}
		} else {
			for j := r.start; j <= r.end; j++ {
				mark(j)
			}
		}
		if r.end+1 < n && isExceptionLine(strings.TrimSpace(lines[r.end+1].text)) {
			mark(r.end + 1)
		}
	}

	// 3. Budgeted fill: scored lines, highest first, ties on line number.
	budget := adaptiveBudget(n)
	type candidate struct {
		idx   int
		score int
	}
	var candidates []candidate
	seenWarn := make(map[string]bool)
	for i := 0; i < n; i++ {
		if keep[i] {
			continue
		}
		score := scoreOf(kinds[i], regionOf[i] >= 0 && regionErr[regionOf[i]])
		if score <= 0 {
			continue
		}
		if kinds[i] == kindWarn {
			// Only exactly-identical warnings collapse (after the timestamp is
			// stripped). Normalising numbers, paths or hex would merge distinct
			// failures at different addresses into one line.
			key := normalizeWarn(strings.TrimSpace(lines[i].text))
			if seenWarn[key] {
				continue
			}
			seenWarn[key] = true
		}
		candidates = append(candidates, candidate{idx: i, score: score})
	}
	sort.Slice(candidates, func(a, b int) bool {
		if candidates[a].score != candidates[b].score {
			return candidates[a].score > candidates[b].score
		}
		return candidates[a].idx < candidates[b].idx
	})
	for _, c := range candidates {
		if kept >= budget {
			break
		}
		mark(c.idx)
	}

	// 4. Context around everything that survived.
	expandContext(keep, contextRadius)

	return assemble(lines, keep)
}

// adaptiveBudget returns how many scored lines may be kept besides the
// must-keep set.
func adaptiveBudget(total int) int {
	b := int(float64(total) * budgetRatio)
	if b < budgetMin {
		b = budgetMin
	}
	if b > budgetMax {
		b = budgetMax
	}
	return b
}

// scoreOf ranks one line for the budget pass.
func scoreOf(k lineKind, inErrorRegion bool) int {
	switch k {
	case kindError:
		return scoreError
	case kindFail:
		return scoreFail
	case kindSummary:
		return scoreSummary
	case kindWarn:
		return scoreWarn
	case kindStack:
		if inErrorRegion {
			return scoreStack + scoreStackBonus
		}
		return scoreStack
	case kindInfo:
		return scoreInfo
	}
	return 0
}

// classifyLines assigns a kind to every line.
func classifyLines(lines []line, regions []region) []lineKind {
	kinds := make([]lineKind, len(lines))
	regionOf := regionIndex(lines, regions)
	for i := range lines {
		t := strings.TrimSpace(lines[i].text)
		switch {
		case t == "":
			kinds[i] = kindOther
		case isErrorLine(t):
			kinds[i] = kindError
		case isRunLine(t):
			// `=== RUN   TestX` is noise once the result line is kept.
			kinds[i] = kindInfo
		case isFailLine(t):
			kinds[i] = kindFail
		case isSummaryLine(t):
			kinds[i] = kindSummary
		case isWarnLine(t):
			kinds[i] = kindWarn
		case isInfoLine(t):
			kinds[i] = kindInfo
		case regionOf[i] >= 0:
			kinds[i] = kindStack
		default:
			kinds[i] = kindOther
		}
	}
	return kinds
}

// isRunLine reports whether the line is a go test runner marker.
func isRunLine(t string) bool {
	return strings.HasPrefix(t, "=== RUN") ||
		strings.HasPrefix(t, "=== PAUSE") ||
		strings.HasPrefix(t, "=== CONT")
}

// isExceptionLine reports whether the line is an exception header, e.g. the
// line that terminates a Python traceback.
func isExceptionLine(t string) bool {
	return reException.MatchString(t)
}

// findStackRegions groups consecutive stack frames into regions.
//
// Two shapes are recognised, because they end differently:
//
//   - Python: a `Traceback (most recent call last):` header followed by indented
//     frames. The region ends at the first non-empty, non-indented line, which
//     is the exception line itself and is kept separately.
//   - JS/Java/Go: consecutive `at ` / `file:line` frames. The region ends at the
//     first line that is not a frame.
//
// Blank lines never terminate a region: chained exceptions put blank lines in
// the middle of a traceback, and stopping at the first one would keep only the
// first exception.
func findStackRegions(lines []line) []region {
	var regions []region
	n := len(lines)
	for i := 0; i < n; {
		t := strings.TrimSpace(lines[i].text)
		if t == "" {
			i++
			continue
		}
		python := isTracebackHeader(t)
		if !python && !isFrameLine(t) {
			i++
			continue
		}
		end := i
		for j := i + 1; j < n; j++ {
			tj := strings.TrimSpace(lines[j].text)
			if tj == "" {
				continue
			}
			if python {
				// Any indented non-empty line is part of the traceback: frames
				// (`File "..."`), the echoed source line and pytest's `E` gutter.
				if isIndented(lines[j].text) {
					end = j
					continue
				}
				break
			}
			if isFrameLine(tj) {
				end = j
				continue
			}
			break
		}
		regions = append(regions, region{start: i, end: end})
		i = end + 1
	}
	return regions
}

// regionIndex maps every line to the region it belongs to, or -1.
func regionIndex(lines []line, regions []region) []int {
	out := make([]int, len(lines))
	for i := range out {
		out[i] = -1
	}
	for id, r := range regions {
		for j := r.start; j <= r.end && j < len(out); j++ {
			out[j] = id
		}
	}
	return out
}

// regionHasError reports whether a stack region belongs to an exception: either
// it contains an error line, or the line right after it is the exception header
// (which is how Python arranges it).
func regionHasError(lines []line, kinds []lineKind, r region) bool {
	for i := r.start; i <= r.end && i < len(kinds); i++ {
		if kinds[i] == kindError || kinds[i] == kindFail {
			return true
		}
	}
	if r.end+1 < len(lines) && isExceptionLine(strings.TrimSpace(lines[r.end+1].text)) {
		return true
	}
	return false
}

// isIndented reports whether a line starts with whitespace.
func isIndented(raw string) bool {
	return raw != "" && (raw[0] == ' ' || raw[0] == '\t')
}

// reTimestamp matches a leading timestamp, which is the only part of a warning
// line allowed to differ between two otherwise identical warnings.
var reTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(?:[T ][0-9:.]{4,15})?\s*`)

// normalizeWarn collapses a warning line for deduplication. Numbers, paths and
// hex survive on purpose: merging them would merge distinct failures.
func normalizeWarn(s string) string {
	s = reTimestamp.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(s), " ")
}

// expandContext keeps radius lines either side of every kept line, so an error
// is never printed without its neighbours.
func expandContext(keep []bool, radius int) {
	mark := make([]bool, len(keep))
	for i, k := range keep {
		if !k {
			continue
		}
		for j := i - radius; j <= i+radius; j++ {
			if j >= 0 && j < len(keep) {
				mark[j] = true
			}
		}
	}
	for i := range keep {
		if mark[i] {
			keep[i] = true
		}
	}
}

// assemble rebuilds the text: kept lines verbatim, every run of dropped lines
// replaced by one counter line.
func assemble(lines []line, keep []bool) string {
	var b strings.Builder
	runStart, runLen, runBytes := 0, 0, 0
	lastTerm := ""

	flushRun := func() {
		if runLen == 0 {
			return
		}
		marker := "... " + strconv.Itoa(runLen) + " lines omitted ..."
		if runBytes > len(marker)+len(lastTerm) {
			// Only worth it when the marker is actually cheaper. A dropped run
			// of short lines is cheaper to keep, and keeping it loses nothing.
			b.WriteString(marker)
			b.WriteString(lastTerm)
		} else {
			for i := runStart; i < runStart+runLen && i < len(lines); i++ {
				b.WriteString(lines[i].text)
				b.WriteString(lines[i].term)
			}
		}
		runStart, runLen, runBytes, lastTerm = 0, 0, 0, ""
	}

	for i := range lines {
		if keep[i] {
			flushRun()
			b.WriteString(lines[i].text)
			b.WriteString(lines[i].term)
			continue
		}
		if runLen == 0 {
			runStart = i
		}
		runLen++
		runBytes += len(lines[i].text) + len(lines[i].term)
		lastTerm = lines[i].term
	}
	flushRun()
	return b.String()
}
