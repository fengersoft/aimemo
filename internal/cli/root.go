package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"aimemo/internal/store"
)

// Process exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// Machine readable error codes.
const (
	codeInvalidArgs = "invalid_args"
	codeNotFound    = "not_found"
	codeConflict    = "conflict"
	codeDBError     = "db_error"
	codeIOError     = "io_error"
)

// appError carries a machine readable code alongside a human message.
type appError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *appError) Error() string { return e.Message }

func newError(code, format string, args ...any) *appError {
	return &appError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// envelope is the stable output contract consumed by the AI.
type envelope struct {
	OK    bool      `json:"ok"`
	Data  any       `json:"data,omitempty"`
	Error *appError `json:"error,omitempty"`
}

// globalOpts holds options valid for every command.
type globalOpts struct {
	dbPath string
	format string
}

// commandNames lists the supported subcommands.
var commandNames = []string{
	"add", "list", "search", "get", "update", "delete", "categories", "stats", "usage",
}

// Run executes one invocation and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	g, rest := extractGlobals(args)
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "aimemo: a command is required")
		printCommandList(stderr)
		return exitUsage
	}
	cmd, cmdArgs := rest[0], rest[1:]
	switch cmd {
	case "add":
		return runAdd(g, cmdArgs, stdout, stderr)
	case "list":
		return runList(g, cmdArgs, stdout, stderr)
	case "search":
		return runSearch(g, cmdArgs, stdout, stderr)
	case "get":
		return runGet(g, cmdArgs, stdout, stderr)
	case "update":
		return runUpdate(g, cmdArgs, stdout, stderr)
	case "delete":
		return runDelete(g, cmdArgs, stdout, stderr)
	case "categories":
		return runCategories(g, cmdArgs, stdout, stderr)
	case "stats":
		return runStats(g, cmdArgs, stdout, stderr)
	case "usage":
		return runUsage(g, cmdArgs, stdout, stderr)
	case "help", "-h", "--help":
		printCommandList(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "aimemo: unknown command %q\n", cmd)
		printCommandList(stderr)
		return exitUsage
	}
}

// extractGlobals pulls --db and --format out of the argument list so they may
// appear before or after the subcommand.
func extractGlobals(args []string) (globalOpts, []string) {
	g := globalOpts{dbPath: defaultDBPath(), format: "json"}
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--db" && i+1 < len(args):
			g.dbPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--db="):
			g.dbPath = strings.TrimPrefix(a, "--db=")
		case a == "--format" && i+1 < len(args):
			g.format = args[i+1]
			i++
		case strings.HasPrefix(a, "--format="):
			g.format = strings.TrimPrefix(a, "--format=")
		default:
			rest = append(rest, a)
		}
	}
	return g, rest
}

// defaultDBPath honours AIMEMO_DB, otherwise uses ~/.aimemo/aimemo.db.
func defaultDBPath() string {
	if p := os.Getenv("AIMEMO_DB"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "aimemo.db"
	}
	return filepath.Join(home, ".aimemo", "aimemo.db")
}

// openStore opens the database for a command.
func openStore(g globalOpts) (*store.Store, error) {
	s, err := store.Open(g.dbPath)
	if err != nil {
		return nil, newError(codeIOError, "open database: %v", err)
	}
	return s, nil
}

// fail reports an error on stderr and returns the exit code.
func fail(g globalOpts, stderr io.Writer, err error) int {
	code := codeDBError
	var ae *appError
	switch {
	case errors.As(err, &ae):
		code = ae.Code
	case errors.Is(err, store.ErrInvalid):
		code = codeInvalidArgs
	case errors.Is(err, store.ErrNotFound):
		code = codeNotFound
	}
	if g.format == "text" {
		fmt.Fprintf(stderr, "aimemo: %v\n", err)
	} else {
		b, merr := json.Marshal(envelope{OK: false, Error: &appError{Code: code, Message: err.Error()}})
		if merr != nil {
			fmt.Fprintf(stderr, "{\"ok\":false,\"error\":{\"code\":%q,\"message\":%q}}\n", code, err.Error())
		} else {
			fmt.Fprintf(stderr, "%s\n", b)
		}
	}
	if code == codeInvalidArgs {
		return exitUsage
	}
	return exitError
}

// printCommandList writes the supported commands.
func printCommandList(w io.Writer) {
	fmt.Fprintln(w, "usage: aimemo [--db PATH] [--format json|text] <command> [options]")
	fmt.Fprintln(w, "commands:")
	for _, c := range commandNames {
		fmt.Fprintf(w, "  %s\n", c)
	}
	fmt.Fprintln(w, "  help")
	fmt.Fprintln(w, "run 'aimemo usage' for the full AI facing guide")
}

// stringList collects a repeatable string flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// isBoolFlag reports whether a flag takes no value.
func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}

// splitArgs separates positional arguments from flags so that positionals may
// appear before or after flags. Go's flag package stops at the first
// positional, which would break "aimemo search 数据库 --limit 5".
func splitArgs(fs *flag.FlagSet, args []string) (positional []string, flagArgs []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			name := strings.TrimLeft(a, "-")
			if strings.Contains(name, "=") {
				flagArgs = append(flagArgs, a)
				continue
			}
			f := fs.Lookup(name)
			if f != nil && !isBoolFlag(f) && i+1 < len(args) {
				flagArgs = append(flagArgs, a, args[i+1])
				i++
				continue
			}
			flagArgs = append(flagArgs, a)
			continue
		}
		positional = append(positional, a)
	}
	return positional, flagArgs
}

// parseFlagSet parses args into fs, tolerating positionals in any position.
// It returns the positional arguments.
func parseFlagSet(fs *flag.FlagSet, args []string, stdout io.Writer) ([]string, error) {
	fs.SetOutput(io.Discard)
	positional, flagArgs := splitArgs(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(stdout)
			fs.Usage()
			return positional, errHelpRequested
		}
		return positional, err
	}
	return positional, nil
}

// errHelpRequested signals that the user asked for the command help.
var errHelpRequested = errors.New("help requested")
