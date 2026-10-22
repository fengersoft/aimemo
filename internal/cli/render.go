package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"aimemo/internal/store"
)

// memoryView is the CLI facing shape of a stored record.
type memoryView struct {
	ID             int64    `json:"id"`
	Category       string   `json:"category"`
	Type           string   `json:"type"`
	Importance     int      `json:"importance"`
	Title          string   `json:"title"`
	Content        string   `json:"content,omitempty"`
	ContentPreview string   `json:"content_preview,omitempty"`
	Scope          string   `json:"scope"`
	Source         string   `json:"source"`
	Tags           []string `json:"tags"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
	MatchContext   string   `json:"match_context,omitempty"`
}

// previewLimit is the number of characters kept in content_preview.
const previewLimit = 200

// toMemoryView renders a full record.
func toMemoryView(m store.Memory) memoryView {
	return memoryView{
		ID:           m.ID,
		Category:     m.Category,
		Type:         m.Type,
		Importance:   m.Importance,
		Title:        m.Title,
		Content:      m.Content,
		Scope:        m.Scope,
		Source:       m.Source,
		Tags:         m.Tags,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
		MatchContext: m.MatchContext,
	}
}

// toMemoryPreview renders a record without the full body.
func toMemoryPreview(m store.Memory) memoryView {
	v := toMemoryView(m)
	v.Content = ""
	r := []rune(m.Content)
	if len(r) > previewLimit {
		v.ContentPreview = string(r[:previewLimit]) + "…"
	} else {
		v.ContentPreview = m.Content
	}
	return v
}

// emit writes the success envelope to w.
func emit(g globalOpts, w io.Writer, data any) {
	if g.format == "text" {
		writeText(w, data)
		return
	}
	b, err := json.MarshalIndent(envelope{OK: true, Data: data}, "", "  ")
	if err != nil {
		fmt.Fprintf(w, "{\"ok\":false,\"error\":{\"code\":%q,\"message\":%q}}\n", codeDBError, err.Error())
		return
	}
	fmt.Fprintf(w, "%s\n", b)
}

// writeText renders the command output as plain text.
func writeText(w io.Writer, data any) {
	switch v := data.(type) {
	case memoryView:
		writeMemoryText(w, v)
	case []memoryView:
		for _, m := range v {
			writeMemoryText(w, m)
		}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s: %v\n", k, v[k])
		}
	default:
		b, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			fmt.Fprintf(w, "%v\n", data)
			return
		}
		fmt.Fprintf(w, "%s\n", b)
	}
}

// writeMemoryText renders one memory record as plain text.
func writeMemoryText(w io.Writer, m memoryView) {
	fmt.Fprintf(w, "#%d  %s  (%s, importance %d)\n", m.ID, m.Category, m.Type, m.Importance)
	if m.Title != "" {
		fmt.Fprintf(w, "  title: %s\n", m.Title)
	}
	if len(m.Tags) > 0 {
		fmt.Fprintf(w, "  tags: %s\n", strings.Join(m.Tags, ", "))
	}
	if m.Scope != "" {
		fmt.Fprintf(w, "  scope: %s\n", m.Scope)
	}
	if m.Source != "" {
		fmt.Fprintf(w, "  source: %s\n", m.Source)
	}
	if m.ContentPreview != "" {
		fmt.Fprintf(w, "  %s\n", m.ContentPreview)
	}
	if m.Content != "" {
		fmt.Fprintf(w, "  %s\n", m.Content)
	}
	if m.MatchContext != "" {
		fmt.Fprintf(w, "  match: %s\n", m.MatchContext)
	}
	fmt.Fprintf(w, "  created: %s  updated: %s\n", m.CreatedAt, m.UpdatedAt)
}

// sortStrings sorts a slice of strings in place.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
