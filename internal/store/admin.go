package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Health is the result of a self check.
type Health struct {
	SchemaVersion   int      `json:"schema_version"`
	ExpectedVersion int      `json:"expected_version"`
	Integrity       string   `json:"integrity"`
	FTSCheck        string   `json:"fts_check"`
	Memories        int      `json:"memories"`
	FTSRows         int      `json:"fts_rows"`
	MissingFTS      int      `json:"missing_fts"`
	OrphanFTS       int      `json:"orphan_fts"`
	StaleFTS        int      `json:"stale_fts"`
	Problems        []string `json:"problems,omitempty"`
	Consistent      bool     `json:"consistent"`
}

// Health runs SQLite integrity checks and verifies that the FTS index matches
// the memories table, including the transformed text of every row.
func (s *Store) Health() (*Health, error) {
	h := &Health{ExpectedVersion: schemaVersion, Problems: []string{}}
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&h.SchemaVersion); err != nil {
		return nil, err
	}
	if h.SchemaVersion > schemaVersion {
		h.Problems = append(h.Problems,
			fmt.Sprintf("database schema v%d is newer than this binary (v%d); upgrade aimemo", h.SchemaVersion, schemaVersion))
	}
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&h.Integrity); err != nil {
		return nil, err
	}
	if h.Integrity != "ok" {
		h.Problems = append(h.Problems, "integrity_check failed: "+h.Integrity)
	}
	if _, err := s.db.Exec(`INSERT INTO memories_fts(memories_fts) VALUES('integrity-check')`); err != nil {
		h.FTSCheck = err.Error()
		h.Problems = append(h.Problems, "fts integrity-check failed: "+h.FTSCheck)
	} else {
		h.FTSCheck = "ok"
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&h.Memories); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memories_fts`).Scan(&h.FTSRows); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM memories m
		WHERE NOT EXISTS (SELECT 1 FROM memories_fts f WHERE f.rowid = m.id)`).Scan(&h.MissingFTS); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM memories_fts f
		WHERE NOT EXISTS (SELECT 1 FROM memories m WHERE m.id = f.rowid)`).Scan(&h.OrphanFTS); err != nil {
		return nil, err
	}
	stale, err := s.staleFTSCount()
	if err != nil {
		return nil, err
	}
	h.StaleFTS = stale
	if h.MissingFTS > 0 {
		h.Problems = append(h.Problems, fmt.Sprintf("%d memories are missing from the FTS index", h.MissingFTS))
	}
	if h.OrphanFTS > 0 {
		h.Problems = append(h.Problems, fmt.Sprintf("%d FTS rows have no memory", h.OrphanFTS))
	}
	if h.StaleFTS > 0 {
		h.Problems = append(h.Problems, fmt.Sprintf("%d FTS rows have stale text", h.StaleFTS))
	}
	h.Consistent = h.SchemaVersion <= schemaVersion &&
		h.Integrity == "ok" && h.FTSCheck == "ok" &&
		h.Memories == h.FTSRows && h.MissingFTS == 0 && h.OrphanFTS == 0 && h.StaleFTS == 0
	return h, nil
}

// staleFTSCount compares the stored index text with what indexFTS would write.
// It streams rows so the check does not hold the whole table in memory.
func (s *Store) staleFTSCount() (int, error) {
	rows, err := s.db.Query(`
		SELECT m.title, m.content, m.problem, m.solution, m.pitfall, m.example, f.title, f.content
		FROM memories m JOIN memories_fts f ON f.rowid = m.id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	stale := 0
	for rows.Next() {
		var (
			title, content, problem, solution, pitfall, example string
			ftsTitle, ftsContent                                string
		)
		if err := rows.Scan(&title, &content, &problem, &solution, &pitfall, &example,
			&ftsTitle, &ftsContent); err != nil {
			return 0, err
		}
		want := structuredBody(content, problem, solution, pitfall, example)
		if ftsTitle != TransformCJK(title) || ftsContent != TransformCJK(want) {
			stale++
		}
	}
	return stale, rows.Err()
}

// RebuildFTS regenerates the whole FTS index from the memories table and
// returns the number of indexed rows.
func (s *Store) RebuildFTS() (int, error) {
	type row struct {
		id    int64
		title string
		body  string
	}
	rows, err := s.db.Query(`SELECT id, title, content, problem, solution, pitfall, example FROM memories ORDER BY id`)
	if err != nil {
		return 0, err
	}
	var all []row
	for rows.Next() {
		var (
			r                                            row
			content, problem, solution, pitfall, example string
		)
		if err := rows.Scan(&r.id, &r.title, &content, &problem, &solution, &pitfall, &example); err != nil {
			rows.Close()
			return 0, err
		}
		r.body = structuredBody(content, problem, solution, pitfall, example)
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	err = s.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM memories_fts`); err != nil {
			return err
		}
		for _, r := range all {
			tags, err := tagsOf(tx, r.id)
			if err != nil {
				return err
			}
			if err := indexFTS(tx, r.id, r.title, r.body, tags); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(all), nil
}

// Backup writes a consistent snapshot with VACUUM INTO. The target must not
// exist so a typo cannot overwrite a previous backup.
func (s *Store) Backup(path string) error {
	if filepath.Clean(path) == "" || path == "" {
		return fmt.Errorf("%w: backup path must not be empty", ErrInvalid)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: backup target %q already exists", ErrInvalid, path)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(`VACUUM INTO ?`, filepath.ToSlash(path))
	return err
}

// PruneOptions controls what gets removed.
type PruneOptions struct {
	Workspace  string
	Expired    bool
	Superseded bool
	OlderThan  string
	DryRun     bool
}

// PruneResult is the summary.
type PruneResult struct {
	Deleted int     `json:"deleted"`
	DryRun  bool    `json:"dry_run"`
	Ids     []int64 `json:"ids,omitempty"`
}

// Prune removes memories.
func (s *Store) Prune(o PruneOptions) (*PruneResult, error) {
	now := Now()
	var ids []int64
	q := `SELECT id FROM memories WHERE workspace = ?`
	args := []any{o.Workspace}
	cond := false
	if o.Expired {
		q += ` AND valid_until != '' AND valid_until < ?`
		args = append(args, now)
		cond = true
	}
	if o.Superseded {
		q += ` AND valid_until != ''`
		cond = true
	}
	if o.OlderThan != "" {
		d, err := parseAge(o.OlderThan)
		if err != nil {
			return nil, err
		}
		q += ` AND updated_at < ?`
		args = append(args, time.Now().UTC().Add(-d).Format("2006-01-02T15:04:05.000000Z"))
		cond = true
	}
	if !cond {
		return &PruneResult{DryRun: o.DryRun, Ids: []int64{}}, nil
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if o.DryRun {
		return &PruneResult{Deleted: 0, DryRun: true, Ids: ids}, nil
	}
	res := &PruneResult{Ids: ids}
	for _, id := range ids {
		if err := s.Delete(id, o.Workspace); err == nil {
			res.Deleted++
		}
	}
	return res, nil
}

// parseAge accepts a Go duration or a day suffix such as 90d.
func parseAge(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if strings.HasSuffix(v, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(v, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("%w: invalid age %q", ErrInvalid, v)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%w: invalid age %q", ErrInvalid, v)
	}
	return d, nil
}

// ExportJSON exports all memories in a workspace.
func (s *Store) ExportJSON(workspace string) ([]Memory, error) {
	return s.exportJSON(`WHERE m.workspace = ?`, workspace)
}

// ExportJSONAll exports memories from every workspace.
func (s *Store) ExportJSONAll() ([]Memory, error) {
	return s.exportJSON(``)
}

func (s *Store) exportJSON(where string, args ...any) ([]Memory, error) {
	rows, err := s.db.Query(`SELECT `+selectMemoryColumns+` FROM memories m JOIN categories c ON c.id = m.category_id `+where+` ORDER BY m.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		var catID int64
		var workspace, problem, solution, pitfall, example, scope, source, validUntil, supersedes, lastUsed, createdAt, updatedAt string
		var confidence, useCount, helpfulCount, wrongCount int
		if err := rows.Scan(&m.ID, &catID, &m.Category, &m.Type, &m.Importance, &m.Title, &m.Content, &problem, &solution, &pitfall, &example, &scope, &source, &workspace, &confidence, &validUntil, &supersedes, &lastUsed, &useCount, &helpfulCount, &wrongCount, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		m.CategoryID = catID
		m.Problem = problem
		m.Solution = solution
		m.Pitfall = pitfall
		m.Example = example
		m.Scope = scope
		m.Source = source
		m.Workspace = workspace
		m.Confidence = confidence
		m.ValidUntil = validUntil
		n, _ := strconv.ParseInt(supersedes, 10, 64)
		m.Supersedes = n
		m.LastUsedAt = lastUsed
		m.UseCount = useCount
		m.HelpfulCount = helpfulCount
		m.WrongCount = wrongCount
		m.CreatedAt = createdAt
		m.UpdatedAt = updatedAt
		tags, err := tagsOf(s.db, m.ID)
		if err != nil {
			return nil, err
		}
		m.Tags = tags
		out = append(out, m)
	}
	return out, rows.Err()
}

// TagStats returns tag counts.
func (s *Store) TagStats(workspace string) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT t.tag, COUNT(*) FROM memory_tags t JOIN memories m ON m.id = t.memory_id WHERE m.workspace = ? GROUP BY t.tag`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var tag string
		var n int
		if err := rows.Scan(&tag, &n); err != nil {
			return nil, err
		}
		out[tag] = n
	}
	return out, rows.Err()
}

// AddTags adds tags to a memory.
func (s *Store) AddTags(id int64, workspace string, tags []string) (*Memory, error) {
	var out *Memory
	err := s.withTx(func(tx *sql.Tx) error {
		if _, err := getMemory(tx, id, workspace); err != nil {
			return err
		}
		now := Now()
		for _, t := range tags {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO memory_tags(memory_id, tag, created_at) VALUES(?,?,?)`, id, t, now); err != nil {
				return err
			}
		}
		tags, err := tagsOf(tx, id)
		if err != nil {
			return err
		}
		m2, err := getMemory(tx, id, workspace)
		if err != nil {
			return err
		}
		if err := indexFTS(tx, id, m2.Title, structuredBody(m2.Content, m2.Problem, m2.Solution, m2.Pitfall, m2.Example), tags); err != nil {
			return err
		}
		m, err := getMemory(tx, id, workspace)
		if err != nil {
			return err
		}
		out = m
		return nil
	})
	return out, err
}

// RemoveTags removes tags from a memory.
func (s *Store) RemoveTags(id int64, workspace string, tags []string) (*Memory, error) {
	var out *Memory
	err := s.withTx(func(tx *sql.Tx) error {
		if _, err := getMemory(tx, id, workspace); err != nil {
			return err
		}
		for _, t := range tags {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			if _, err := tx.Exec(`DELETE FROM memory_tags WHERE memory_id = ? AND tag = ?`, id, t); err != nil {
				return err
			}
		}
		tags, err := tagsOf(tx, id)
		if err != nil {
			return err
		}
		m2, err := getMemory(tx, id, workspace)
		if err != nil {
			return err
		}
		if err := indexFTS(tx, id, m2.Title, structuredBody(m2.Content, m2.Problem, m2.Solution, m2.Pitfall, m2.Example), tags); err != nil {
			return err
		}
		m, err := getMemory(tx, id, workspace)
		if err != nil {
			return err
		}
		out = m
		return nil
	})
	return out, err
}

// Workspaces lists all workspaces.
func (s *Store) Workspaces() ([]map[string]any, error) {
	rows, err := s.db.Query(`SELECT workspace, COUNT(*), MAX(updated_at) FROM memories GROUP BY workspace ORDER BY workspace`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var ws string
		var cnt int
		var last string
		if err := rows.Scan(&ws, &cnt, &last); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"workspace": ws, "count": cnt, "last_updated_at": last})
	}
	return out, rows.Err()
}
