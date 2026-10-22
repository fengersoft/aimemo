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

// ListCategories returns every category together with its memory count.
func (s *Store) ListCategories(sort string) ([]Category, error) {
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
		LEFT JOIN memories m ON m.category_id = c.id
		GROUP BY c.id
		ORDER BY ` + order)
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
