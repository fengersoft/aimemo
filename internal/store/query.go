package store

import (
	"database/sql"
	"strings"
)

// Filter narrows a listing or a search. Repeated values inside one field are
// combined with OR.
type Filter struct {
	Categories     []string
	Scopes         []string
	Types          []string
	Tags           []string
	Workspace      string
	AllWorkspaces  bool
	MinImportance  int
	MinConfidence  int
	IncludeExpired bool
	Since          string
	Until          string
}

// clauses renders the filter as a SQL predicate plus its bind arguments.
func (f Filter) clauses() (string, []any) {
	var cs []string
	var args []any

	if len(f.Categories) > 0 {
		ph := make([]string, 0, len(f.Categories))
		for _, c := range f.Categories {
			ph = append(ph, "c.name = ? COLLATE NOCASE")
			args = append(args, c)
		}
		cs = append(cs, "("+strings.Join(ph, " OR ")+")")
	}
	if len(f.Scopes) > 0 {
		ph := make([]string, 0, len(f.Scopes))
		for _, sc := range f.Scopes {
			ph = append(ph, "m.scope = ?")
			args = append(args, sc)
		}
		cs = append(cs, "("+strings.Join(ph, " OR ")+")")
	}
	if len(f.Types) > 0 {
		ph := make([]string, 0, len(f.Types))
		for _, t := range f.Types {
			ph = append(ph, "m.type = ?")
			args = append(args, t)
		}
		cs = append(cs, "("+strings.Join(ph, " OR ")+")")
	}
	if len(f.Tags) > 0 {
		ph := make([]string, 0, len(f.Tags))
		for _, t := range f.Tags {
			ph = append(ph, "?")
			args = append(args, t)
		}
		cs = append(cs, "m.id IN (SELECT memory_id FROM memory_tags WHERE tag IN ("+strings.Join(ph, ",")+"))")
	}
	if f.MinImportance > 0 {
		cs = append(cs, "m.importance >= ?")
		args = append(args, f.MinImportance)
	}
	if f.MinConfidence > 0 {
		cs = append(cs, "m.confidence >= ?")
		args = append(args, f.MinConfidence)
	}
	// The empty workspace is itself a namespace, so the default listing only
	// shows default-workspace memories. AllWorkspaces bypasses this filter.
	if !f.AllWorkspaces {
		cs = append(cs, "m.workspace = ?")
		args = append(args, f.Workspace)
	}
	if !f.IncludeExpired {
		cs = append(cs, "(m.valid_until = '' OR m.valid_until >= ?)")
		args = append(args, Now())
	}
	if f.Since != "" {
		cs = append(cs, "m.created_at >= ?")
		args = append(args, f.Since)
	}
	if f.Until != "" {
		cs = append(cs, "m.created_at <= ?")
		args = append(args, f.Until)
	}
	if len(cs) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(cs, " AND "), args
}

// orderBy renders the ORDER BY clause. relevance only applies to a search,
// because bm25 needs the FTS table in the query.
func orderBy(sort string, searchable bool) string {
	switch sort {
	case "importance":
		return "ORDER BY m.importance DESC, m.created_at DESC, m.id DESC"
	case "confidence":
		return "ORDER BY m.confidence DESC, m.importance DESC, m.created_at DESC, m.id DESC"
	case "helpful":
		return "ORDER BY m.helpful_count DESC, m.use_count DESC, m.importance DESC, m.created_at DESC, m.id DESC"
	case "relevance":
		if searchable {
			return "ORDER BY bm25(memories_fts), m.created_at DESC, m.id DESC"
		}
	case "created_at", "":
	}
	return "ORDER BY m.created_at DESC, m.id DESC"
}

// attachTags loads the tags of many records in one query.
func attachTags(q querier, mems []Memory) error {
	if len(mems) == 0 {
		return nil
	}
	ph := make([]string, 0, len(mems))
	args := make([]any, 0, len(mems))
	byID := make(map[int64]*Memory, len(mems))
	for i := range mems {
		mems[i].Tags = []string{}
		ph = append(ph, "?")
		args = append(args, mems[i].ID)
		byID[mems[i].ID] = &mems[i]
	}
	rows, err := q.Query(
		`SELECT memory_id, tag FROM memory_tags WHERE memory_id IN (`+strings.Join(ph, ",")+`) ORDER BY tag`,
		args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var tag string
		if err := rows.Scan(&id, &tag); err != nil {
			return err
		}
		if m, ok := byID[id]; ok {
			m.Tags = append(m.Tags, tag)
		}
	}
	return rows.Err()
}

// scanMemories reads the standard memory column set.
func scanMemories(rows *sql.Rows) ([]Memory, error) {
	defer rows.Close()
	out := []Memory{}
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.CategoryID, &m.Category, &m.Type, &m.Importance,
			&m.Title, &m.Content, &m.Problem, &m.Solution, &m.Pitfall, &m.Example,
			&m.Scope, &m.Source, &m.Workspace, &m.Confidence, &m.ValidUntil, &m.Supersedes,
			&m.LastUsedAt, &m.UseCount, &m.HelpfulCount, &m.WrongCount,
			&m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
