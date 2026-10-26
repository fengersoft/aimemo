package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"aimemo/internal/store"
)

// runUse records that a memory was retrieved and used.
func runUse(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("use", flag.ContinueOnError)
	positional, err := parseFlagSet(fs, args, stdout)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	if len(positional) < 1 {
		return fail(g, stderr, newError(codeInvalidArgs, "use requires an id"))
	}
	id, err := parseID(positional[0])
	if err != nil {
		return fail(g, stderr, err)
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	m, err := s.MarkUsed(id, g.workspace)
	if err != nil {
		return fail(g, stderr, err)
	}
	emit(g, stdout, toMemoryView(*m))
	return exitOK
}

// runFeedback records whether a memory was helpful or wrong.
func runFeedback(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("feedback", flag.ContinueOnError)
	helpful := fs.Bool("helpful", false, "the memory helped solve the task")
	wrong := fs.Bool("wrong", false, "the memory was misleading or wrong")
	positional, err := parseFlagSet(fs, args, stdout)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	if len(positional) < 1 {
		return fail(g, stderr, newError(codeInvalidArgs, "feedback requires an id"))
	}
	if *helpful == *wrong {
		return fail(g, stderr, newError(codeInvalidArgs, "provide exactly one of --helpful or --wrong"))
	}
	id, err := parseID(positional[0])
	if err != nil {
		return fail(g, stderr, err)
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	m, err := s.Feedback(id, g.workspace, *helpful)
	if err != nil {
		return fail(g, stderr, err)
	}
	emit(g, stdout, toMemoryView(*m))
	return exitOK
}

// runDoctor checks the database, the FTS index and the PATH setup.
func runDoctor(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	rebuild := fs.Bool("rebuild", false, "rebuild the FTS index when it is inconsistent")
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}

	binary, _ := os.Executable()
	path, pathErr := exec.LookPath("aimemo")
	onPath := pathErr == nil && path != ""

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	health, err := s.Health()
	if err != nil {
		return fail(g, stderr, err)
	}
	rebuilt := 0
	if *rebuild || !health.Consistent {
		rebuilt, err = s.RebuildFTS()
		if err != nil {
			return fail(g, stderr, err)
		}
		if health, err = s.Health(); err != nil {
			return fail(g, stderr, err)
		}
	}
	// also rebuild semantic features when --rebuild is given
	featuresRebuilt := 0
	if *rebuild {
		featuresRebuilt, err = s.RebuildFeatures()
		if err != nil {
			return fail(g, stderr, err)
		}
	}
	emit(g, stdout, map[string]any{
		"database":         g.dbPath,
		"binary":           binary,
		"on_path":          onPath,
		"path":             path,
		"rebuilt":          rebuilt,
		"features_rebuilt": featuresRebuilt,
		"health":           health,
		"healthy":          health.Consistent && onPath,
	})
	return exitOK
}

// runBackup writes a consistent copy of the database.
func runBackup(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	out := fs.String("out", "", "backup file path")
	force := fs.Bool("force", false, "overwrite an existing backup file")
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	if strings.TrimSpace(*out) == "" {
		return fail(g, stderr, newError(codeInvalidArgs, "backup requires --out"))
	}
	if *force {
		if err := os.Remove(*out); err != nil && !os.IsNotExist(err) {
			return fail(g, stderr, newError(codeIOError, "%v", err))
		}
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	if err := s.Backup(*out); err != nil {
		return fail(g, stderr, err)
	}
	emit(g, stdout, map[string]any{"backup": *out})
	return exitOK
}

// mdDoc is a parsed markdown export.
type mdDoc struct {
	id       int64
	meta     map[string]string
	tags     []string
	title    string
	sections map[string]string
	content  string
	path     string
}

// parseMarkdown reads the subset of YAML frontmatter that export-md writes.
func parseMarkdown(path string, raw []byte) (*mdDoc, error) {
	doc := &mdDoc{meta: map[string]string{}, sections: map[string]string{}, path: path}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	i := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		i = 1
		var listKey string
		for ; i < len(lines); i++ {
			line := lines[i]
			if strings.TrimSpace(line) == "---" {
				i++
				break
			}
			if strings.HasPrefix(line, "  - ") || strings.HasPrefix(line, "- ") {
				v := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "  - "), "- "))
				if listKey == "tags" {
					doc.tags = append(doc.tags, unquoteYAML(v))
				}
				continue
			}
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if v == "" {
				listKey = k
				continue
			}
			listKey = ""
			doc.meta[k] = unquoteYAML(v)
		}
	}

	if id, err := strconv.ParseInt(doc.meta["id"], 10, 64); err == nil && id > 0 {
		doc.id = id
	}
	doc.title = doc.meta["title"]

	var body strings.Builder
	var preamble, current string
	flush := func() {
		text := strings.TrimSpace(body.String())
		body.Reset()
		if current == "" {
			if preamble == "" {
				preamble = text
			}
			return
		}
		doc.sections[current] = text
	}
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "# ") && doc.title == "" {
			doc.title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			continue
		}
		if strings.HasPrefix(line, "## ") {
			flush()
			current = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	flush()

	doc.content = doc.sections["经验内容"]
	if doc.content == "" {
		doc.content = preamble
	}
	if doc.content == "" {
		doc.content = strings.TrimSpace(doc.sections["经验"])
	}
	return doc, nil
}

// unquoteYAML removes the double quotes that %q adds and unescapes them.
func unquoteYAML(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		if s, err := strconv.Unquote(v); err == nil {
			return s
		}
	}
	return v
}

// docCreateInput maps a markdown document onto a create input.
func docCreateInput(doc *mdDoc, workspace string) store.CreateInput {
	importance, _ := strconv.Atoi(doc.meta["importance"])
	confidence, _ := strconv.Atoi(doc.meta["confidence"])
	supersedes, _ := strconv.ParseInt(doc.meta["supersedes"], 10, 64)
	category := doc.meta["category"]
	if strings.TrimSpace(category) == "" {
		category = "imported"
	}
	typ := doc.meta["type"]
	if strings.TrimSpace(typ) == "" {
		typ = "note"
	}
	if importance == 0 {
		importance = 3
	}
	if confidence == 0 {
		confidence = 3
	}
	ws := doc.meta["workspace"]
	if workspace != "" {
		ws = workspace
	}
	return store.CreateInput{
		Category:   category,
		Type:       typ,
		Importance: importance,
		Title:      doc.title,
		Content:    doc.content,
		Problem:    doc.sections["问题"],
		Solution:   doc.sections["解决方案"],
		Pitfall:    doc.sections["踩坑与注意事项"],
		Example:    doc.sections["示例"],
		Scope:      doc.meta["scope"],
		Source:     doc.meta["source"],
		Workspace:  ws,
		Confidence: confidence,
		ValidUntil: doc.meta["valid_until"],
		Supersedes: supersedes,
		Tags:       doc.tags,
		CreatedAt:  docTimestamp(doc.meta["created_at"]),
		UpdatedAt:  docTimestamp(doc.meta["updated_at"]),
	}
}

// docTimestamp keeps a canonical timestamp and drops anything else.
func docTimestamp(v string) string {
	v = strings.TrimSpace(v)
	if _, err := time.Parse("2006-01-02T15:04:05.000000Z", v); err != nil {
		return ""
	}
	return v
}

// runImportMd imports markdown files written by export-md.
func runImportMd(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import-md", flag.ContinueOnError)
	dir := fs.String("dir", "./markdown", "directory to import")
	overwrite := fs.Bool("overwrite", false, "update records whose id already exists")
	allowDuplicate := fs.Bool("allow-duplicate", false, "import even when a similar title exists")
	dry := fs.Bool("dry-run", false, "parse but do not write")
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}

	var docs []*mdDoc
	err := filepath.WalkDir(*dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		if strings.EqualFold(d.Name(), "README.md") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		doc, err := parseMarkdown(path, raw)
		if err != nil {
			return err
		}
		docs = append(docs, doc)
		return nil
	})
	if err != nil {
		return fail(g, stderr, newError(codeIOError, "%v", err))
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].path < docs[j].path })

	if *dry {
		emit(g, stdout, map[string]any{"dry_run": true, "count": len(docs)})
		return exitOK
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	created, updated, skipped, failed := 0, 0, 0, 0
	for _, doc := range docs {
		if strings.TrimSpace(doc.content) == "" {
			failed++
			continue
		}
		in := docCreateInput(doc, g.workspace)
		if *overwrite && doc.id > 0 {
			if _, err := s.Get(doc.id, in.Workspace); err == nil {
				_, err := s.Update(doc.id, in.Workspace, store.UpdateInput{
					Category:   &in.Category,
					Type:       &in.Type,
					Importance: &in.Importance,
					Title:      &in.Title,
					Content:    &in.Content,
					Problem:    &in.Problem,
					Solution:   &in.Solution,
					Pitfall:    &in.Pitfall,
					Example:    &in.Example,
					Scope:      &in.Scope,
					Source:     &in.Source,
					Workspace:  &in.Workspace,
					Confidence: &in.Confidence,
					ValidUntil: &in.ValidUntil,
					Supersedes: &in.Supersedes,
					SetTags:    &in.Tags,
				})
				if err != nil {
					failed++
					continue
				}
				updated++
				continue
			}
		}
		if !*allowDuplicate && strings.TrimSpace(in.Title) != "" {
			similar, err := s.FindSimilar(in.Title, 0, in.Workspace)
			if err != nil {
				failed++
				continue
			}
			if len(similar) > 0 {
				skipped++
				continue
			}
		}
		if _, err := s.Create(in); err != nil {
			failed++
			continue
		}
		created++
	}

	emit(g, stdout, map[string]any{
		"created": created,
		"updated": updated,
		"skipped": skipped,
		"failed":  failed,
		"total":   len(docs),
	})
	return exitOK
}

var (
	ver = "dev"
	com = "unknown"
	dat = "unknown"
)

// SetVersion injects build metadata.
func SetVersion(v, c, d string) {
	if v != "" {
		ver = v
	}
	if c != "" {
		com = c
	}
	if d != "" {
		dat = d
	}
}

func runVersion(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	emit(g, stdout, map[string]any{
		"version": ver,
		"commit":  com,
		"date":    dat,
	})
	return exitOK
}

func runPrune(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	expired := fs.Bool("expired", false, "prune expired memories")
	superseded := fs.Bool("superseded", false, "prune superseded memories")
	older := fs.String("older-than", "", "prune older than duration like 90d")
	dry := fs.Bool("dry-run", false, "list without deleting")
	force := fs.Bool("force", false, "do not prompt")
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()
	res, err := s.Prune(store.PruneOptions{Workspace: g.workspace, Expired: *expired, Superseded: *superseded, OlderThan: *older, DryRun: *dry || !*force})
	if err != nil {
		return fail(g, stderr, err)
	}
	if res != nil && res.DryRun && len(res.Ids) > 0 {
		type item struct {
			ID        int64  `json:"id"`
			Title     string `json:"title"`
			Category  string `json:"category"`
			Workspace string `json:"workspace"`
		}
		items := make([]item, 0, len(res.Ids))
		for _, id := range res.Ids {
			if m, gerr := s.Get(id, g.workspace); gerr == nil {
				items = append(items, item{ID: m.ID, Title: m.Title, Category: m.Category, Workspace: m.Workspace})
			}
		}
		emit(g, stdout, map[string]any{"deleted": 0, "dry_run": true, "ids": res.Ids, "items": items})
		return exitOK
	}
	emit(g, stdout, res)
	return exitOK
}

func runExportJSON(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export-json", flag.ContinueOnError)
	out := fs.String("out", "", "output file")
	all := fs.Bool("all", false, "export every workspace")
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()
	var res []store.Memory
	if *all {
		res, err = s.ExportJSONAll()
	} else {
		res, err = s.ExportJSON(g.workspace)
	}
	if err != nil {
		return fail(g, stderr, err)
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	if *out == "" {
		_, err = stdout.Write(append(b, '\n'))
		return exitOK
	}
	if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
		return fail(g, stderr, newError(codeIOError, "%v", err))
	}
	emit(g, stdout, map[string]any{"exported": len(res), "out": *out})
	return exitOK
}

func runImportJSON(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import-json", flag.ContinueOnError)
	in := fs.String("in", "", "input file")
	overwrite := fs.Bool("overwrite", false, "overwrite by id")
	allowDup := fs.Bool("allow-duplicate", false, "allow duplicate titles")
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	if *in == "" {
		return fail(g, stderr, newError(codeInvalidArgs, "import-json requires --in"))
	}
	raw, err := os.ReadFile(*in)
	if err != nil {
		return fail(g, stderr, newError(codeIOError, "%v", err))
	}
	var list []store.CreateInput
	if err := json.Unmarshal(raw, &list); err != nil {
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()
	created, updated, skipped, failed := 0, 0, 0, 0
	for i := range list {
		cin := list[i]
		if g.workspace != "" {
			cin.Workspace = g.workspace
		}
		if cin.CreatedAt == "" {
			cin.CreatedAt = store.Now()
		}
		if cin.UpdatedAt == "" {
			cin.UpdatedAt = cin.CreatedAt
		}
		if *overwrite && strings.TrimSpace(cin.Title) != "" {
			if m, err := s.FindByTitle(cin.Title, cin.Workspace); err == nil {
				cat := cin.Category
				typ := cin.Type
				imp := cin.Importance
				title := cin.Title
				content := cin.Content
				prob := cin.Problem
				sol := cin.Solution
				pit := cin.Pitfall
				ex := cin.Example
				scope := cin.Scope
				src := cin.Source
				ws := cin.Workspace
				conf := cin.Confidence
				vu := cin.ValidUntil
				sup := cin.Supersedes
				tags := cin.Tags
				if _, err := s.Update(m.ID, ws, store.UpdateInput{Category: &cat, Type: &typ, Importance: &imp, Title: &title, Content: &content, Problem: &prob, Solution: &sol, Pitfall: &pit, Example: &ex, Scope: &scope, Source: &src, Workspace: &ws, Confidence: &conf, ValidUntil: &vu, Supersedes: &sup, SetTags: &tags}); err == nil {
					updated++
					continue
				}
			}
		}
		if !*allowDup && strings.TrimSpace(cin.Title) != "" {
			if sim, err := s.FindSimilar(cin.Title, 0, cin.Workspace); err == nil && len(sim) > 0 {
				skipped++
				continue
			}
		}
		if _, err := s.Create(cin); err != nil {
			failed++
			continue
		}
		created++
	}
	emit(g, stdout, map[string]any{"created": created, "updated": updated, "skipped": skipped, "failed": failed, "total": len(list)})
	return exitOK
}

func runTag(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tag", flag.ContinueOnError)
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	if len(args) == 0 {
		return fail(g, stderr, newError(codeInvalidArgs, "tag requires subcommand"))
	}
	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()
	switch args[0] {
	case "list":
		m, err := s.TagStats(g.workspace)
		if err != nil {
			return fail(g, stderr, err)
		}
		emit(g, stdout, m)
		return exitOK
	case "add":
		if len(args) < 3 {
			return fail(g, stderr, newError(codeInvalidArgs, "tag add <id> <tag>..."))
		}
		id, err := parseID(args[1])
		if err != nil {
			return fail(g, stderr, err)
		}
		m, err := s.AddTags(id, g.workspace, args[2:])
		if err != nil {
			return fail(g, stderr, err)
		}
		emit(g, stdout, toMemoryView(*m))
		return exitOK
	case "rm":
		if len(args) < 3 {
			return fail(g, stderr, newError(codeInvalidArgs, "tag rm <id> <tag>..."))
		}
		id, err := parseID(args[1])
		if err != nil {
			return fail(g, stderr, err)
		}
		m, err := s.RemoveTags(id, g.workspace, args[2:])
		if err != nil {
			return fail(g, stderr, err)
		}
		emit(g, stdout, toMemoryView(*m))
		return exitOK
	default:
		return fail(g, stderr, newError(codeInvalidArgs, "unknown tag subcommand"))
	}
}

func runWorkspaces(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("workspaces", flag.ContinueOnError)
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()
	res, err := s.Workspaces()
	if err != nil {
		return fail(g, stderr, err)
	}
	emit(g, stdout, res)
	return exitOK
}

func runCompletion(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("completion", flag.ContinueOnError)
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	if len(args) == 0 {
		return fail(g, stderr, newError(codeInvalidArgs, "completion requires shell"))
	}
	shell := args[0]
	switch shell {
	case "bash":
		fmt.Fprintln(stdout, "# bash completion for aimemo\n_aimemo() {\n  local cur prev cmds\n  COMPREPLY=()\n  cur=\"${COMP_WORDS[COMP_CWORD]}\"\n  prev=\"${COMP_WORDS[COMP_CWORD-1]}\"\n  cmds=\"add list search context get update delete use feedback categories stats usage version export-md import-md export-json import-json init doctor backup prune tag workspaces completion web\"\n  if [ $COMP_CWORD -eq 1 ]; then\n    COMPREPLY=( $(compgen -W \"${cmds}\" -- ${cur}) )\n    return 0\n  fi\n}\ncomplete -F _aimemo aimemo")
		return exitOK
	case "zsh":
		fmt.Fprintln(stdout, "#compdef aimemo\n_aimemo() {\n  local -a cmds\n  cmds=(add list search context get update delete use feedback categories stats usage version export-md import-md export-json import-json init doctor backup prune tag workspaces completion web)\n  _describe 'command' cmds\n}\n_aimemo \"$@\"")
		return exitOK
	case "fish":
		fmt.Fprintln(stdout, "complete -c aimemo -f -a \"add list search context get update delete use feedback categories stats usage version export-md import-md export-json import-json init doctor backup prune tag workspaces completion web\"")
		return exitOK
	case "powershell":
		fmt.Fprintln(stdout, "Register-ArgumentCompleter -Native -CommandName aimemo -ScriptBlock {")
		fmt.Fprintln(stdout, "  param($wordToComplete,$commandAst,$cursorPosition)")
		fmt.Fprintln(stdout, "  $cmds = @('add','list','search','context','get','update','delete','use','feedback','categories','stats','usage','version','export-md','import-md','export-json','import-json','init','doctor','backup','prune','tag','workspaces','completion','web')")
		fmt.Fprintln(stdout, "  $cmds | Where-Object { $_ -like \"$wordToComplete*\" } | ForEach-Object { [System.Management.Automation.CompletionResult]::new($_,$_,'ParameterValue',$_) }")
		fmt.Fprintln(stdout, "}")
		return exitOK
	default:
		return fail(g, stderr, newError(codeInvalidArgs, "unknown shell"))
	}
}

func runUpdateSelf(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("updateself", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite existing files even if unchanged")
	globalOnly := fs.Bool("global", false, "update only global opencode config")
	localOnly := fs.Bool("local", false, "update only project templates")
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}

	exe, _ := os.Executable()
	home, _ := os.UserHomeDir()
	updated := make([]string, 0)
	skipped := make([]string, 0)

	updateFile := func(dst string, data []byte) error {
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		if !*force {
			if old, err := os.ReadFile(dst); err == nil && string(old) == string(data) {
				skipped = append(skipped, dst)
				return nil
			}
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return err
		}
		updated = append(updated, dst)
		return nil
	}

	// Project templates (AGENTS.md managed block, skill, agent)
	if !*globalOnly {
		targetDir := "."
		detected := detectLang(targetDir)
		// AGENTS.md
		if data, err := templateFS.ReadFile("templates/AGENTS.md"); err == nil {
			data = []byte(strings.ReplaceAll(string(data), "<!--LANG-->", detected))
			agentsPath := filepath.Join(targetDir, "AGENTS.md")
			if existing, readErr := os.ReadFile(agentsPath); readErr == nil {
				merged := mergeManagedBlock(string(existing), string(data))
				if *force || merged != string(existing) {
					if err := os.WriteFile(agentsPath, []byte(merged), 0644); err == nil {
						updated = append(updated, agentsPath)
					}
				} else {
					skipped = append(skipped, agentsPath)
				}
			} else {
				_ = os.WriteFile(agentsPath, []byte(wrapManagedBlock(string(data))), 0644)
				updated = append(updated, agentsPath)
			}
			// sync CLAUDE.md
			claudePath := filepath.Join(targetDir, "CLAUDE.md")
			if cex, cerr := os.ReadFile(claudePath); cerr == nil {
				cmerged := mergeManagedBlock(string(cex), string(data))
				if *force || cmerged != string(cex) {
					if err := os.WriteFile(claudePath, []byte(cmerged), 0644); err == nil {
						updated = append(updated, claudePath)
					}
				} else {
					skipped = append(skipped, claudePath)
				}
			} else if os.IsNotExist(cerr) {
				if err := os.WriteFile(claudePath, []byte(wrapManagedBlock(string(data))), 0644); err == nil {
					updated = append(updated, claudePath)
				}
			}
			// skill/agent
			for _, f := range []struct{ src, dst string }{
				{"templates/skill/skill.yaml", filepath.Join(".opencode", "skills", "aimemo", "skill.yaml")},
				{"templates/skill/skill.json", filepath.Join(".opencode", "skills", "aimemo", "skill.json")},
				{"templates/skill/README.md", filepath.Join(".opencode", "skills", "aimemo", "README.md")},
				{"templates/agent/memory-retriever.md", filepath.Join(".opencode", "agent", "memory-retriever.md")},
				{"templates/agent/memory-curator.md", filepath.Join(".opencode", "agent", "memory-curator.md")},
			} {
				if data, err := templateFS.ReadFile(f.src); err == nil {
					if err := updateFile(filepath.Join(targetDir, f.dst), data); err == nil {
						// already tracked
					}
				}
			}
		}
	}

	// Global opencode agents
	if !*localOnly && home != "" {
		globalAgentDir := filepath.Join(home, ".config", "opencode", "agent")
		for _, name := range []string{"memory-retriever.md", "memory-curator.md"} {
			if data, err := templateFS.ReadFile("templates/agent/" + name); err == nil {
				_ = updateFile(filepath.Join(globalAgentDir, name), data)
			}
		}
		// global skill
		globalSkillDir := filepath.Join(home, ".agents", "skills", "aimemo")
		for _, f := range []struct{ src, name string }{
			{"templates/skill/skill.yaml", "skill.yaml"},
			{"templates/skill/skill.json", "skill.json"},
			{"templates/skill/README.md", "README.md"},
		} {
			if data, err := templateFS.ReadFile(f.src); err == nil {
				_ = updateFile(filepath.Join(globalSkillDir, f.name), data)
			}
		}
		// Claude global agents
		claudeGlobal := filepath.Join(home, ".claude", "agents")
		if err := os.MkdirAll(claudeGlobal, 0755); err == nil {
			for _, name := range []string{"memory-retriever.md", "memory-curator.md"} {
				if data, err := templateFS.ReadFile("templates/agent/" + name); err == nil {
					_ = updateFile(filepath.Join(claudeGlobal, name), data)
				}
			}
		}
		// sync CLAUDE.md globally
		if data, err := templateFS.ReadFile("templates/AGENTS.md"); err == nil {
			detected := detectLang(".")
			data = []byte(strings.ReplaceAll(string(data), "<!--LANG-->", detected))
			claudePath := filepath.Join(home, ".claude", "CLAUDE.md")
			if cex, cerr := os.ReadFile(claudePath); cerr == nil {
				cmerged := mergeManagedBlock(string(cex), string(data))
				if cmerged != string(cex) {
					_ = os.WriteFile(claudePath, []byte(cmerged), 0644)
				}
			} else if os.IsNotExist(cerr) {
				_ = os.WriteFile(claudePath, []byte(wrapManagedBlock(string(data))), 0644)
			}
		}
	}

	emit(g, stdout, map[string]any{
		"exe":     exe,
		"updated": updated,
		"skipped": skipped,
	})
	return exitOK
}

// runConflicts detects contradictions between stored experiences.
func runConflicts(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("conflicts", flag.ContinueOnError)
	maxScan := fs.Int("max-scan", 500, "maximum memories to scan (1-2000)")
	allWs := fs.Bool("all-workspaces", false, "scan across all workspaces")
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	if *maxScan < 1 || *maxScan > 2000 {
		return fail(g, stderr, newError(codeInvalidArgs, "max-scan must be between 1 and 2000"))
	}
	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	ws := g.workspace
	if *allWs {
		ws = ""
	}
	conflicts, err := s.DetectConflicts(ws, *maxScan)
	if err != nil {
		return fail(g, stderr, err)
	}
	emit(g, stdout, map[string]any{
		"count":     len(conflicts),
		"conflicts": conflicts,
	})
	return exitOK
}
