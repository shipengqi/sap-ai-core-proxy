package main

import (
	"os"

	"github.com/shipengqi/sap-ai-core-proxy/internal/cli"
)

func main() {
	if code := cli.Execute(cli.NewRootCommand(), os.Args[1:], os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}
