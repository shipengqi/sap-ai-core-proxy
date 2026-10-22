package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/cmd/aicore-tui/format"
	"github.com/shipengqi/sap-ai-core-proxy/cmd/aicore-tui/stats"
)

func main() {
	base := flag.String("url", "http://localhost:3001", "proxy base URL")
	interval := flag.Duration("interval", 2*time.Second, "refresh interval")
	plain := flag.Bool("plain", false, "print one snapshot and exit")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fetcher := stats.New(*base)
	for {
		snap := fetcher.Fetch(ctx)
		if !*plain {
			fmt.Print("\033[H\033[2J")
		}
		fmt.Print(render(fetcher.Base(), snap))
		if *plain || ctx.Err() != nil {
			return
		}
		t := time.NewTimer(*interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func render(base string, s stats.Snapshot) string {
	t := s.Stats.Totals.Tokens
	status := "OK"
	if s.Down() {
		status = "DOWN"
	} else if !s.OK() {
		status = "DEGRADED"
	}
	out := fmt.Sprintf("SAP AI Core Proxy  [%s]  %s\n", status, base)
	out += fmt.Sprintf("Uptime %s  |  Requests %s  Failures %s  Cache hit %s\n\n", format.Duration(s.Stats.UptimeSeconds), format.HumanCount(s.Stats.Totals.Requests), format.HumanCount(s.Stats.Totals.Failures), format.Percent(s.Stats.Totals.CacheHitRatio))
	out += fmt.Sprintf("Tokens: input %s  output %s  cache-read %s  cache-write %s\n", format.HumanCount(t.Input), format.HumanCount(t.Output), format.HumanCount(t.CacheRead), format.HumanCount(t.CacheWrite))
	out += fmt.Sprintf("Missing usage %s  Uninstrumented %s  Capture dropped %s\n\n", format.HumanCount(s.Stats.Totals.UsageMissing), format.HumanCount(s.Stats.Totals.Uninstrumented), format.HumanCount(s.Stats.Totals.UsageCaptureDropped))
	out += "By surface:\n"
	for _, b := range s.Stats.BySurface {
		out += fmt.Sprintf("  %-12s requests %-8s failures %-8s cache %s\n", format.PadRight(b.Surface, 12), format.HumanCount(b.Requests), format.HumanCount(b.Failures), format.Percent(b.CacheHitRatio))
	}
	if len(s.Errors) > 0 {
		out += "\nEndpoint errors:\n"
		for _, e := range s.Errors {
			out += "  " + e.Error() + "\n"
		}
	}
	out += "\nLast refresh " + format.Clock(s.FetchedAt) + "  (Ctrl-C to exit)\n"
	return out
}
