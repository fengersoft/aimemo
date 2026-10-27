package store

import (
	"fmt"
	"strings"
	"unicode"
)

// ConflictType describes why two memories were flagged as conflicting.
type ConflictType string

const (
	// ConflictNegation: one recommends something, the other says not to.
	ConflictNegation ConflictType = "negation"
	// ConflictDuplicate: same category, very similar title, different content.
	ConflictDuplicate ConflictType = "duplicate"
	// ConflictContradiction: one says "use X", the other says "use Y" for the same topic.
	ConflictContradiction ConflictType = "contradiction"
)

// Conflict is a detected contradiction between two memories.
type Conflict struct {
	ID           int64        `json:"id"`
	Type         ConflictType `json:"type"`
	LeftID       int64        `json:"left_id"`
	LeftTitle    string       `json:"left_title"`
	LeftCategory string       `json:"left_category"`
	RightID      int64        `json:"right_id"`
	RightTitle   string       `json:"right_title"`
	RightCategory string      `json:"right_category"`
	Explanation  string       `json:"explanation"`
	Score        float64      `json:"score"`
}

// negationMarkers are words/phrases that indicate a negative recommendation.
var negationMarkers = []string{
	// English
	"don't use", "do not use", "never use", "avoid", "wrong", "incorrect",
	"bad idea", "should not", "shouldn't", "must not", "mustn't",
	"no longer", "deprecated", "obsolete", "broken", "harmful",
	"not recommended", "not advised", "unwise", "mistake",
	// Chinese
	"不要用", "不用", "不能用", "别用", "避免", "禁用", "错误", "不对",
	"不好", "有问题", "有坑", "废弃", "过时", "弃用", "勿用",
	"不推荐", "不建议", "不正确", "不妥", "有误", "踩坑",
}

// positiveMarkers are words that indicate a positive recommendation.
var positiveMarkers = []string{
	// English
	"should use", "must use", "recommend", "best practice", "prefer",
	"use instead", "always use", "go to", "standard", "correct way",
	// Chinese
	"推荐用", "建议用", "最好用", "应该用", "需要使用", "使用",
	"最佳实践", "正确做法", "标准做法", "推荐", "建议",
}

// hasNegation reports whether text contains a negation marker.
func hasNegation(text string) bool {
	lower := strings.ToLower(text)
	for _, m := range negationMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// hasPositive reports whether text contains a positive recommendation marker.
func hasPositive(text string) bool {
	lower := strings.ToLower(text)
	for _, m := range positiveMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// extractNegationTopic tries to find the subject of a negation, e.g.
// "don't use goroutines for X" -> "goroutines".
func extractNegationTopic(text string) string {
	lower := strings.ToLower(text)
	for _, m := range []string{"don't use", "do not use", "never use", "不要用", "别用", "不用", "不推荐用", "不建议用"} {
		if idx := strings.Index(lower, m); idx >= 0 {
			rest := strings.TrimSpace(text[idx+len(m):])
			// grab the first word or CJK run after the marker
			var b strings.Builder
			for _, r := range rest {
				if unicode.IsSpace(r) {
					break
				}
				if unicode.IsLetter(r) || unicode.IsDigit(r) || r > 0x2E80 {
					b.WriteRune(r)
					continue
				}
				break
			}
			return b.String()
		}
	}
	return ""
}

// titleSimilarity returns a rough similarity score (0..1) between two titles.
func titleSimilarity(a, b string) float64 {
	na, nb := normalizeTitle(a), normalizeTitle(b)
	if na == "" || nb == "" {
		return 0
	}
	if na == nb {
		return 1.0
	}
	// containment
	if strings.Contains(na, nb) || strings.Contains(nb, na) {
		return 0.8
	}
	// character overlap
	setA := map[rune]bool{}
	for _, r := range na {
		setA[r] = true
	}
	inter := 0
	for _, r := range nb {
		if setA[r] {
			inter++
		}
	}
	union := len(setA)
	for _, r := range nb {
		if !setA[r] {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// DetectConflicts finds pairs of memories in a workspace that may contradict
// each other. It uses a two-pass approach:
//
//  1. Fetch recent memories (up to maxScan).
//  2. For each pair in the same category, check for negation / contradiction.
//
// This is intentionally conservative: it only reports high-confidence
// conflicts so the signal-to-noise ratio stays usable.
func (s *Store) DetectConflicts(workspace string, maxScan int) ([]Conflict, error) {
	if maxScan <= 0 || maxScan > 2000 {
		maxScan = 500
	}
	rows, err := s.db.Query(`
		SELECT `+selectMemoryColumns+`
		FROM memories m JOIN categories c ON c.id = m.category_id
		WHERE m.workspace = ? AND (m.valid_until = '' OR m.valid_until >= ?)
		ORDER BY m.id DESC LIMIT ?`, workspace, Now(), maxScan)
	if err != nil {
		return nil, err
	}
	items, err := scanMemories(rows)
	if err != nil {
		return nil, err
	}

	var conflicts []Conflict
	id := int64(0)

	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			a, b := &items[i], &items[j]
			// only compare within the same category
			if a.Category != b.Category {
				continue
			}
			// skip pairs where both are expired
			if a.ValidUntil != "" && b.ValidUntil != "" {
				continue
			}

			// Negation conflict: one has positive, other has negation, same topic.
			if hasNegation(a.Content+a.Solution) && hasPositive(b.Content+b.Solution) {
				topicA := extractNegationTopic(a.Content + " " + a.Solution)
				topicB := extractNegationTopic(b.Content + " " + b.Solution)
				if topicA != "" || topicB != "" || titleSimilarity(a.Title, b.Title) > 0.4 {
					id++
					conflicts = append(conflicts, Conflict{
						ID:            id,
						Type:          ConflictNegation,
						LeftID:        a.ID,
						LeftTitle:     a.Title,
						LeftCategory:  a.Category,
						RightID:       b.ID,
						RightTitle:    b.Title,
						RightCategory: b.Category,
						Explanation:   fmt.Sprintf("%q appears to recommend against what %q recommends", a.Title, b.Title),
						Score:         0.7 + 0.3*titleSimilarity(a.Title, b.Title),
					})
				}
			}
			// reverse direction
			if hasNegation(b.Content+b.Solution) && hasPositive(a.Content+a.Solution) {
				topicA := extractNegationTopic(a.Content + " " + a.Solution)
				topicB := extractNegationTopic(b.Content + " " + b.Solution)
				if topicA != "" || topicB != "" || titleSimilarity(a.Title, b.Title) > 0.4 {
					id++
					conflicts = append(conflicts, Conflict{
						ID:            id,
						Type:          ConflictNegation,
						LeftID:        a.ID,
						LeftTitle:     a.Title,
						LeftCategory:  a.Category,
						RightID:       b.ID,
						RightTitle:    b.Title,
						RightCategory: b.Category,
						Explanation:   fmt.Sprintf("%q appears to recommend against what %q recommends", b.Title, a.Title),
						Score:         0.7 + 0.3*titleSimilarity(a.Title, b.Title),
					})
				}
			}
			// Contradiction: both recommend different things
			if hasPositive(a.Content+a.Solution) && hasPositive(b.Content+b.Solution) {
				if titleSimilarity(a.Title, b.Title) > 0.6 && a.Content != b.Content {
					id++
					conflicts = append(conflicts, Conflict{
						ID:            id,
						Type:          ConflictContradiction,
						LeftID:        a.ID,
						LeftTitle:     a.Title,
						LeftCategory:  a.Category,
						RightID:       b.ID,
						RightTitle:    b.Title,
						RightCategory: b.Category,
						Explanation:   fmt.Sprintf("%q and %q suggest different approaches for similar topics", a.Title, b.Title),
						Score:         0.6 + 0.2*titleSimilarity(a.Title, b.Title),
					})
				}
			}

			// Duplicate / near-duplicate with divergent content.
			if sim := titleSimilarity(a.Title, b.Title); sim > 0.85 {
				if a.Content != b.Content && a.Solution != b.Solution {
					id++
					conflicts = append(conflicts, Conflict{
						ID:            id,
						Type:          ConflictDuplicate,
						LeftID:        a.ID,
						LeftTitle:     a.Title,
						LeftCategory:  a.Category,
						RightID:       b.ID,
						RightTitle:    b.Title,
						RightCategory: b.Category,
						Explanation:   fmt.Sprintf("titles are %.0f%% similar but solutions differ; consider merging or superseding", sim*100),
						Score:         sim,
					})
				}
			}
		}
	}
	return conflicts, nil
}
