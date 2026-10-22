package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalid marks caller input that failed validation.
var ErrInvalid = errors.New("invalid input")

// ErrNotFound marks a missing record.
var ErrNotFound = errors.New("not found")

// Field limits.
const (
	MaxContentLen  = 100000
	MaxTitleLen    = 500
	MaxCategoryLen = 100
	MaxTagLen      = 50
	MaxTags        = 20
	MaxScopeLen    = 200
	MaxSourceLen   = 200
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
	Scope        string   `json:"scope"`
	Source       string   `json:"source"`
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
	Scope      string
	Source     string
	Tags       []string
}

// withDefaults fills in the values a caller may leave unset.
func (in CreateInput) withDefaults() CreateInput {
	if in.Type == "" {
		in.Type = TypeNote
	}
	if in.Importance == 0 {
		in.Importance = 3
	}
	return in
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
		res, err := tx.Exec(`
			INSERT INTO memories(category_id, type, importance, title, content, scope, source, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?)`,
			catID, in.Type, in.Importance, in.Title, in.Content, in.Scope, in.Source, now, now)
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
		if err := indexFTS(tx, id, in.Title, in.Content, in.Tags); err != nil {
			return err
		}
		m, err := getMemory(tx, id)
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

// Get returns one record by id.
func (s *Store) Get(id int64) (*Memory, error) {
	m, err := getMemory(s.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// selectMemoryColumns is the standard column list used by every read.
const selectMemoryColumns = `m.id, m.category_id, c.name, m.type, m.importance, m.title, m.content, m.scope, m.source, m.created_at, m.updated_at`

func getMemory(q querier, id int64) (*Memory, error) {
	row := q.QueryRow(`
		SELECT `+selectMemoryColumns+`
		FROM memories m JOIN categories c ON c.id = m.category_id
		WHERE m.id = ?`, id)
	var m Memory
	if err := row.Scan(&m.ID, &m.CategoryID, &m.Category, &m.Type, &m.Importance, &m.Title,
		&m.Content, &m.Scope, &m.Source, &m.CreatedAt, &m.UpdatedAt); err != nil {
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
