package store

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
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
//
// NUL bytes are dropped. FTS5 tokenizes them as a separator, so keeping them
// in the index but stripping them from the query would make a document and an
// identical query disagree.
func TransformCJK(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 2)
	for _, r := range s {
		if r == 0 {
			continue
		}
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
//
// NUL bytes are removed: FTS5 parses the quoted text as a C string, so a NUL
// inside a phrase truncates it and the statement fails with "unterminated
// string". That is the very failure this builder exists to prevent.
func phrase(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// MaxQueryRunes caps the length of a search query. A longer query builds a
// MATCH expression that is expensive for the FTS5 parser to evaluate.
const MaxQueryRunes = 256

// MaxOrQueryRunes caps a disjunctive retrieval query. It is larger than
// MaxQueryRunes because callers expand a task description into many terms.
const MaxOrQueryRunes = 2048

// matchTerms turns a query into escaped FTS5 terms.
func matchTerms(query string, loose bool) ([]string, error) {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty query")
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
		return nil, fmt.Errorf("query contains no searchable terms")
	}
	return terms, nil
}

// BuildMatch converts a user query into an FTS5 MATCH expression.
//
// The raw user text is never handed to SQLite: every fragment is escaped and
// wrapped, because an unescaped quote makes FTS5 fail with "unterminated
// string".
//
// The query is cut into fragments by whitespace and then by script. A CJK
// fragment becomes one phrase, which FTS5 matches only at consecutive
// positions, i.e. an exact substring: "数据库" matches "本地持久化数据库" but
// not "数据在别处，还有库". A phrase must also lie inside a single column
// (title, content or tags). Fragments are combined with AND, so a query that
// mixes scripts requires all of its fragments to be present. When loose is
// true each CJK character becomes its own term joined by AND, which widens
// recall at the cost of the substring guarantee.
func BuildMatch(query string, loose bool) (string, error) {
	if n := utf8.RuneCountInString(query); n > MaxQueryRunes {
		return "", fmt.Errorf("query is %d characters long, at most %d are allowed", n, MaxQueryRunes)
	}
	terms, err := matchTerms(query, loose)
	if err != nil {
		return "", err
	}
	return strings.Join(terms, " AND "), nil
}

// BuildMatchOr converts a query into an FTS5 MATCH expression where any
// fragment may match. It is used for ranked retrieval, where recall matters
// more than requiring every fragment to be present.
func BuildMatchOr(query string, loose bool) (string, error) {
	if n := utf8.RuneCountInString(query); n > MaxOrQueryRunes {
		return "", fmt.Errorf("query is %d characters long, at most %d are allowed", n, MaxOrQueryRunes)
	}
	terms, err := matchTerms(query, loose)
	if err != nil {
		return "", err
	}
	return strings.Join(terms, " OR "), nil
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

// MatchContext returns a window of the original text around the longest query
// fragment that occurs in it. The window is computed on the unpadded text, so
// the result never contains the artificial spaces inserted for tokenization.
func MatchContext(content, query string, window int) string {
	hr := []rune(content)
	fragments := queryFragments(query)

	bestStart, bestLen := -1, 0
	for _, f := range fragments {
		fr := []rune(f)
		if len(fr) == 0 {
			continue
		}
		if idx := indexFoldRunes(hr, fr); idx >= 0 && len(fr) > bestLen {
			bestStart, bestLen = idx, len(fr)
		}
	}
	if bestStart < 0 {
		end := window * 2
		if end > len(hr) {
			end = len(hr)
		}
		out := string(hr[:end])
		if end < len(hr) {
			out += "…"
		}
		return out
	}

	start := bestStart - window
	if start < 0 {
		start = 0
	}
	end := bestStart + bestLen + window
	if end > len(hr) {
		end = len(hr)
	}
	out := string(hr[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(hr) {
		out += "…"
	}
	return out
}

// indexFoldRunes returns the index of needle inside haystack, comparing runes
// case-insensitively.
func indexFoldRunes(haystack, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
	lower := make([]rune, len(haystack))
	for i, r := range haystack {
		lower[i] = unicode.ToLower(r)
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		ok := true
		for j := range needle {
			if lower[i+j] != unicode.ToLower(needle[j]) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}
