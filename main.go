// aimemo is a memory store for AI agents.
package main

import (
	"os"

	"aimemo/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
