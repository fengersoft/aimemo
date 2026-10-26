// aimemo is a memory store for AI agents.
package main

import (
	"os"

	"aimemo/internal/cli"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	cli.SetVersion(version, commit, date)
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
