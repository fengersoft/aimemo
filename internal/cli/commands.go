package cli

import "io"

// The command stubs are replaced by the real implementations in Task 8.
func runAdd(g globalOpts, args []string, stdout, stderr io.Writer) int        { return exitUsage }
func runList(g globalOpts, args []string, stdout, stderr io.Writer) int       { return exitUsage }
func runSearch(g globalOpts, args []string, stdout, stderr io.Writer) int     { return exitUsage }
func runGet(g globalOpts, args []string, stdout, stderr io.Writer) int        { return exitUsage }
func runUpdate(g globalOpts, args []string, stdout, stderr io.Writer) int     { return exitUsage }
func runDelete(g globalOpts, args []string, stdout, stderr io.Writer) int     { return exitUsage }
func runCategories(g globalOpts, args []string, stdout, stderr io.Writer) int { return exitUsage }
func runStats(g globalOpts, args []string, stdout, stderr io.Writer) int      { return exitUsage }
func runUsage(g globalOpts, args []string, stdout, stderr io.Writer) int      { return exitUsage }
