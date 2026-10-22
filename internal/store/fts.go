package store

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode"
)

// IsCJK reports whether r is a CJK-style letter that must be tokenized
// character by character for substring search to work.
func IsCJK(r rune) bool {
	return r > 0x2E80 && unicode.IsLetter(r)
}

// TransformCJK pads every CJK letter with spaces on BOTH sides.
//
// The unicode61 tokenizer treats a whole Chinese sentence as a single token,
// so "数据库" would never match inside "使用数据库存储". Padding splits the
// sentence into per-character tokens. Both sides are required: padding only
// after the character would fuse "Redis作" into one token and break searches
// for "redis" in mixed CJK/Latin text.
//
// The padding is collapsed back to single spaces so that the transformed text
// is a clean token sequence and matches what BuildMatch produces.
func TransformCJK(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 2)
	for _, r := range s {
		if IsCJK(r) {
			b.WriteRune(' ')
			b.WriteRune(r)
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// segment is a run of either CJK or non-CJK characters.
type segment struct {
	text string
	cjk  bool
}

// splitSegments splits s into alternating CJK / non-CJK runs.
func splitSegments(s string) []segment {
	runes := []rune(s)
	if len(runes) == 0 {
		return nil
	}
	var segs []segment
	cur, start := -1, 0
	for i, r := range runes {
		k := 0
		if IsCJK(r) {
			k = 1
		}
		if cur == -1 {
			cur, start = k, i
			continue
		}
		if k != cur {
			segs = append(segs, segment{text: string(runes[start:i]), cjk: cur == 1})
			cur, start = k, i
		}
	}
	segs = append(segs, segment{text: string(runes[start:]), cjk: cur == 1})
	return segs
}

// hasSearchableToken reports whether s contains at least one letter or digit.
func hasSearchableToken(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// phrase wraps s as an FTS5 quoted phrase with quotes escaped by doubling.
func phrase(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// BuildMatch converts a user query into an FTS5 MATCH expression.
//
// The raw user text is never handed to SQLite: every fragment is escaped and
// wrapped, because an unescaped quote makes FTS5 fail with "unterminated
// string".
//
// When loose is false a CJK run becomes a phrase, which FTS5 matches only at
// consecutive positions, i.e. an exact substring. When loose is true each CJK
// character becomes its own term joined by AND, which widens recall.
func BuildMatch(query string, loose bool) (string, error) {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty query")
	}
	var terms []string
	for _, field := range fields {
		for _, seg := range splitSegments(field) {
			if !hasSearchableToken(seg.text) {
				continue
			}
			if seg.cjk {
				if loose {
					for _, r := range seg.text {
						terms = append(terms, phrase(string(r)))
					}
					continue
				}
				terms = append(terms, phrase(TransformCJK(seg.text)))
				continue
			}
			terms = append(terms, phrase(seg.text))
		}
	}
	if len(terms) == 0 {
		return "", fmt.Errorf("query contains no searchable terms")
	}
	return strings.Join(terms, " AND "), nil
}

// queryFragments returns the raw fragments of a query, used to locate a match
// in the original unpadded text.
func queryFragments(query string) []string {
	var out []string
	for _, field := range strings.Fields(query) {
		for _, seg := range splitSegments(field) {
			if seg.text != "" {
				out = append(out, seg.text)
			}
		}
	}
	return out
}

// indexFTS writes or replaces the FTS row for a memory. It must be called
// inside the same transaction as the main table write.
func indexFTS(tx *sql.Tx, id int64, title, content string, tags []string) error {
	if _, err := tx.Exec(`DELETE FROM memories_fts WHERE rowid = ?`, id); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO memories_fts(rowid, title, content, tags) VALUES(?,?,?,?)`,
		id,
		TransformCJK(title),
		TransformCJK(content),
		TransformCJK(strings.Join(tags, " ")),
	)
	return err
}
