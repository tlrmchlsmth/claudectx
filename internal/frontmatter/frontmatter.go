// Package frontmatter splits and parses the YAML frontmatter block of a
// SKILL.md file, preserving the raw text so round-trips are byte-faithful.
package frontmatter

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Doc is a SKILL.md split into its parts. Raw is kept verbatim so writers
// can re-emit the file unchanged.
type Doc struct {
	RawFrontmatter string         // without the --- fences
	Body           string         // everything after the closing fence
	Fields         map[string]any // parsed frontmatter, nil if none
}

// Parse splits content into frontmatter and body. A file with no leading
// "---" line has no frontmatter (Fields == nil).
func Parse(content string) (*Doc, error) {
	if !strings.HasPrefix(content, "---\n") && content != "---" {
		return &Doc{Body: content}, nil
	}
	rest := strings.TrimPrefix(content, "---\n")
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return nil, fmt.Errorf("unterminated frontmatter (no closing ---)")
	}
	raw := rest[:idx]
	body := rest[idx+len("\n---"):]
	body = strings.TrimPrefix(body, "\n")

	fields := map[string]any{}
	if err := yaml.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, fmt.Errorf("invalid frontmatter YAML: %w", err)
	}
	return &Doc{RawFrontmatter: raw, Body: body, Fields: fields}, nil
}

// String reassembles the document from its raw parts.
func (d *Doc) String() string {
	if d.Fields == nil && d.RawFrontmatter == "" {
		return d.Body
	}
	return "---\n" + d.RawFrontmatter + "\n---\n" + d.Body
}

// Get returns a string field ("" when absent or not a string).
func (d *Doc) Get(key string) string {
	if d.Fields == nil {
		return ""
	}
	if v, ok := d.Fields[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// Has reports whether the field exists at all.
func (d *Doc) Has(key string) bool {
	_, ok := d.Fields[key]
	return ok
}
