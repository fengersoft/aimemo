package cli

import (
	"errors"
	"flag"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"aimemo/internal/store"
)

// listOutput is the payload of the list command.
type listOutput struct {
	Items  []memoryView `json:"items"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

// searchOutput is the payload of the search command.
type searchOutput struct {
	Items []memoryView `json:"items"`
	Total int          `json:"total"`
	Query string       `json:"query"`
}

// categoryView is the payload of one category.
type categoryView struct {
	Name        string `json:"name"`
	MemoryCount int    `json:"memory_count"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// deleteOutput is the payload of the delete command.
type deleteOutput struct {
	Deleted int64 `json:"deleted"`
}

// filterFlags holds the filter options shared by list and search.
type filterFlags struct {
	categories    stringList
	scopes        stringList
	types         stringList
	tags          stringList
	minImportance int
	since         string
	until         string
	limit         int
	offset        int
	sort          string
}

// registerFilterFlags declares the shared filter options.
func registerFilterFlags(fs *flag.FlagSet) *filterFlags {
	f := &filterFlags{}
	fs.Var(&f.categories, "category", "filter by category, repeatable (OR)")
	fs.Var(&f.scopes, "scope", "filter by scope, repeatable (OR)")
	fs.Var(&f.types, "type", "filter by type: summary|important|note, repeatable (OR)")
	fs.Var(&f.tags, "tag", "filter by tag, repeatable (OR)")
	fs.IntVar(&f.minImportance, "min-importance", 0, "minimum importance, 1-5")
	fs.StringVar(&f.since, "since", "", "created at or after, RFC3339 or YYYY-MM-DD")
	fs.StringVar(&f.until, "until", "", "created at or before, RFC3339 or YYYY-MM-DD")
	fs.IntVar(&f.limit, "limit", 20, "maximum results, 1-500")
	fs.IntVar(&f.offset, "offset", 0, "result offset")
	fs.StringVar(&f.sort, "sort", "", "sort: created_at | importance | relevance")
	return f
}

// toFilter validates the filter options and builds a store.Filter.
func (f *filterFlags) toFilter() (store.Filter, error) {
	if f.limit < 1 || f.limit > 500 {
		return store.Filter{}, newError(codeInvalidArgs, "limit must be between 1 and 500")
	}
	if f.offset < 0 {
		return store.Filter{}, newError(codeInvalidArgs, "offset must not be negative")
	}
	if f.minImportance < 0 || f.minImportance > 5 {
		return store.Filter{}, newError(codeInvalidArgs, "min-importance must be between 1 and 5")
	}
	for _, t := range f.types {
		if t != store.TypeSummary && t != store.TypeImportant && t != store.TypeNote {
			return store.Filter{}, newError(codeInvalidArgs, "type must be summary, important or note")
		}
	}
	switch f.sort {
	case "", "created_at", "importance", "relevance":
	default:
		return store.Filter{}, newError(codeInvalidArgs, "sort must be created_at, importance or relevance")
	}
	since, err := parseBoundary(f.since, false)
	if err != nil {
		return store.Filter{}, err
	}
	until, err := parseBoundary(f.until, true)
	if err != nil {
		return store.Filter{}, err
	}
	return store.Filter{
		Categories:    f.categories,
		Scopes:        f.scopes,
		Types:         f.types,
		Tags:          f.tags,
		MinImportance: f.minImportance,
		Since:         since,
		Until:         until,
	}, nil
}

// parseBoundary converts a date or timestamp into the canonical UTC format
// used by created_at. endOfDay expands a plain date to the end of that day.
func parseBoundary(v string, endOfDay bool) (string, error) {
	if strings.TrimSpace(v) == "" {
		return "", nil
	}
	if len(v) == 10 && v[4] == '-' && v[7] == '-' {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return "", newError(codeInvalidArgs, "invalid date %q", v)
		}
		if endOfDay {
			t = t.Add(24*time.Hour - time.Millisecond)
		}
		return t.UTC().Format("2006-01-02T15:04:05.000000Z"), nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return "", newError(codeInvalidArgs, "invalid timestamp %q, expected RFC3339 or YYYY-MM-DD", v)
	}
	return t.UTC().Format("2006-01-02T15:04:05.000000Z"), nil
}

// parseID parses a record id.
func parseID(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 {
		return 0, newError(codeInvalidArgs, "invalid id %q", s)
	}
	return n, nil
}

// runAdd stores a new memory.
func runAdd(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	category := fs.String("category", "", "category name, created automatically")
	content := fs.String("content", "", "body text, use - to read from stdin")
	title := fs.String("title", "", "optional title")
	typ := fs.String("type", store.TypeNote, "summary | important | note")
	importance := fs.Int("importance", 3, "importance from 1 to 5")
	var tags stringList
	fs.Var(&tags, "tag", "tag, repeatable")
	scope := fs.String("scope", "", "optional scope, for example a project name")
	source := fs.String("source", "", "optional source")

	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}

	body := *content
	if body == "-" {
		b, rerr := io.ReadAll(os.Stdin)
		if rerr != nil {
			return fail(g, stderr, newError(codeIOError, "read stdin: %v", rerr))
		}
		body = string(b)
	}

	if *importance < 1 || *importance > 5 {
		return fail(g, stderr, newError(codeInvalidArgs, "importance must be between 1 and 5"))
	}
	if *typ != store.TypeSummary && *typ != store.TypeImportant && *typ != store.TypeNote {
		return fail(g, stderr, newError(codeInvalidArgs, "type must be summary, important or note"))
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	m, err := s.Create(store.CreateInput{
		Category:   *category,
		Type:       *typ,
		Importance: *importance,
		Title:      *title,
		Content:    body,
		Scope:      *scope,
		Source:     *source,
		Tags:       tags,
	})
	if err != nil {
		return fail(g, stderr, err)
	}
	emit(g, stdout, toMemoryView(*m))
	return exitOK
}

// runList returns a filtered page of memories.
func runList(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	ff := registerFilterFlags(fs)

	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	f, err := ff.toFilter()
	if err != nil {
		return fail(g, stderr, err)
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	sort := ff.sort
	if sort == "" {
		sort = "created_at"
	}
	res, err := s.List(f, ff.limit, ff.offset, sort)
	if err != nil {
		return fail(g, stderr, err)
	}
	items := make([]memoryView, 0, len(res.Items))
	for _, m := range res.Items {
		items = append(items, toMemoryPreview(m))
	}
	emit(g, stdout, listOutput{Items: items, Total: res.Total, Limit: ff.limit, Offset: ff.offset})
	return exitOK
}

// runSearch performs a full text search.
func runSearch(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	ff := registerFilterFlags(fs)
	loose := fs.Bool("loose", false, "relax CJK phrase matching to per character AND")

	positional, err := parseFlagSet(fs, args, stdout)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	query := strings.TrimSpace(strings.Join(positional, " "))
	if query == "" {
		return fail(g, stderr, newError(codeInvalidArgs, "search requires a query"))
	}
	f, err := ff.toFilter()
	if err != nil {
		return fail(g, stderr, err)
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	sort := ff.sort
	if sort == "" {
		sort = "importance"
	}
	res, err := s.Search(query, f, ff.limit, ff.offset, sort, *loose)
	if err != nil {
		return fail(g, stderr, err)
	}
	items := make([]memoryView, 0, len(res.Items))
	for _, m := range res.Items {
		items = append(items, toMemoryView(m))
	}
	emit(g, stdout, searchOutput{Items: items, Total: res.Total, Query: res.Query})
	return exitOK
}

// runGet returns one record.
func runGet(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	positional, err := parseFlagSet(fs, args, stdout)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	if len(positional) < 1 {
		return fail(g, stderr, newError(codeInvalidArgs, "get requires an id"))
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

	m, err := s.Get(id)
	if err != nil {
		return fail(g, stderr, err)
	}
	emit(g, stdout, toMemoryView(*m))
	return exitOK
}

// runUpdate changes a record. Only flags that were explicitly given are
// applied, which is detected with flag.Visit.
func runUpdate(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	category := fs.String("category", "", "new category name")
	content := fs.String("content", "", "new body text")
	title := fs.String("title", "", "new title")
	typ := fs.String("type", "", "summary | important | note")
	importance := fs.Int("importance", 0, "importance from 1 to 5")
	var tags stringList
	fs.Var(&tags, "tag", "replace the whole tag list")
	var addTags stringList
	fs.Var(&addTags, "add-tag", "add a tag, repeatable")
	var removeTags stringList
	fs.Var(&removeTags, "remove-tag", "remove a tag, repeatable")
	scope := fs.String("scope", "", "new scope")
	source := fs.String("source", "", "new source")

	positional, err := parseFlagSet(fs, args, stdout)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	if len(positional) < 1 {
		return fail(g, stderr, newError(codeInvalidArgs, "update requires an id"))
	}
	id, err := parseID(positional[0])
	if err != nil {
		return fail(g, stderr, err)
	}

	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	in := store.UpdateInput{AddTags: addTags, RemoveTags: removeTags}
	if given["category"] {
		in.Category = category
	}
	if given["content"] {
		in.Content = content
	}
	if given["title"] {
		in.Title = title
	}
	if given["type"] {
		if *typ != store.TypeSummary && *typ != store.TypeImportant && *typ != store.TypeNote {
			return fail(g, stderr, newError(codeInvalidArgs, "type must be summary, important or note"))
		}
		in.Type = typ
	}
	if given["importance"] {
		if *importance < 1 || *importance > 5 {
			return fail(g, stderr, newError(codeInvalidArgs, "importance must be between 1 and 5"))
		}
		in.Importance = importance
	}
	if given["scope"] {
		in.Scope = scope
	}
	if given["source"] {
		in.Source = source
	}
	if given["tag"] {
		set := append([]string{}, tags...)
		in.SetTags = &set
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	m, err := s.Update(id, in)
	if err != nil {
		return fail(g, stderr, err)
	}
	emit(g, stdout, toMemoryView(*m))
	return exitOK
}

// runDelete removes a record.
func runDelete(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	positional, err := parseFlagSet(fs, args, stdout)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	if len(positional) < 1 {
		return fail(g, stderr, newError(codeInvalidArgs, "delete requires an id"))
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

	if err := s.Delete(id); err != nil {
		return fail(g, stderr, err)
	}
	emit(g, stdout, deleteOutput{Deleted: id})
	return exitOK
}

// runCategories lists every category with its memory count.
func runCategories(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("categories", flag.ContinueOnError)
	sort := fs.String("sort", "name", "name | count | created_at")

	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	switch *sort {
	case "name", "count", "created_at":
	default:
		return fail(g, stderr, newError(codeInvalidArgs, "sort must be name, count or created_at"))
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	cats, err := s.ListCategories(*sort)
	if err != nil {
		return fail(g, stderr, err)
	}
	out := make([]categoryView, 0, len(cats))
	for _, c := range cats {
		out = append(out, categoryView{
			Name:        c.Name,
			MemoryCount: c.MemoryCount,
			CreatedAt:   c.CreatedAt,
			UpdatedAt:   c.UpdatedAt,
		})
	}
	emit(g, stdout, out)
	return exitOK
}

// runStats summarises the store.
func runStats(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
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

	st, err := s.Stats()
	if err != nil {
		return fail(g, stderr, err)
	}
	emit(g, stdout, st)
	return exitOK
}

// usageFlag documents one option of a command.
type usageFlag struct {
	Name     string `json:"name"`
	Required bool   `json:"required,omitempty"`
	Default  string `json:"default,omitempty"`
	Help     string `json:"help"`
}

// usageCommand documents one command.
type usageCommand struct {
	Name    string      `json:"name"`
	Summary string      `json:"summary"`
	Args    []string    `json:"args,omitempty"`
	Flags   []usageFlag `json:"flags,omitempty"`
}

// usageGuide is the machine readable manual handed to the AI.
type usageGuide struct {
	Program  string         `json:"program"`
	Purpose  string         `json:"purpose"`
	Defaults map[string]any `json:"defaults"`
	Commands []usageCommand `json:"commands"`
	Examples []string       `json:"examples"`
}

// usageGuideData builds the manual.
func usageGuideData() usageGuide {
	return usageGuide{
		Program: "aimemo",
		Purpose: "A memory store for AI agents. The agent picks its own categories and records summaries or important information.",
		Defaults: map[string]any{
			"db":        "environment AIMEMO_DB, otherwise ~/.aimemo/aimemo.db",
			"format":    "json",
			"exit_code": "0 success, 1 runtime error, 2 usage error",
		},
		Commands: []usageCommand{
			{
				Name:    "add",
				Summary: "Store a new memory. The category is created automatically when it does not exist.",
				Flags: []usageFlag{
					{Name: "category", Required: true, Help: "category name"},
					{Name: "content", Required: true, Help: "body text, - reads from stdin"},
					{Name: "title", Default: "", Help: "optional title"},
					{Name: "type", Default: "note", Help: "summary | important | note"},
					{Name: "importance", Default: "3", Help: "1 to 5"},
					{Name: "tag", Help: "repeatable"},
					{Name: "scope", Default: "", Help: "optional scope, for example a project name"},
					{Name: "source", Default: "", Help: "optional source"},
				},
			},
			{
				Name:    "list",
				Summary: "List memories by structure. Returns content_preview, not the full body.",
				Flags: []usageFlag{
					{Name: "category", Help: "repeatable, OR"},
					{Name: "scope", Help: "repeatable, OR"},
					{Name: "type", Help: "repeatable, OR"},
					{Name: "tag", Help: "repeatable, OR"},
					{Name: "min-importance", Help: "1 to 5"},
					{Name: "since", Help: "RFC3339 or YYYY-MM-DD"},
					{Name: "until", Help: "RFC3339 or YYYY-MM-DD"},
					{Name: "limit", Default: "20", Help: "1 to 500"},
					{Name: "offset", Default: "0", Help: "result offset"},
					{Name: "sort", Default: "created_at", Help: "created_at | importance"},
				},
			},
			{
				Name:    "search",
				Summary: "Full text search. Chinese substrings match exactly. Returns the full body and a match_context window.",
				Args:    []string{"QUERY"},
				Flags: []usageFlag{
					{Name: "category", Help: "repeatable, OR"},
					{Name: "scope", Help: "repeatable, OR"},
					{Name: "type", Help: "repeatable, OR"},
					{Name: "tag", Help: "repeatable, OR"},
					{Name: "min-importance", Help: "1 to 5"},
					{Name: "since", Help: "RFC3339 or YYYY-MM-DD"},
					{Name: "until", Help: "RFC3339 or YYYY-MM-DD"},
					{Name: "limit", Default: "20", Help: "1 to 500"},
					{Name: "offset", Default: "0", Help: "result offset"},
					{Name: "sort", Default: "importance", Help: "importance | created_at | relevance"},
					{Name: "loose", Default: "false", Help: "relax CJK phrase matching"},
				},
			},
			{
				Name:    "get",
				Summary: "Read one record by id, including the full body.",
				Args:    []string{"ID"},
			},
			{
				Name:    "update",
				Summary: "Change a record. Only the flags that are given are applied.",
				Args:    []string{"ID"},
				Flags: []usageFlag{
					{Name: "category", Help: "new category"},
					{Name: "content", Help: "new body"},
					{Name: "title", Help: "new title"},
					{Name: "type", Help: "summary | important | note"},
					{Name: "importance", Help: "1 to 5"},
					{Name: "scope", Help: "new scope"},
					{Name: "source", Help: "new source"},
					{Name: "tag", Help: "replace the whole tag list"},
					{Name: "add-tag", Help: "repeatable"},
					{Name: "remove-tag", Help: "repeatable"},
				},
			},
			{
				Name:    "delete",
				Summary: "Delete a record.",
				Args:    []string{"ID"},
			},
			{
				Name:    "categories",
				Summary: "List existing categories with their memory counts. Reuse an existing category instead of creating a near duplicate.",
				Flags: []usageFlag{
					{Name: "sort", Default: "name", Help: "name | count | created_at"},
				},
			},
			{
				Name:    "stats",
				Summary: "Counts by category, type and scope, plus the average importance.",
			},
			{
				Name:    "usage",
				Summary: "Print this guide.",
			},
		},
		Examples: []string{
			`aimemo add --category "技术选型" --type important --importance 5 --title "选型结论" --content "最终选择 SQLite 作为本地持久化数据库" --tag db --tag 选型`,
			`aimemo categories`,
			`aimemo search 数据库 --limit 5`,
			`aimemo list --category "技术选型" --min-importance 4 --sort importance`,
			`aimemo get 12`,
			`aimemo update 12 --importance 3 --add-tag 复核`,
			`aimemo delete 12`,
		},
	}
}

// runUsage prints the AI facing manual.
func runUsage(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	emit(g, stdout, usageGuideData())
	return exitOK
}
