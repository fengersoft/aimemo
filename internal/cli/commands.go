package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	categories     stringList
	scopes         stringList
	types          stringList
	tags           stringList
	minImportance  int
	minConfidence  int
	includeExpired bool
	allWorkspaces  bool
	since          string
	until          string
	limit          int
	offset         int
	sort           string
}

// registerFilterFlags declares the shared filter options.
func registerFilterFlags(fs *flag.FlagSet) *filterFlags {
	f := &filterFlags{}
	fs.Var(&f.categories, "category", "filter by category, repeatable (OR)")
	fs.Var(&f.scopes, "scope", "filter by scope, repeatable (OR)")
	fs.Var(&f.types, "type", "filter by type: summary|important|note, repeatable (OR)")
	fs.Var(&f.tags, "tag", "filter by tag, repeatable (OR)")
	fs.IntVar(&f.minImportance, "min-importance", 0, "minimum importance, 1-5")
	fs.IntVar(&f.minConfidence, "min-confidence", 0, "minimum confidence, 1-5")
	fs.BoolVar(&f.includeExpired, "include-expired", false, "include memories past their valid_until")
	fs.BoolVar(&f.allWorkspaces, "all-workspaces", false, "search across all workspaces")
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
	if f.minConfidence < 0 || f.minConfidence > 5 {
		return store.Filter{}, newError(codeInvalidArgs, "min-confidence must be between 1 and 5")
	}
	for _, t := range f.types {
		if t != store.TypeSummary && t != store.TypeImportant && t != store.TypeNote {
			return store.Filter{}, newError(codeInvalidArgs, "type must be summary, important or note")
		}
	}
	switch f.sort {
	case "", "created_at", "importance", "relevance", "confidence", "helpful":
	default:
		return store.Filter{}, newError(codeInvalidArgs, "sort must be created_at, importance, relevance, confidence or helpful")
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
		Categories:     f.categories,
		Scopes:         f.scopes,
		Types:          f.types,
		Tags:           f.tags,
		MinImportance:  f.minImportance,
		MinConfidence:  f.minConfidence,
		IncludeExpired: f.includeExpired,
		AllWorkspaces:  f.allWorkspaces,
		Since:          since,
		Until:          until,
	}, nil
}

// applyGlobals scopes a filter to the workspace selected on the command line.
func applyGlobals(f store.Filter, g globalOpts) store.Filter {
	if g.workspace != "" {
		f.Workspace = g.workspace
	}
	return f
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
	confidence := fs.Int("confidence", 3, "confidence from 1 to 5")
	problem := fs.String("problem", "", "optional problem statement")
	solution := fs.String("solution", "", "optional solution description")
	pitfall := fs.String("pitfall", "", "optional pitfall or caveat")
	example := fs.String("example", "", "optional code or usage example")
	validUntil := fs.String("valid-until", "", "optional expiry date, YYYY-MM-DD")
	supersedes := fs.Int64("supersedes", 0, "optional id of the memory this one replaces")
	allowDuplicate := fs.Bool("allow-duplicate", false, "store even when a similar title exists")
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
	if *confidence < 1 || *confidence > 5 {
		return fail(g, stderr, newError(codeInvalidArgs, "confidence must be between 1 and 5"))
	}
	if *typ != store.TypeSummary && *typ != store.TypeImportant && *typ != store.TypeNote {
		return fail(g, stderr, newError(codeInvalidArgs, "type must be summary, important or note"))
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	if !*allowDuplicate && *supersedes == 0 && strings.TrimSpace(*title) != "" {
		similar, err := s.FindSimilar(*title, 0, g.workspace)
		if err != nil {
			return fail(g, stderr, err)
		}
		if len(similar) > 0 {
			ids := make([]string, 0, len(similar))
			for _, m := range similar {
				ids = append(ids, fmt.Sprintf("#%d %s", m.ID, m.Title))
			}
			return fail(g, stderr, newError(codeConflict,
				"similar memory already exists: %s (use --allow-duplicate to store anyway)",
				strings.Join(ids, "; ")))
		}
	}

	m, err := s.Create(store.CreateInput{
		Category:   *category,
		Type:       *typ,
		Importance: *importance,
		Title:      *title,
		Content:    body,
		Problem:    *problem,
		Solution:   *solution,
		Pitfall:    *pitfall,
		Example:    *example,
		Scope:      *scope,
		Source:     *source,
		Workspace:  g.workspace,
		Confidence: *confidence,
		ValidUntil: *validUntil,
		Supersedes: *supersedes,
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
	f = applyGlobals(f, g)

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
	semantic := fs.Bool("semantic", false, "use semantic (feature-vector) search instead of FTS")

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
	f = applyGlobals(f, g)

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	if *semantic {
		results, err := s.SearchSemantic(query, f, ff.limit, 1000)
		if err != nil {
			return fail(g, stderr, err)
		}
		items := make([]memoryView, 0, len(results))
		for _, r := range results {
			v := toMemoryView(r.Memory)
			v.SemanticScore = r.Score
			items = append(items, v)
		}
		emit(g, stdout, searchOutput{Items: items, Total: len(results), Query: query})
		return exitOK
	}

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

// contextTerm is one retrieval term with a relative weight.
type contextTerm struct {
	text   string
	weight float64
}

// contextStopWords are common words that carry no retrieval signal.
var contextStopWords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "are": true, "was": true,
	"to": true, "of": true, "and": true, "or": true, "for": true, "in": true,
	"on": true, "at": true, "with": true, "by": true, "as": true, "it": true,
	"this": true, "that": true, "how": true, "what": true, "why": true,
	"when": true, "do": true, "does": true, "can": true, "be": true, "fix": true,
	"issue": true, "problem": true,
	"的": true, "了": true, "是": true, "在": true, "和": true, "与": true,
	"怎么": true, "如何": true, "什么": true, "为什么": true, "可以": true,
	"需要": true, "一个": true, "这个": true, "那个": true, "帮我": true,
	"看看": true, "问题": true, "现在": true, "我们": true, "你们": true,
}

// isWordRune reports whether r may appear in a retrieval term.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// extractContextTerms turns a task description into weighted retrieval terms.
// CJK runs are expanded into bigrams so that a long sentence still matches
// stored notes that contain only part of it.
func extractContextTerms(task string) []contextTerm {
	terms := make([]contextTerm, 0, 32)
	seen := make(map[string]bool)
	add := func(text string, weight float64) {
		text = strings.ToLower(strings.TrimSpace(text))
		if text == "" || seen[text] || contextStopWords[text] {
			return
		}
		seen[text] = true
		terms = append(terms, contextTerm{text: text, weight: weight})
	}

	runes := []rune(task)
	for i := 0; i < len(runes); {
		if !isWordRune(runes[i]) {
			i++
			continue
		}
		j := i
		cjk := store.IsCJK(runes[i])
		for j < len(runes) && isWordRune(runes[j]) && store.IsCJK(runes[j]) == cjk {
			j++
		}
		group := runes[i:j]
		if cjk {
			if len(group) <= 6 {
				add(string(group), 3)
			}
			for k := 0; k+1 < len(group); k++ {
				add(string(group[k:k+2]), 1)
			}
		} else if len(group) >= 2 {
			add(string(group), 2)
		}
		i = j
	}
	if len(terms) > 48 {
		terms = terms[:48]
	}
	return terms
}

// recencyBonus rewards recently stored memories.
func recencyBonus(created string) float64 {
	t, err := time.Parse("2006-01-02T15:04:05.000000Z", created)
	if err != nil {
		return 0
	}
	days := time.Since(t).Hours() / 24
	switch {
	case days <= 30:
		return 3
	case days <= 180:
		return 2
	case days <= 365:
		return 1
	}
	return 0
}

// scoreBreakdown explains how a memory was ranked.
type scoreBreakdown struct {
	Title      float64 `json:"title"`
	Content    float64 `json:"content"`
	Tags       float64 `json:"tags"`
	Category   float64 `json:"category"`
	Importance float64 `json:"importance"`
	Recency    float64 `json:"recency"`
	Feedback   float64 `json:"feedback"`
}

// combinedBody joins the indexed body fields of a memory.
func combinedBody(m store.Memory) string {
	parts := make([]string, 0, 5)
	for _, p := range []string{m.Content, m.Problem, m.Solution, m.Pitfall, m.Example} {
		if strings.TrimSpace(p) != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n\n")
}

// scoreMemory ranks a memory against the extracted task terms.
func scoreMemory(m store.Memory, terms []contextTerm) (float64, scoreBreakdown) {
	title := strings.ToLower(m.Title)
	content := strings.ToLower(combinedBody(m))
	tags := strings.ToLower(strings.Join(m.Tags, " "))
	category := strings.ToLower(m.Category)

	var b scoreBreakdown
	for _, t := range terms {
		if strings.Contains(title, t.text) {
			b.Title += t.weight * 3
		}
		if strings.Contains(tags, t.text) {
			b.Tags += t.weight * 2
		}
		if strings.Contains(content, t.text) {
			b.Content += t.weight
		}
		if strings.Contains(category, t.text) {
			b.Category += t.weight
		}
	}
	b.Importance = float64(m.Importance) * 0.5
	b.Recency = recencyBonus(m.CreatedAt)
	b.Feedback = float64(m.HelpfulCount)*1.5 - float64(m.WrongCount)*2 + float64(m.UseCount)*0.2
	total := b.Title + b.Content + b.Tags + b.Category + b.Importance + b.Recency + b.Feedback
	return total, b
}

// contextItem is one memory inside a context pack.
type contextItem struct {
	ID         int64           `json:"id"`
	Title      string          `json:"title"`
	Category   string          `json:"category"`
	Type       string          `json:"type"`
	Importance int             `json:"importance"`
	Confidence int             `json:"confidence"`
	Tags       []string        `json:"tags"`
	Score      float64         `json:"score"`
	Breakdown  *scoreBreakdown `json:"breakdown,omitempty"`
	Content    string          `json:"content"`
	Truncated  bool            `json:"truncated,omitempty"`
}

// contextOutput is the payload of the context command.
type contextOutput struct {
	Query      string        `json:"query"`
	Keywords   []string      `json:"keywords"`
	Items      []contextItem `json:"items"`
	Context    string        `json:"context"`
	Chars      int           `json:"chars"`
	Candidates int           `json:"candidates"`
}

// buildContextBlock renders the ranked memories into a token-budgeted block.
func buildContextBlock(mems []store.Memory, maxChars int) (string, []contextItem) {
	var b strings.Builder
	out := make([]contextItem, 0, len(mems))
	used := 0

	for _, m := range mems {
		title := m.Title
		if strings.TrimSpace(title) == "" {
			title = fmt.Sprintf("Memory #%d", m.ID)
		}
		header := fmt.Sprintf("### #%d %s\n", m.ID, title)
		meta := fmt.Sprintf("- 分类: %s | 重要度: %d | 类型: %s", m.Category, m.Importance, m.Type)
		if len(m.Tags) > 0 {
			meta += " | 标签: " + strings.Join(m.Tags, ", ")
		}
		header += meta + "\n\n"

		hLen := utf8.RuneCountInString(header)
		if used+hLen >= maxChars {
			break
		}
		remain := maxChars - used - hLen
		body := combinedBody(m)
		truncated := false
		if utf8.RuneCountInString(body) > remain {
			if remain < 80 {
				break
			}
			body = string([]rune(body)[:remain-1]) + "…"
			truncated = true
		}

		b.WriteString(header)
		b.WriteString(body)
		b.WriteString("\n\n")
		used += hLen + utf8.RuneCountInString(body) + 1
		out = append(out, contextItem{
			ID:         m.ID,
			Title:      title,
			Category:   m.Category,
			Type:       m.Type,
			Importance: m.Importance,
			Confidence: m.Confidence,
			Tags:       m.Tags,
			Content:    body,
			Truncated:  truncated,
		})
	}
	return strings.TrimSpace(b.String()), out
}

// runContext builds a ranked, token-budgeted experience pack for a task.
func runContext(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("context", flag.ContinueOnError)
	ff := registerFilterFlags(fs)
	ff.limit = 8
	maxChars := fs.Int("max-chars", 4000, "maximum characters in the context block")
	maxTokens := fs.Int("max-tokens", 0, "approximate token budget, overrides max-chars")
	explain := fs.Bool("explain", false, "include the score breakdown of each item")

	positional, err := parseFlagSet(fs, args, stdout)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	task := strings.TrimSpace(strings.Join(positional, " "))
	if task == "" {
		return fail(g, stderr, newError(codeInvalidArgs, "context requires a task description"))
	}
	if *maxTokens > 0 {
		*maxChars = *maxTokens * 2
	}
	if *maxChars < 200 || *maxChars > 100000 {
		return fail(g, stderr, newError(codeInvalidArgs, "max-chars must be between 200 and 100000"))
	}
	if ff.limit < 1 || ff.limit > 20 {
		return fail(g, stderr, newError(codeInvalidArgs, "limit must be between 1 and 20 for context"))
	}
	f, err := ff.toFilter()
	if err != nil {
		return fail(g, stderr, err)
	}
	f = applyGlobals(f, g)

	terms := extractContextTerms(task)
	if len(terms) == 0 {
		return fail(g, stderr, newError(codeInvalidArgs, "context found no searchable terms"))
	}
	query := make([]string, 0, len(terms))
	keywords := make([]string, 0, len(terms))
	for _, t := range terms {
		query = append(query, t.text)
		keywords = append(keywords, t.text)
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	candidates, err := s.SearchAny(strings.Join(query, " "), f, 1000, false)
	if err != nil {
		return fail(g, stderr, err)
	}

	type scored struct {
		m     store.Memory
		score float64
	}
	ranked := make([]scored, 0, len(candidates))
	breakdowns := make(map[int64]scoreBreakdown, len(candidates))
	for _, m := range candidates {
		score, breakdown := scoreMemory(m, terms)
		ranked = append(ranked, scored{m: m, score: score})
		breakdowns[m.ID] = breakdown
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].m.Importance != ranked[j].m.Importance {
			return ranked[i].m.Importance > ranked[j].m.Importance
		}
		return ranked[i].m.CreatedAt > ranked[j].m.CreatedAt
	})
	if len(ranked) > ff.limit {
		ranked = ranked[:ff.limit]
	}

	mems := make([]store.Memory, 0, len(ranked))
	for _, r := range ranked {
		mems = append(mems, r.m)
	}
	block, items := buildContextBlock(mems, *maxChars)
	for i := range items {
		items[i].Score = math.Round(ranked[i].score*100) / 100
		if *explain {
			b := breakdowns[items[i].ID]
			b.Title = math.Round(b.Title*100) / 100
			b.Content = math.Round(b.Content*100) / 100
			b.Tags = math.Round(b.Tags*100) / 100
			b.Category = math.Round(b.Category*100) / 100
			b.Importance = math.Round(b.Importance*100) / 100
			b.Recency = math.Round(b.Recency*100) / 100
			b.Feedback = math.Round(b.Feedback*100) / 100
			items[i].Breakdown = &b
		}
	}

	if g.format == "prompt" {
		if strings.TrimSpace(block) == "" {
			fmt.Fprintln(stdout, "未找到相关历史经验")
			return exitOK
		}
		fmt.Fprintln(stdout, block)
		return exitOK
	}

	emit(g, stdout, contextOutput{
		Query:      task,
		Keywords:   keywords,
		Items:      items,
		Context:    block,
		Chars:      utf8.RuneCountInString(block),
		Candidates: len(candidates),
	})
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

	m, err := s.Get(id, g.workspace)
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
	problem := fs.String("problem", "", "new problem statement")
	solution := fs.String("solution", "", "new solution description")
	pitfall := fs.String("pitfall", "", "new pitfall or caveat")
	example := fs.String("example", "", "new code or usage example")
	confidence := fs.Int("confidence", 0, "confidence from 1 to 5")
	validUntil := fs.String("valid-until", "", "expiry date, YYYY-MM-DD, empty clears it")
	supersedes := fs.Int64("supersedes", 0, "id of the memory this one replaces")

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
	if given["problem"] {
		in.Problem = problem
	}
	if given["solution"] {
		in.Solution = solution
	}
	if given["pitfall"] {
		in.Pitfall = pitfall
	}
	if given["example"] {
		in.Example = example
	}
	if given["confidence"] {
		if *confidence < 1 || *confidence > 5 {
			return fail(g, stderr, newError(codeInvalidArgs, "confidence must be between 1 and 5"))
		}
		in.Confidence = confidence
	}
	if given["valid-until"] {
		in.ValidUntil = validUntil
	}
	if given["supersedes"] {
		in.Supersedes = supersedes
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

	m, err := s.Update(id, g.workspace, in)
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

	if err := s.Delete(id, g.workspace); err != nil {
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

	cats, err := s.ListCategories(*sort, g.workspace)
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

	st, err := s.Stats(g.workspace)
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
		Purpose: "A memory store for AI agents. Record reusable knowledge, skills, patterns, and lessons learned to improve future reasoning and task quality.",
		Defaults: map[string]any{
			"db":        "environment AIMEMO_DB, otherwise aimemo.db next to the executable",
			"format":    "json",
			"workspace": "global --workspace flag, isolated memory namespace",
			"exit_code": "0 success, 1 runtime error, 2 usage error",
		},
		Commands: []usageCommand{
			{
				Name:    "add",
				Summary: "Store reusable knowledge, skills, or lessons learned. The category is created automatically when it does not exist.",
				Flags: []usageFlag{
					{Name: "category", Required: true, Help: "category name"},
					{Name: "content", Required: true, Help: "body text, - reads from stdin"},
					{Name: "title", Default: "", Help: "optional title"},
					{Name: "type", Default: "note", Help: "summary | important | note"},
					{Name: "importance", Default: "3", Help: "1 to 5"},
					{Name: "tag", Help: "repeatable"},
					{Name: "scope", Default: "", Help: "optional scope, for example a project name"},
					{Name: "source", Default: "", Help: "optional source"},
					{Name: "problem", Help: "structured: what problem this solves"},
					{Name: "solution", Help: "structured: how it was solved"},
					{Name: "pitfall", Help: "structured: pitfalls to avoid"},
					{Name: "example", Help: "structured: code or command example"},
					{Name: "confidence", Default: "3", Help: "1 to 5"},
					{Name: "valid-until", Help: "RFC3339 or YYYY-MM-DD expiry"},
					{Name: "supersedes", Help: "id of the memory this replaces"},
					{Name: "allow-duplicate", Help: "skip the duplicate title check"},
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
					{Name: "min-confidence", Help: "1 to 5"},
					{Name: "include-expired", Help: "keep memories past valid_until"},
					{Name: "workspace", Help: "restrict to one workspace"},
					{Name: "since", Help: "RFC3339 or YYYY-MM-DD"},
					{Name: "until", Help: "RFC3339 or YYYY-MM-DD"},
					{Name: "limit", Default: "20", Help: "1 to 500"},
					{Name: "offset", Default: "0", Help: "result offset"},
					{Name: "sort", Default: "created_at", Help: "created_at | importance | confidence"},
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
					{Name: "min-confidence", Help: "1 to 5"},
					{Name: "include-expired", Help: "keep memories past valid_until"},
					{Name: "workspace", Help: "restrict to one workspace"},
					{Name: "since", Help: "RFC3339 or YYYY-MM-DD"},
					{Name: "until", Help: "RFC3339 or YYYY-MM-DD"},
					{Name: "limit", Default: "20", Help: "1 to 500"},
					{Name: "offset", Default: "0", Help: "result offset"},
					{Name: "sort", Default: "importance", Help: "importance | created_at | relevance"},
					{Name: "loose", Default: "false", Help: "relax CJK phrase matching"},
				},
			},
			{
				Name:    "context",
				Summary: "Build a ranked, deduplicated, token-budgeted experience pack for a task. Prefer this over search when starting a task.",
				Args:    []string{"TASK"},
				Flags: []usageFlag{
					{Name: "limit", Default: "5", Help: "1 to 20"},
					{Name: "max-chars", Default: "4000", Help: "character budget"},
					{Name: "max-tokens", Help: "token budget, counted as two characters per token"},
					{Name: "explain", Help: "include the score breakdown"},
					{Name: "category", Help: "repeatable, OR"},
					{Name: "tag", Help: "repeatable, OR"},
					{Name: "type", Help: "repeatable, OR"},
					{Name: "min-importance", Help: "1 to 5"},
					{Name: "min-confidence", Help: "1 to 5"},
					{Name: "workspace", Help: "restrict to one workspace"},
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
					{Name: "problem", Help: "structured: what problem this solves"},
					{Name: "solution", Help: "structured: how it was solved"},
					{Name: "pitfall", Help: "structured: pitfalls to avoid"},
					{Name: "example", Help: "structured: code or command example"},
					{Name: "confidence", Help: "1 to 5"},
					{Name: "valid-until", Help: "RFC3339 or YYYY-MM-DD expiry"},
					{Name: "supersedes", Help: "id of the memory this replaces"},
				},
			},
			{
				Name:    "delete",
				Summary: "Delete a record.",
				Args:    []string{"ID"},
			},
			{
				Name:    "use",
				Summary: "Record that a memory was retrieved and used. Feeds the helpfulness score.",
				Args:    []string{"ID"},
			},
			{
				Name:    "feedback",
				Summary: "Record whether a memory was helpful or wrong. Feeds the ranking score.",
				Args:    []string{"ID"},
				Flags: []usageFlag{
					{Name: "helpful", Help: "the memory helped solve the task"},
					{Name: "wrong", Help: "the memory was misleading or wrong"},
				},
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
				Name:    "export-md",
				Summary: "Export valuable memories as human-readable markdown files.",
				Flags: []usageFlag{
					{Name: "out", Default: "./markdown", Help: "output directory"},
					{Name: "min-importance", Default: "3", Help: "minimum importance"},
					{Name: "all", Help: "ignore min-importance"},
					{Name: "category", Help: "repeatable category filter"},
					{Name: "tag", Help: "repeatable tag filter"},
					{Name: "type", Help: "repeatable type filter"},
					{Name: "ids", Help: "comma separated memory ids"},
					{Name: "overwrite", Help: "overwrite existing files"},
					{Name: "no-index", Help: "do not write README.md index"},
					{Name: "dry-run", Help: "preview without writing"},
				},
			},
			{
				Name:    "import-md",
				Summary: "Import markdown files written by export-md back into the database.",
				Flags: []usageFlag{
					{Name: "dir", Default: "./markdown", Help: "directory to import"},
					{Name: "overwrite", Help: "update records whose id already exists"},
					{Name: "dry-run", Help: "parse without writing"},
				},
			},
			{
				Name:    "init",
				Summary: "Initialize AGENTS.md, the aimemo skill, and memory agents in a project directory. Re-running merges the managed block instead of overwriting user content. Memory agents are installed globally by default.",
				Flags: []usageFlag{
					{Name: "dir", Default: ".", Help: "target directory"},
					{Name: "lang", Help: "project language (auto-detected when empty)"},
					{Name: "force", Help: "overwrite existing files"},
					{Name: "no-global", Help: "skip installing memory agents to the global opencode config"},
				},
			},
			{
				Name:    "doctor",
				Summary: "Check database integrity, the FTS index, and whether aimemo is on PATH.",
			},
			{
				Name:    "backup",
				Summary: "Write a consistent copy of the database.",
				Flags: []usageFlag{
					{Name: "out", Required: true, Help: "backup file path"},
					{Name: "force", Help: "overwrite an existing backup file"},
				},
			},
			{
				Name:    "usage",
				Summary: "Print this guide.",
			},
			{Name: "updateself", Summary: "Update local and global opencode agent/skill templates from embedded versions.", Args: []string{"[--force] [--global] [--local]"}},
			{Name: "web", Summary: "Browse memories in a local web UI.", Args: []string{"[--addr 127.0.0.1:8080] [--open]"}},
			{Name: "version", Summary: "Show build version."},
			{Name: "prune", Summary: "Remove expired or superseded memories.", Args: []string{"[--expired] [--superseded] [--older-than 90d] [--dry-run] [--force]"}},
			{Name: "export-json", Summary: "Export memories as JSON.", Args: []string{"[--out file.json] [--all]"}},
			{Name: "import-json", Summary: "Import memories from JSON.", Args: []string{"--in file.json [--overwrite] [--allow-duplicate]"}},
			{Name: "tag", Summary: "Manage tags on a memory.", Args: []string{"add <id> <tag>... | rm <id> <tag>... | list"}},
			{Name: "workspaces", Summary: "List workspaces with counts."},
			{Name: "completion", Summary: "Generate shell completion scripts.", Args: []string{"bash|zsh|fish|powershell"}},
		},
		Examples: []string{
			`aimemo add --category skills/go --type important --importance 4 --title "error handling" --content "prefer wrapping errors with context" --tag pattern --tag go`,
			`aimemo search 并发`,
			`aimemo context "修复 sqlite busy 超时" --limit 5 --format prompt`,
			`aimemo list --category skills/go --min-confidence 4`,
			`aimemo get 1`,
			`aimemo use 1`,
			`aimemo feedback 1 --helpful`,
			`aimemo update 1 --add-tag proven`,
			`aimemo categories`,
			`aimemo stats`,
			`aimemo init`,
			`aimemo doctor`,
			`aimemo backup --out ./aimemo-backup.db`,
			`aimemo export-md --out ./markdown --min-importance 4`,
			`aimemo import-md --dir ./markdown --overwrite`,
			`aimemo usage`,
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

// runExportMd exports selected memories as human-readable markdown files.
func runExportMd(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export-md", flag.ContinueOnError)
	out := fs.String("out", "./markdown", "output directory")
	minImp := fs.Int("min-importance", 3, "minimum importance")
	all := fs.Bool("all", false, "ignore min-importance")
	overwrite := fs.Bool("overwrite", false, "overwrite existing files")
	dry := fs.Bool("dry-run", false, "preview only")
	noIndex := fs.Bool("no-index", false, "do not write README.md index")
	var cats stringList
	var tags stringList
	var types stringList
	var ids stringList
	fs.Var(&cats, "category", "category filter (repeatable)")
	fs.Var(&tags, "tag", "tag filter (repeatable)")
	fs.Var(&types, "type", "type filter (repeatable)")
	fs.Var(&ids, "ids", "comma separated ids or repeatable")
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}

	idSet := map[int64]bool{}
	for _, raw := range ids {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, err := parseID(part)
			if err != nil {
				return fail(g, stderr, err)
			}
			idSet[id] = true
		}
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	f := applyGlobals(store.Filter{
		Categories: cats,
		Types:      types,
		Tags:       tags,
	}, g)
	res, err := s.List(f, 10000, 0, "importance")
	if err != nil {
		return fail(g, stderr, err)
	}

	selected := make([]store.Memory, 0, len(res.Items))
	for _, m := range res.Items {
		if len(idSet) > 0 && !idSet[m.ID] {
			continue
		}
		if len(idSet) == 0 && !*all && m.Importance < *minImp {
			continue
		}
		selected = append(selected, m)
	}

	if *dry {
		outList := make([]map[string]any, 0, len(selected))
		for _, m := range selected {
			outList = append(outList, map[string]any{"id": m.ID, "title": m.Title, "category": m.Category, "importance": m.Importance})
		}
		emit(g, stdout, map[string]any{"dry_run": true, "count": len(selected), "items": outList})
		return exitOK
	}

	root, err := filepath.Abs(*out)
	if err != nil {
		return fail(g, stderr, newError(codeIOError, "%v", err))
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fail(g, stderr, newError(codeIOError, "%v", err))
	}

	files := make([]exportedFile, 0, len(selected))
	used := map[string]bool{}
	exported, skipped := 0, 0

	for _, m := range selected {
		relDir, err := safeCategoryPath(m.Category)
		if err != nil {
			return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
		}
		name := slug(m.Title)
		if name == "" {
			name = fmt.Sprintf("id-%d", m.ID)
		}
		rel := filepath.Join(relDir, name+".md")
		for used[rel] {
			name = fmt.Sprintf("%s-%d", name, m.ID)
			rel = filepath.Join(relDir, name+".md")
		}
		path := filepath.Join(root, rel)
		if !withinDir(root, path) {
			return fail(g, stderr, newError(codeInvalidArgs, "category escapes the output directory: %q", m.Category))
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fail(g, stderr, newError(codeIOError, "%v", err))
		}
		if _, err := os.Stat(path); err == nil && !*overwrite {
			skipped++
			used[rel] = true
			files = append(files, exportedFile{m: m, rel: rel, path: path})
			continue
		}
		if err := os.WriteFile(path, []byte(renderHumanMD(m)), 0o644); err != nil {
			return fail(g, stderr, newError(codeIOError, "%v", err))
		}
		used[rel] = true
		exported++
		files = append(files, exportedFile{m: m, rel: rel, path: path})
	}

	if !*noIndex {
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(renderIndex(files)), 0o644); err != nil {
			return fail(g, stderr, newError(codeIOError, "%v", err))
		}
	}

	emit(g, stdout, map[string]any{"exported": exported, "skipped": skipped, "out": root})
	return exitOK
}

// exportedFile remembers where one memory was written.
type exportedFile struct {
	m    store.Memory
	rel  string
	path string
}

// safeCategoryPath turns a category into a relative directory, rejecting
// traversal segments and sanitising characters Windows cannot store.
func safeCategoryPath(category string) (string, error) {
	parts := strings.Split(strings.ReplaceAll(category, "\\", "/"), "/")
	clean := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "." {
			continue
		}
		if p == ".." {
			return "", fmt.Errorf("category %q contains a traversal segment", category)
		}
		p = strings.Map(func(r rune) rune {
			if strings.ContainsRune(`<>:"|?*`, r) || r < 0x20 {
				return '_'
			}
			return r
		}, p)
		if isReservedName(p) {
			p = "_" + p
		}
		clean = append(clean, p)
	}
	return filepath.Join(clean...), nil
}

// isReservedName reports whether p is a Windows reserved device name.
func isReservedName(p string) bool {
	base := strings.ToUpper(strings.TrimSuffix(p, filepath.Ext(p)))
	switch base {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}

// withinDir reports whether target stays inside root.
func withinDir(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// slug builds a readable file name and keeps Unicode letters so Chinese
// titles stay recognisable.
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}
	res := strings.Trim(b.String(), "-")
	if res == "" {
		return ""
	}
	runes := []rune(res)
	if len(runes) > 80 {
		res = string(runes[:80])
	}
	return strings.Trim(res, "-")
}

func renderHumanMD(m store.Memory) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("id: %d\n", m.ID))
	sb.WriteString(fmt.Sprintf("title: %q\n", m.Title))
	sb.WriteString(fmt.Sprintf("category: %q\n", m.Category))
	sb.WriteString(fmt.Sprintf("type: %q\n", m.Type))
	sb.WriteString(fmt.Sprintf("importance: %d\n", m.Importance))
	sb.WriteString(fmt.Sprintf("confidence: %d\n", m.Confidence))
	if len(m.Tags) > 0 {
		sb.WriteString("tags:\n")
		for _, t := range m.Tags {
			sb.WriteString(fmt.Sprintf("  - %q\n", t))
		}
	} else {
		sb.WriteString("tags: []\n")
	}
	sb.WriteString(fmt.Sprintf("scope: %q\n", m.Scope))
	sb.WriteString(fmt.Sprintf("source: %q\n", m.Source))
	if m.Workspace != "" {
		sb.WriteString(fmt.Sprintf("workspace: %q\n", m.Workspace))
	}
	if m.ValidUntil != "" {
		sb.WriteString(fmt.Sprintf("valid_until: %q\n", m.ValidUntil))
	}
	if m.Supersedes > 0 {
		sb.WriteString(fmt.Sprintf("supersedes: %d\n", m.Supersedes))
	}
	sb.WriteString(fmt.Sprintf("created_at: %q\n", m.CreatedAt))
	sb.WriteString(fmt.Sprintf("updated_at: %q\n", m.UpdatedAt))
	sb.WriteString(fmt.Sprintf("exported_at: %q\n", time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")))
	sb.WriteString("---\n\n")

	title := m.Title
	if strings.TrimSpace(title) == "" {
		title = fmt.Sprintf("Memory #%d", m.ID)
	}
	sb.WriteString("# " + title + "\n\n")
	sb.WriteString(fmt.Sprintf("> 分类：%s ｜ 重要度：%d ｜ 置信度：%d", m.Category, m.Importance, m.Confidence))
	if len(m.Tags) > 0 {
		sb.WriteString(" ｜ 标签：" + strings.Join(m.Tags, ", "))
	}
	sb.WriteString("\n\n")

	sb.WriteString("## 经验内容\n\n" + strings.TrimSpace(m.Content) + "\n\n")
	if strings.TrimSpace(m.Problem) != "" {
		sb.WriteString("## 问题\n\n" + strings.TrimSpace(m.Problem) + "\n\n")
	}
	if strings.TrimSpace(m.Solution) != "" {
		sb.WriteString("## 解决方案\n\n" + strings.TrimSpace(m.Solution) + "\n\n")
	}
	if strings.TrimSpace(m.Pitfall) != "" {
		sb.WriteString("## 踩坑与注意事项\n\n" + strings.TrimSpace(m.Pitfall) + "\n\n")
	}
	if strings.TrimSpace(m.Example) != "" {
		sb.WriteString("## 示例\n\n" + strings.TrimSpace(m.Example) + "\n\n")
	}
	if strings.TrimSpace(m.Scope) != "" || strings.TrimSpace(m.Source) != "" || m.Workspace != "" {
		sb.WriteString("## 来源\n\n")
		if m.Workspace != "" {
			sb.WriteString("- 工作区：" + m.Workspace + "\n")
		}
		if m.Scope != "" {
			sb.WriteString("- 范围：" + m.Scope + "\n")
		}
		if m.Source != "" {
			sb.WriteString("- 来源：" + m.Source + "\n")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderIndex writes a table of contents for the exported files.
func renderIndex(files []exportedFile) string {
	var sb strings.Builder
	sb.WriteString("# aimemo 经验索引\n\n")
	sb.WriteString("> 由 `aimemo export-md` 生成，请勿手工编辑。\n\n")
	sb.WriteString("| 标题 | 分类 | 重要度 | 文件 |\n")
	sb.WriteString("|---|---|---|---|\n")
	for _, f := range files {
		title := f.m.Title
		if strings.TrimSpace(title) == "" {
			title = fmt.Sprintf("Memory #%d", f.m.ID)
		}
		title = strings.ReplaceAll(title, "|", "\\|")
		sb.WriteString(fmt.Sprintf("| %s | %s | %d | [%s](%s) |\n",
			title, f.m.Category, f.m.Importance, filepath.ToSlash(f.rel), filepath.ToSlash(f.rel)))
	}
	sb.WriteString("\n")
	return sb.String()
}

// initFiles lists the embedded templates written by init.
var initFiles = []struct{ src, dst string }{
	{"templates/AGENTS.md", "AGENTS.md"},
	{"templates/skill/skill.yaml", filepath.Join(".opencode", "skills", "aimemo", "skill.yaml")},
	{"templates/skill/skill.json", filepath.Join(".opencode", "skills", "aimemo", "skill.json")},
	{"templates/skill/README.md", filepath.Join(".opencode", "skills", "aimemo", "README.md")},
	{"templates/agent/memory-retriever.md", filepath.Join(".opencode", "agent", "memory-retriever.md")},
	{"templates/agent/memory-curator.md", filepath.Join(".opencode", "agent", "memory-curator.md")},
}

// detectLang guesses the primary programming language of dir.
func detectLang(dir string) string {
	checks := []struct {
		path string
		lang string
	}{
		{"go.mod", "go"},
		{"package.json", "typescript"},
		{"pyproject.toml", "python"},
		{"requirements.txt", "python"},
		{"Cargo.toml", "rust"},
		{"pom.xml", "java"},
		{"build.gradle", "java"},
		{"build.gradle.kts", "java"},
		{"composer.json", "php"},
		{"Gemfile", "ruby"},
		{"CMakeLists.txt", "cpp"},
	}
	for _, c := range checks {
		if _, err := os.Stat(filepath.Join(dir, c.path)); err == nil {
			return c.lang
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.csproj"))
	if len(matches) > 0 {
		return "csharp"
	}
	return "unknown"
}

// runInit initializes AGENTS.md, the aimemo skill, and memory agents in a project.
func runInit(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	dir := fs.String("dir", ".", "target directory")
	lang := fs.String("lang", "", "project language (auto-detected when empty)")
	force := fs.Bool("force", false, "overwrite existing files")
	noGlobal := fs.Bool("no-global", false, "skip installing memory agents to the global opencode config")
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}

	targetDir := *dir
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fail(g, stderr, newError(codeIOError, "%v", err))
	}

	detected := *lang
	if detected == "" {
		detected = detectLang(targetDir)
	}

	agentsPath := filepath.Join(targetDir, "AGENTS.md")
	written := make([]string, 0, len(initFiles))
	skipped := make([]string, 0, len(initFiles))
	mergedAgents := false
	for _, f := range initFiles {
		data, err := templateFS.ReadFile(f.src)
		if err != nil {
			return fail(g, stderr, newError(codeIOError, "read template %s: %v", f.src, err))
		}
		isAgents := f.src == "templates/AGENTS.md"
		if isAgents {
			data = []byte(strings.ReplaceAll(string(data), "<!--LANG-->", detected))
		}
		dst := filepath.Join(targetDir, f.dst)
		if isAgents {
			existing, readErr := os.ReadFile(agentsPath)
			switch {
			case readErr == nil && !*force:
				merged := mergeManagedBlock(string(existing), string(data))
				if err := os.WriteFile(agentsPath, []byte(merged), 0644); err != nil {
					return fail(g, stderr, newError(codeIOError, "%v", err))
				}
				written = append(written, f.dst)
				mergedAgents = merged != string(existing)
				// also sync CLAUDE.md
				claudePath := filepath.Join(targetDir, "CLAUDE.md")
				if cex, cerr := os.ReadFile(claudePath); cerr == nil {
					if cmerged := mergeManagedBlock(string(cex), string(data)); cmerged != string(cex) {
						if err := os.WriteFile(claudePath, []byte(cmerged), 0644); err == nil {
							written = append(written, "CLAUDE.md")
						}
					}
				} else if os.IsNotExist(cerr) {
					if err := os.WriteFile(claudePath, []byte(wrapManagedBlock(string(data))), 0644); err == nil {
						written = append(written, "CLAUDE.md")
					}
				}
				continue
			case readErr != nil && !os.IsNotExist(readErr):
				return fail(g, stderr, newError(codeIOError, "%v", readErr))
			default:
				data = []byte(wrapManagedBlock(string(data)))
				if isAgents {
					claudePath := filepath.Join(targetDir, "CLAUDE.md")
					if _, cerr := os.Stat(claudePath); os.IsNotExist(cerr) {
						if err := os.WriteFile(claudePath, data, 0644); err == nil {
							written = append(written, "CLAUDE.md")
						}
					}
				}
			}
		} else if _, statErr := os.Stat(dst); statErr == nil && !*force {
			skipped = append(skipped, f.dst)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return fail(g, stderr, newError(codeIOError, "%v", err))
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return fail(g, stderr, newError(codeIOError, "%v", err))
		}
		written = append(written, f.dst)
	}

	// Cursor rules for broader compatibility
	cursorRulesDir := filepath.Join(targetDir, ".cursor", "rules")
	if err := os.MkdirAll(cursorRulesDir, 0755); err == nil {
		if data, cerr := templateFS.ReadFile("templates/AGENTS.md"); cerr == nil {
			curData := []byte(strings.ReplaceAll(string(data), "<!--LANG-->", detected))
			frontMatter := strings.Join([]string{"---", "alwaysApply: true", "description: aimemo", "---", ""}, "\n")
			cursorDst := filepath.Join(cursorRulesDir, "aimemo.mdc")
			if cex, rerr := os.ReadFile(cursorDst); rerr == nil {
				merged := mergeManagedBlock(string(cex), string(curData))
				if *force || merged != string(cex) {
					if err := os.WriteFile(cursorDst, []byte(merged), 0644); err == nil {
						written = append(written, cursorDst)
					}
				} else {
					skipped = append(skipped, cursorDst)
				}
			} else if os.IsNotExist(rerr) {
				content := frontMatter + wrapManagedBlock(string(curData))
				if err := os.WriteFile(cursorDst, []byte(content), 0644); err == nil {
					written = append(written, cursorDst)
				}
			}
		}
	}

	if !*noGlobal {
		home, err := os.UserHomeDir()
		if err != nil {
			return fail(g, stderr, newError(codeIOError, "%v", err))
		}
		globalAgentDir := filepath.Join(home, ".config", "opencode", "agent")
		if err := os.MkdirAll(globalAgentDir, 0755); err != nil {
			return fail(g, stderr, newError(codeIOError, "%v", err))
		}
		for _, name := range []string{"memory-retriever.md", "memory-curator.md"} {
			data, err := templateFS.ReadFile("templates/agent/" + name)
			if err != nil {
				return fail(g, stderr, newError(codeIOError, "%v", err))
			}
			dst := filepath.Join(globalAgentDir, name)
			if _, statErr := os.Stat(dst); statErr == nil && !*force {
				skipped = append(skipped, dst)
				continue
			}
			if err := os.WriteFile(dst, data, 0644); err != nil {
				return fail(g, stderr, newError(codeIOError, "%v", err))
			}
			written = append(written, dst)
		}
	}

	path, pathErr := exec.LookPath("aimemo")
	onPath := pathErr == nil && path != ""

	emit(g, stdout, map[string]any{
		"dir":           targetDir,
		"language":      detected,
		"written":       written,
		"skipped":       skipped,
		"agents_merged": mergedAgents,
		"on_path":       onPath,
		"path":          path,
		"hint":          hintForPath(onPath),
	})
	return exitOK
}

// hintForPath reminds the user to install aimemo on PATH when it is missing.
func hintForPath(onPath bool) string {
	if onPath {
		return ""
	}
	return "aimemo is not on PATH; add its directory to PATH so agents can run it"
}

const managedBegin = "<!-- aimemo:begin -->"
const managedEnd = "<!-- aimemo:end -->"

// wrapManagedBlock marks generated content so later runs can replace only it.
func wrapManagedBlock(content string) string {
	return managedBegin + "\n" + strings.TrimSpace(content) + "\n" + managedEnd + "\n"
}

// mergeManagedBlock replaces the managed region in existing, appending it when
// the file has no markers yet.
func mergeManagedBlock(existing, content string) string {
	payload := wrapManagedBlock(content)
	if i := strings.Index(existing, managedBegin); i >= 0 {
		if j := strings.Index(existing[i:], managedEnd); j >= 0 {
			j += i + len(managedEnd)
			return existing[:i] + strings.TrimSuffix(payload, "\n") + existing[j:]
		}
	}
	trimmed := strings.TrimRight(existing, " \t\r\n")
	if trimmed == "" {
		return payload
	}
	return trimmed + "\n\n" + payload
}
