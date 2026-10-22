// Package version reports which build of the proxy is running.
//
// The values are placeholders under `go run` and are replaced at link time by
// the Makefile and goreleaser. They stay unexported on purpose: the build
// identity is a property of the binary, so nothing in the process should be
// able to change it at runtime.
package version

import (
	"fmt"
	"runtime"
)

// Product is the name shown in human-readable version output. The binary is
// installed under more than one name (aicoreproxy, sap-ai-core-proxy), so the
// product name is fixed here instead of being derived from os.Args[0].
const Product = "sap-ai-core-proxy"

// Injected with -X at build time; see the LDFLAGS in Makefile and the ldflags
// in .goreleaser.yaml. A plain `go run .` keeps these defaults.
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

// Info is the body of GET /version and the shape both dashboards decode.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// Get returns the build identity of this binary.
func Get() Info {
	return Info{
		Version:   version,
		Commit:    commit,
		BuildDate: buildDate,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// String renders the single-line form printed by -version. It is deliberately
// one line so it can be captured with $(...) in scripts and compared as a
// whole.
func String() string {
	return fmt.Sprintf("%s %s (commit %s, built %s, %s %s/%s)",
		Product, version, commit, buildDate, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
