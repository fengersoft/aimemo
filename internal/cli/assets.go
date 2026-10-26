package cli

import "embed"

// templateFS holds files written by the init command.
//
//go:embed templates
var templateFS embed.FS
