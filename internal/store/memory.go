package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalid marks caller input that failed validation.
var ErrInvalid = errors.New("invalid input")

// ErrNotFound marks a missing record.
var ErrNotFound = errors.New("not found")

// Field limits.
const (
	MaxContentLen   = 100000
	MaxTitleLen     = 500
	MaxCategoryLen  = 100
	MaxTagLen       = 50
	MaxTags         = 20
	MaxScopeLen     = 200
	MaxSourceLen    = 200
	MaxWorkspaceLen = 100
)

// Valid memory types.
const (
	TypeSummary   = "summary"
	TypeImportant = "important"
	TypeNote      = "note"
)

func validType(t string) bool {
	return t == TypeSummary || t == TypeImportant || t == TypeNote
}

// Memory is one stored record.
type Memory struct {
	ID           int64    `json:"id"`
	CategoryID   int64    `json:"-"`
	Category     string   `json:"category"`
	Type         string   `json:"type"`
	Importance   int      `json:"importance"`
	Title        string   `json:"title"`
	Content      string   `json:"content"`
	Problem      string   `json:"problem,omitempty"`
	Solution     string   `json:"solution,omitempty"`
	Pitfall      string   `json:"pitfall,omitempty"`
	Example      string   `json:"example,omitempty"`
	Scope        string   `json:"scope"`
	Source       string   `json:"source"`
	Workspace    string   `json:"workspace,omitempty"`
	Confidence   int      `json:"confidence"`
	ValidUntil   string   `json:"valid_until,omitempty"`
	Supersedes   int64    `json:"supersedes,omitempty"`
	LastUsedAt   string   `json:"last_used_at,omitempty"`
	UseCount     int      `json:"use_count"`
	HelpfulCount int      `json:"helpful_count"`
	WrongCount   int      `json:"wrong_count"`
	Tags         []string `json:"tags"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	MatchContext string   `json:"match_context,omitempty"`
}

// CreateInput carries the fields of a new record.
type CreateInput struct {
	Category   string
	Type       string
	Importance int
	Title      string
	Content    string
	Problem    string
	Solution   string
	Pitfall    string
	Example    string
	Scope      string
	Source     string
	Workspace  string
	Confidence int
	ValidUntil string
	Supersedes int64
	Tags       []string
	CreatedAt  string
	UpdatedAt  string
}

// withDefaults fills in the values a caller may leave unset.
func (in CreateInput) withDefaults() CreateInput {
	if in.Type == "" {
		in.Type = TypeNote
	}
	if in.Importance == 0 {
		in.Importance = 3
	}
	if in.Confidence == 0 {
		in.Confidence = 3
	}
	if in.CreatedAt == "" {
		in.CreatedAt = Now()
	}
	if in.UpdatedAt == "" {
		in.UpdatedAt = in.CreatedAt
	}
	return in
}

// canonicalTime is the layout used by every stored timestamp.
const canonicalTime = "2006-01-02T15:04:05.000000Z"

// validTimestamp reports whether ts is empty or a canonical timestamp.
func validTimestamp(ts string) bool {
	if ts == "" {
		return true
	}
	_, err := time.Parse(canonicalTime, ts)
	return err == nil
}

// structuredBody joins the optional structured fields for FTS indexing.
func structuredBody(content, problem, solution, pitfall, example string) string {
	parts := make([]string, 0, 5)
	for _, p := range []string{content, problem, solution, pitfall, example} {
		if strings.TrimSpace(p) != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n\n")
}

func (in CreateInput) validate() error {
	if strings.TrimSpace(in.Category) == "" {
		return fmt.Errorf("%w: category must not be empty", ErrInvalid)
	}
	if len([]rune(in.Category)) > MaxCategoryLen {
		return fmt.Errorf("%w: category exceeds %d characters", ErrInvalid, MaxCategoryLen)
	}
	if !validType(in.Type) {
		return fmt.Errorf("%w: type must be one of summary, important, note", ErrInvalid)
	}
	if in.Importance < 1 || in.Importance > 5 {
		return fmt.Errorf("%w: importance must be between 1 and 5", ErrInvalid)
	}
	if strings.TrimSpace(in.Content) == "" {
		return fmt.Errorf("%w: content must not be empty", ErrInvalid)
	}
	if len([]rune(in.Content)) > MaxContentLen {
		return fmt.Errorf("%w: content exceeds %d characters", ErrInvalid, MaxContentLen)
	}
	if len([]rune(in.Title)) > MaxTitleLen {
		return fmt.Errorf("%w: title exceeds %d characters", ErrInvalid, MaxTitleLen)
	}
	if len([]rune(in.Scope)) > MaxScopeLen {
		return fmt.Errorf("%w: scope exceeds %d characters", ErrInvalid, MaxScopeLen)
	}
	if len([]rune(in.Source)) > MaxSourceLen {
		return fmt.Errorf("%w: source exceeds %d characters", ErrInvalid, MaxSourceLen)
	}
	if len([]rune(in.Workspace)) > MaxWorkspaceLen {
		return fmt.Errorf("%w: workspace exceeds %d characters", ErrInvalid, MaxWorkspaceLen)
	}
	if in.Confidence < 1 || in.Confidence > 5 {
		return fmt.Errorf("%w: confidence must be between 1 and 5", ErrInvalid)
	}
	if len([]rune(in.Problem)) > MaxContentLen || len([]rune(in.Solution)) > MaxContentLen ||
		len([]rune(in.Pitfall)) > MaxContentLen || len([]rune(in.Example)) > MaxContentLen {
		return fmt.Errorf("%w: structured fields exceed %d characters", ErrInvalid, MaxContentLen)
	}
	if _, err := ParseDeadline(in.ValidUntil); err != nil {
		return err
	}
	if in.Supersedes < 0 {
		return fmt.Errorf("%w: supersedes must not be negative", ErrInvalid)
	}
	if !validTimestamp(in.CreatedAt) || !validTimestamp(in.UpdatedAt) {
		return fmt.Errorf("%w: created_at and updated_at must be RFC3339 timestamps", ErrInvalid)
	}
	if len(in.Tags) > MaxTags {
		return fmt.Errorf("%w: at most %d tags are allowed", ErrInvalid, MaxTags)
	}
	for _, t := range in.Tags {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("%w: tag must not be empty", ErrInvalid)
		}
		if len([]rune(t)) > MaxTagLen {
			return fmt.Errorf("%w: tag exceeds %d characters", ErrInvalid, MaxTagLen)
		}
	}
	return nil
}

// Create stores a new memory, creating its category when needed.
func (s *Store) Create(in CreateInput) (*Memory, error) {
	in = in.withDefaults()
	if err := in.validate(); err != nil {
		return nil, err
	}
	var out *Memory
	err := s.withTx(func(tx *sql.Tx) error {
		catID, err := ensureCategory(tx, in.Category)
		if err != nil {
			return err
		}
		now := Now()
		validUntil, err := ParseDeadline(in.ValidUntil)
		if err != nil {
			return err
		}
		res, err := tx.Exec(`
			INSERT INTO memories(category_id, type, importance, title, content,
				problem, solution, pitfall, example, scope, source, workspace,
				confidence, valid_until, supersedes, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			catID, in.Type, in.Importance, in.Title, in.Content,
			in.Problem, in.Solution, in.Pitfall, in.Example, in.Scope, in.Source, in.Workspace,
			in.Confidence, validUntil, in.Supersedes, in.CreatedAt, in.UpdatedAt)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if err := setTags(tx, id, in.Tags, now); err != nil {
			return err
		}
		body := structuredBody(in.Content, in.Problem, in.Solution, in.Pitfall, in.Example)
		if err := indexFTS(tx, id, in.Title, body, in.Tags); err != nil {
			return err
		}
		if err := indexFeatures(tx, id, in.Title+"\n"+body); err != nil {
			return err
		}
		if in.Supersedes > 0 {
			if err := expireMemory(tx, in.Supersedes, id, in.Workspace); err != nil {
				return err
			}
		}
		m, err := getMemory(tx, id, in.Workspace)
		if err != nil {
			return err
		}
		out = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Get returns one record by id inside a workspace.
func (s *Store) Get(id int64, workspace string) (*Memory, error) {
	m, err := getMemory(s.db, id, workspace)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// selectMemoryColumns is the standard column list used by every read.
const selectMemoryColumns = `m.id, m.category_id, c.name, m.type, m.importance, m.title, m.content, m.problem, m.solution, m.pitfall, m.example, m.scope, m.source, m.workspace, m.confidence, m.valid_until, m.supersedes, m.last_used_at, m.use_count, m.helpful_count, m.wrong_count, m.created_at, m.updated_at`

func getMemory(q querier, id int64, workspace string) (*Memory, error) {
	row := q.QueryRow(`
		SELECT `+selectMemoryColumns+`
		FROM memories m JOIN categories c ON c.id = m.category_id
		WHERE m.id = ? AND m.workspace = ?`, id, workspace)
	var m Memory
	if err := row.Scan(&m.ID, &m.CategoryID, &m.Category, &m.Type, &m.Importance, &m.Title,
		&m.Content, &m.Problem, &m.Solution, &m.Pitfall, &m.Example, &m.Scope, &m.Source,
		&m.Workspace, &m.Confidence, &m.ValidUntil, &m.Supersedes, &m.LastUsedAt,
		&m.UseCount, &m.HelpfulCount, &m.WrongCount, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	tags, err := tagsOf(q, m.ID)
	if err != nil {
		return nil, err
	}
	m.Tags = tags
	return &m, nil
}

func setTags(tx *sql.Tx, memoryID int64, tags []string, now string) error {
	if _, err := tx.Exec(`DELETE FROM memory_tags WHERE memory_id = ?`, memoryID); err != nil {
		return err
	}
	for _, t := range tags {
		if _, err := tx.Exec(
			`INSERT INTO memory_tags(memory_id, tag, created_at) VALUES(?,?,?)`,
			memoryID, t, now); err != nil {
			return err
		}
	}
	return nil
}

func tagsOf(q querier, memoryID int64) ([]string, error) {
	rows, err := q.Query(`SELECT tag FROM memory_tags WHERE memory_id = ? ORDER BY tag`, memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateInput carries the fields to change. A nil pointer means "keep".
type UpdateInput struct {
	Type       *string
	Importance *int
	Title      *string
	Content    *string
	Problem    *string
	Solution   *string
	Pitfall    *string
	Example    *string
	Category   *string
	Scope      *string
	Source     *string
	Workspace  *string
	Confidence *int
	ValidUntil *string
	Supersedes *int64
	SetTags    *[]string
	AddTags    []string
	RemoveTags []string
}

// Update changes a record in place. workspace selects the record, so a caller
// cannot modify a memory owned by another workspace.
func (s *Store) Update(id int64, workspace string, in UpdateInput) (*Memory, error) {
	var out *Memory
	err := s.withTx(func(tx *sql.Tx) error {
		cur, err := getMemory(tx, id, workspace)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		next := UpdateInput{
			Type:       &cur.Type,
			Importance: &cur.Importance,
			Title:      &cur.Title,
			Content:    &cur.Content,
			Problem:    &cur.Problem,
			Solution:   &cur.Solution,
			Pitfall:    &cur.Pitfall,
			Example:    &cur.Example,
			Scope:      &cur.Scope,
			Source:     &cur.Source,
			Workspace:  &cur.Workspace,
			Confidence: &cur.Confidence,
			ValidUntil: &cur.ValidUntil,
			Supersedes: &cur.Supersedes,
		}
		for _, f := range []struct {
			dst **string
			src *string
		}{
			{&next.Type, in.Type}, {&next.Title, in.Title}, {&next.Content, in.Content},
			{&next.Problem, in.Problem}, {&next.Solution, in.Solution},
			{&next.Pitfall, in.Pitfall}, {&next.Example, in.Example},
			{&next.Scope, in.Scope}, {&next.Source, in.Source},
			{&next.Workspace, in.Workspace}, {&next.ValidUntil, in.ValidUntil},
		} {
			if f.src != nil {
				*f.dst = f.src
			}
		}
		if in.Importance != nil {
			next.Importance = in.Importance
		}
		if in.Confidence != nil {
			next.Confidence = in.Confidence
		}
		if in.Supersedes != nil {
			next.Supersedes = in.Supersedes
		}

		catName := cur.Category
		if in.Category != nil {
			catName = *in.Category
		}
		probe := CreateInput{
			Category:   catName,
			Type:       *next.Type,
			Importance: *next.Importance,
			Title:      *next.Title,
			Content:    *next.Content,
			Problem:    *next.Problem,
			Solution:   *next.Solution,
			Pitfall:    *next.Pitfall,
			Example:    *next.Example,
			Scope:      *next.Scope,
			Source:     *next.Source,
			Workspace:  *next.Workspace,
			Confidence: *next.Confidence,
			ValidUntil: *next.ValidUntil,
			Supersedes: *next.Supersedes,
		}
		if err := probe.validate(); err != nil {
			return err
		}

		catID := cur.CategoryID
		if in.Category != nil {
			catID, err = ensureCategory(tx, *in.Category)
			if err != nil {
				return err
			}
		}

		now := Now()
		validUntil, err := ParseDeadline(*next.ValidUntil)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`
			UPDATE memories
			SET category_id=?, type=?, importance=?, title=?, content=?,
				problem=?, solution=?, pitfall=?, example=?, scope=?, source=?,
				workspace=?, confidence=?, valid_until=?, supersedes=?, updated_at=?
			WHERE id=? AND workspace=?`,
			catID, *next.Type, *next.Importance, *next.Title, *next.Content,
			*next.Problem, *next.Solution, *next.Pitfall, *next.Example,
			*next.Scope, *next.Source, *next.Workspace, *next.Confidence,
			validUntil, *next.Supersedes, now, id, workspace); err != nil {
			return err
		}

		tags := cur.Tags
		switch {
		case in.SetTags != nil:
			tags = append([]string{}, *in.SetTags...)
			seen := map[string]bool{}
			for _, t := range tags {
				if strings.TrimSpace(t) == "" {
					return fmt.Errorf("%w: tag must not be empty", ErrInvalid)
				}
				if len([]rune(t)) > MaxTagLen {
					return fmt.Errorf("%w: tag exceeds %d characters", ErrInvalid, MaxTagLen)
				}
				if seen[t] {
					return fmt.Errorf("%w: duplicate tag %q", ErrInvalid, t)
				}
				seen[t] = true
			}
			if len(tags) > MaxTags {
				return fmt.Errorf("%w: at most %d tags are allowed", ErrInvalid, MaxTags)
			}
			if err := setTags(tx, id, tags, now); err != nil {
				return err
			}
		case len(in.AddTags) > 0 || len(in.RemoveTags) > 0:
			remove := map[string]bool{}
			for _, t := range in.RemoveTags {
				remove[t] = true
			}
			kept := []string{}
			present := map[string]bool{}
			for _, t := range tags {
				if remove[t] {
					continue
				}
				kept = append(kept, t)
				present[t] = true
			}
			for _, t := range in.AddTags {
				if strings.TrimSpace(t) == "" {
					return fmt.Errorf("%w: tag must not be empty", ErrInvalid)
				}
				if len([]rune(t)) > MaxTagLen {
					return fmt.Errorf("%w: tag exceeds %d characters", ErrInvalid, MaxTagLen)
				}
				if !present[t] {
					kept = append(kept, t)
					present[t] = true
				}
			}
			if len(kept) > MaxTags {
				return fmt.Errorf("%w: at most %d tags are allowed", ErrInvalid, MaxTags)
			}
			tags = kept
			if err := setTags(tx, id, tags, now); err != nil {
				return err
			}
		}

		body := structuredBody(*next.Content, *next.Problem, *next.Solution, *next.Pitfall, *next.Example)
		if err := indexFTS(tx, id, *next.Title, body, tags); err != nil {
			return err
		}
		if err := indexFeatures(tx, id, *next.Title+"\n"+body); err != nil {
			return err
		}
		if in.Supersedes != nil && *in.Supersedes > 0 {
			if err := expireMemory(tx, *in.Supersedes, id, *next.Workspace); err != nil {
				return err
			}
		}
		m, err := getMemory(tx, id, *next.Workspace)
		if err != nil {
			return err
		}
		out = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Delete removes a record in a workspace, its tags and its FTS row.
func (s *Store) Delete(id int64, workspace string) error {
	return s.withTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM memories WHERE id = ? AND workspace = ?`, id, workspace)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(`DELETE FROM memories_fts WHERE rowid = ?`, id); err != nil {
			return err
		}
		return nil
	})
}

// ListResult is a page of memories.
type ListResult struct {
	Items  []Memory `json:"items"`
	Total  int      `json:"total"`
	Limit  int      `json:"limit"`
	Offset int      `json:"offset"`
}

// SearchResult is a page of matches for a keyword query.
type SearchResult struct {
	Items []Memory `json:"items"`
	Total int      `json:"total"`
	Query string   `json:"query"`
}

// List returns memories matching the filter, newest first by default.
func (s *Store) List(f Filter, limit, offset int, sort string) (*ListResult, error) {
	where, args := f.clauses()

	var total int
	if err := s.db.QueryRow(`
		SELECT COUNT(*)
		FROM memories m JOIN categories c ON c.id = m.category_id
		WHERE 1=1`+where, args...).Scan(&total); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`
		SELECT `+selectMemoryColumns+`
		FROM memories m JOIN categories c ON c.id = m.category_id
		WHERE 1=1`+where+` `+orderBy(sort, false)+` LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	items, err := scanMemories(rows)
	if err != nil {
		return nil, err
	}
	if err := attachTags(s.db, items); err != nil {
		return nil, err
	}
	return &ListResult{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

// Search performs a full text search and returns matching memories with a
// window of surrounding text.
func (s *Store) Search(query string, f Filter, limit, offset int, sort string, loose bool) (*SearchResult, error) {
	expr, err := BuildMatch(query, loose)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	where, args := f.clauses()
	args = append([]any{expr}, args...)

	var total int
	if err := s.db.QueryRow(`
		SELECT COUNT(*)
		FROM memories_fts
		JOIN memories m ON m.id = memories_fts.rowid
		JOIN categories c ON c.id = m.category_id
		WHERE memories_fts MATCH ?`+where, args...).Scan(&total); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`
		SELECT `+selectMemoryColumns+`
		FROM memories_fts
		JOIN memories m ON m.id = memories_fts.rowid
		JOIN categories c ON c.id = m.category_id
		WHERE memories_fts MATCH ?`+where+` `+orderBy(sort, true)+` LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	items, err := scanMemories(rows)
	if err != nil {
		return nil, err
	}
	if err := attachTags(s.db, items); err != nil {
		return nil, err
	}
	for i := range items {
		items[i].MatchContext = MatchContext(items[i].Content, query, 120)
	}
	return &SearchResult{Items: items, Total: total, Query: query}, nil
}

// SearchAny returns memories that match any fragment of query. The caller is
// expected to rank the results, so the store only applies the filter and a
// hard cap. This backs the context command, where a task description is
// expanded into many loosely related terms.
func (s *Store) SearchAny(query string, f Filter, limit int, loose bool) ([]Memory, error) {
	expr, err := BuildMatchOr(query, loose)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	where, args := f.clauses()
	args = append([]any{expr}, args...)

	rows, err := s.db.Query(`
		SELECT `+selectMemoryColumns+`
		FROM memories_fts
		JOIN memories m ON m.id = memories_fts.rowid
		JOIN categories c ON c.id = m.category_id
		WHERE memories_fts MATCH ?`+where+`
		ORDER BY m.importance DESC, m.created_at DESC, m.id DESC
		LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	items, err := scanMemories(rows)
	if err != nil {
		return nil, err
	}
	if err := attachTags(s.db, items); err != nil {
		return nil, err
	}
	return items, nil
}

// HybridSearchResult pairs a memory with its hybrid score.
type HybridSearchResult struct {
	Memory Memory  `json:"memory"`
	Score  float64 `json:"score"`
}

// SearchHybrid combines FTS and semantic search using Reciprocal Rank Fusion.
func (s *Store) SearchHybrid(query string, f Filter, limit int, alpha float64) ([]HybridSearchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	if alpha < 0 {
		alpha = 0
	}
	if alpha > 1 {
		alpha = 1
	}

	ftsRes, err := s.Search(query, f, limit*3, 0, "relevance", false)
	if err != nil {
		return nil, err
	}
	semRes, err := s.SearchSemantic(query, f, limit*3, 2000)
	if err != nil {
		return nil, err
	}

	rank := map[int64]float64{}
	// FTS ranks
	for i, m := range ftsRes.Items {
		r := 1.0 / (float64(i) + 60.0)
		rank[m.ID] += alpha * r
	}
	// Semantic ranks
	for i, sr := range semRes {
		r := 1.0 / (float64(i) + 60.0)
		rank[sr.Memory.ID] += (1.0 - alpha) * r
	}

	// collect all memories
	all := map[int64]Memory{}
	for _, m := range ftsRes.Items {
		all[m.ID] = m
	}
	for _, sr := range semRes {
		all[sr.Memory.ID] = sr.Memory
	}

	type scored struct {
		id    int64
		score float64
	}
	list := make([]scored, 0, len(rank))
	for id, sc := range rank {
		list = append(list, scored{id: id, score: sc})
	}
	// sort
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].score > list[i].score {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	if len(list) > limit {
		list = list[:limit]
	}

	out := make([]HybridSearchResult, 0, len(list))
	for _, sitem := range list {
		if m, ok := all[sitem.id]; ok {
			out = append(out, HybridSearchResult{Memory: m, Score: sitem.score})
		}
	}
	// attach tags
	memList := make([]Memory, 0, len(out))
	for _, o := range out {
		memList = append(memList, o.Memory)
	}
	if err := attachTags(s.db, memList); err != nil {
		return nil, err
	}
	for i := range out {
		for j := range memList {
			if memList[j].ID == out[i].Memory.ID {
				out[i].Memory.Tags = memList[j].Tags
				break
			}
		}
	}
	return out, nil
}
