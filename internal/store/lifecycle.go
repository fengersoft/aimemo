package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// ParseDeadline converts an optional YYYY-MM-DD or RFC3339 deadline into the
// canonical UTC timestamp. An empty value stays empty.
func ParseDeadline(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if len(v) == 10 && v[4] == '-' && v[7] == '-' {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return "", fmt.Errorf("%w: invalid date %q", ErrInvalid, v)
		}
		return t.Add(24*time.Hour - time.Millisecond).UTC().Format("2006-01-02T15:04:05.000000Z"), nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return "", fmt.Errorf("%w: invalid timestamp %q, expected RFC3339 or YYYY-MM-DD", ErrInvalid, v)
	}
	return t.UTC().Format("2006-01-02T15:04:05.000000Z"), nil
}

// normalizeTitle strips everything but letters and digits so that near
// duplicates such as "Error Wrap!" and "error-wrap" collapse together.
func normalizeTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// FindSimilar returns recent memories whose normalized title equals or
// contains the given title. It backs duplicate detection on create.
func (s *Store) FindSimilar(title string, excludeID int64, workspace string) ([]Memory, error) {
	norm := normalizeTitle(title)
	if len([]rune(norm)) < 4 {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT ` + selectMemoryColumns + `
		FROM memories m JOIN categories c ON c.id = m.category_id
		ORDER BY m.id DESC LIMIT 1000`)
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

	out := []Memory{}
	for _, m := range items {
		if m.ID == excludeID {
			continue
		}
		if m.Workspace != workspace {
			continue
		}
		other := normalizeTitle(m.Title)
		if other == "" {
			continue
		}
		similar := other == norm
		if !similar && len([]rune(norm)) >= 6 {
			similar = strings.Contains(other, norm) || strings.Contains(norm, other)
		}
		if similar {
			out = append(out, m)
			if len(out) == 3 {
				break
			}
		}
	}
	return out, nil
}

// MarkUsed records that a memory was retrieved and used.
func (s *Store) MarkUsed(id int64, workspace string) (*Memory, error) {
	now := Now()
	res, err := s.db.Exec(
		`UPDATE memories SET last_used_at = ?, use_count = use_count + 1 WHERE id = ? AND workspace = ?`,
		now, id, workspace)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	return s.Get(id, workspace)
}

// Feedback records whether a memory was helpful or wrong.
func (s *Store) Feedback(id int64, workspace string, helpful bool) (*Memory, error) {
	column := "helpful_count"
	if !helpful {
		column = "wrong_count"
	}
	res, err := s.db.Exec(
		`UPDATE memories SET `+column+` = `+column+` + 1 WHERE id = ? AND workspace = ?`, id, workspace)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	return s.Get(id, workspace)
}

// Supersede marks oldID as replaced by newID by expiring the old record.
func (s *Store) Supersede(oldID, newID int64, workspace string) error {
	return s.withTx(func(tx *sql.Tx) error {
		return expireMemory(tx, oldID, newID, workspace)
	})
}

// expireMemory sets valid_until on oldID inside an existing transaction. It
// also proves that newID exists in the same workspace, so a bad supersedes
// target rolls the whole write back.
func expireMemory(tx *sql.Tx, oldID, newID int64, workspace string) error {
	if oldID == newID {
		return fmt.Errorf("%w: a memory cannot supersede itself", ErrInvalid)
	}
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM memories WHERE id = ? AND workspace = ?`,
		newID, workspace).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	now := Now()
	res, err := tx.Exec(`
		UPDATE memories
		SET valid_until = ?, updated_at = ?
		WHERE id = ? AND workspace = ?`, now, now, oldID, workspace)
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
	return nil
}

// GetByID is a small helper used by import.
func (s *Store) GetByID(id int64, workspace string) (*Memory, error) { return s.Get(id, workspace) }

// sqlErrNoRows exposes sql.ErrNoRows for callers that wrap it.
var sqlErrNoRows = sql.ErrNoRows

// IsNotFound reports whether err means a missing record.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) || errors.Is(err, sqlErrNoRows) }

// FindByTitle returns the memory whose normalized title matches exactly inside
// a workspace. It backs --overwrite during imports.
func (s *Store) FindByTitle(title, workspace string) (*Memory, error) {
	norm := normalizeTitle(title)
	if norm == "" {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(`
		SELECT `+selectMemoryColumns+`
		FROM memories m JOIN categories c ON c.id = m.category_id
		WHERE m.workspace = ?
		ORDER BY m.id DESC LIMIT 1000`, workspace)
	if err != nil {
		return nil, err
	}
	items, err := scanMemories(rows)
	if err != nil {
		return nil, err
	}
	for _, m := range items {
		if normalizeTitle(m.Title) == norm {
			if err := attachTags(s.db, []Memory{m}); err != nil {
				return nil, err
			}
			return &m, nil
		}
	}
	return nil, ErrNotFound
}
