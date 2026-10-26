package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Category is one AI-defined category.
type Category struct {
	Name        string `json:"name"`
	MemoryCount int    `json:"memory_count"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// ensureCategory returns the id of the category with the given name, creating
// it when it does not exist. Name matching is case-insensitive.
//
// An existing category has its updated_at refreshed, because adding a memory
// to it is a write to the category.
func ensureCategory(tx *sql.Tx, name string) (int64, error) {
	name = strings.TrimSpace(name)
	var id int64
	err := tx.QueryRow(`SELECT id FROM categories WHERE name = ? COLLATE NOCASE`, name).Scan(&id)
	if err == nil {
		if _, err := tx.Exec(`UPDATE categories SET updated_at = ? WHERE id = ?`, Now(), id); err != nil {
			return 0, err
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	now := Now()
	res, err := tx.Exec(`INSERT INTO categories(name, created_at, updated_at) VALUES(?,?,?)`, name, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListCategories returns the categories used in a workspace with their memory
// counts. Categories that only exist in other workspaces are hidden.
func (s *Store) ListCategories(sort, workspace string) ([]Category, error) {
	order := `c.name COLLATE NOCASE ASC`
	switch sort {
	case "count":
		order = `COUNT(m.id) DESC, c.name COLLATE NOCASE ASC`
	case "created_at":
		order = `c.created_at DESC, c.id DESC`
	}
	rows, err := s.db.Query(`
		SELECT c.name, COUNT(m.id), c.created_at, c.updated_at
		FROM categories c
		JOIN memories m ON m.category_id = c.id
		WHERE m.workspace = ?
		GROUP BY c.id
		ORDER BY `+order, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.Name, &c.MemoryCount, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Stats summarises the stored memories.
type Stats struct {
	Total             int            `json:"total"`
	CategoryCount     int            `json:"category_count"`
	ByCategory        map[string]int `json:"by_category"`
	ByType            map[string]int `json:"by_type"`
	ByScope           map[string]int `json:"by_scope"`
	AverageImportance float64        `json:"average_importance"`
}

// Stats returns a summary of one workspace.
func (s *Store) Stats(workspace string) (*Stats, error) {
	out := &Stats{
		ByCategory: map[string]int{},
		ByType:     map[string]int{},
		ByScope:    map[string]int{},
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE workspace = ?`, workspace).Scan(&out.Total); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT category_id) FROM memories WHERE workspace = ?`, workspace).Scan(&out.CategoryCount); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT COALESCE(AVG(importance), 0) FROM memories WHERE workspace = ?`, workspace).Scan(&out.AverageImportance); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`SELECT c.name, COUNT(m.id) FROM categories c JOIN memories m ON m.category_id = c.id WHERE m.workspace = ? GROUP BY c.id`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out.ByCategory[name] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	trows, err := s.db.Query(`SELECT type, COUNT(*) FROM memories WHERE workspace = ? GROUP BY type`, workspace)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	for trows.Next() {
		var name string
		var n int
		if err := trows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out.ByType[name] = n
	}
	if err := trows.Err(); err != nil {
		return nil, err
	}

	srows, err := s.db.Query(`SELECT scope, COUNT(*) FROM memories WHERE workspace = ? GROUP BY scope`, workspace)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var name string
		var n int
		if err := srows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out.ByScope[name] = n
	}
	return out, srows.Err()
}
