// Package tomlx emits the small TOML subset claudectx writes and splices
// generated sections into an existing config.toml without disturbing the
// user's other content or comments.
package tomlx

import (
	"fmt"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// EmitValue renders a Go value as a TOML literal. Supported: string, bool,
// int/int64/float64, []string, map[string]string (inline table).
func EmitValue(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return quote(t), nil
	case bool:
		return fmt.Sprintf("%v", t), nil
	case int:
		return fmt.Sprintf("%d", t), nil
	case int64:
		return fmt.Sprintf("%d", t), nil
	case float64:
		// claude.json numbers arrive as float64; render integral ones plainly.
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t)), nil
		}
		return fmt.Sprintf("%v", t), nil
	case []string:
		parts := make([]string, len(t))
		for i, s := range t {
			parts[i] = quote(s)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			p, err := EmitValue(e)
			if err != nil {
				return "", err
			}
			parts[i] = p
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]string:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%s = %s", bareKey(k), quote(t[k]))
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	default:
		return "", fmt.Errorf("tomlx: unsupported value type %T", v)
	}
}

// EmitTable renders a [name] table with sorted keys.
func EmitTable(name string, kv map[string]any) (string, error) {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]\n", name)
	for _, k := range keys {
		val, err := EmitValue(kv[k])
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s = %s\n", bareKey(k), val)
	}
	return b.String(), nil
}

func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

var bareOK = func(r rune) bool {
	return r == '-' || r == '_' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func bareKey(k string) string {
	for _, r := range k {
		if !bareOK(r) {
			return quote(k)
		}
	}
	return k
}

// SpliceTable replaces the [name] table in doc with block (which must include
// its own header line), or appends it. The rest of the document — comments
// included — is untouched. The result is validated by re-parsing; an invalid
// result is returned as an error rather than silently emitted.
func SpliceTable(doc, name, block string) (string, error) {
	lines := strings.Split(doc, "\n")
	start, end := -1, len(lines)
	header := "[" + name + "]"
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if start == -1 {
			if trimmed == header {
				start = i
			}
			continue
		}
		// First subsequent table header ends the section.
		if strings.HasPrefix(trimmed, "[") {
			end = i
			break
		}
	}
	// Comments and blank lines immediately above the next header belong to
	// that header, not to the section being replaced.
	for end > start+1 && end <= len(lines) {
		prev := strings.TrimSpace(lines[end-1])
		if prev == "" || strings.HasPrefix(prev, "#") {
			end--
			continue
		}
		break
	}

	block = strings.TrimRight(block, "\n") + "\n"
	var out string
	if start == -1 {
		out = strings.TrimRight(doc, "\n")
		if out != "" {
			out += "\n\n"
		}
		out += block
	} else {
		before := strings.Join(lines[:start], "\n")
		after := strings.Join(lines[end:], "\n")
		out = before
		if before != "" && !strings.HasSuffix(before, "\n") {
			out += "\n"
		}
		out += block
		if strings.TrimSpace(after) != "" {
			out += "\n" + after
		}
	}

	var check map[string]any
	if err := toml.Unmarshal([]byte(out), &check); err != nil {
		return "", fmt.Errorf("tomlx: splice produced invalid TOML (refusing to write): %w", err)
	}
	return out, nil
}

// SetTopLevel sets or replaces a top-level scalar key, keeping it above the
// first table header (TOML requires top-level keys before any table).
func SetTopLevel(doc, key string, value any) (string, error) {
	val, err := EmitValue(value)
	if err != nil {
		return "", err
	}
	newLine := fmt.Sprintf("%s = %s", bareKey(key), val)

	lines := strings.Split(doc, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			break // only scan the top-level region
		}
		if strings.HasPrefix(trimmed, key) {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, key))
			if strings.HasPrefix(rest, "=") {
				lines[i] = newLine
				return validate(strings.Join(lines, "\n"))
			}
		}
	}
	// Insert before the first table header, or append.
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			out := append([]string{}, lines[:i]...)
			out = append(out, newLine)
			out = append(out, lines[i:]...)
			return validate(strings.Join(out, "\n"))
		}
	}
	out := strings.TrimRight(doc, "\n")
	if out != "" {
		out += "\n"
	}
	return validate(out + newLine + "\n")
}

func validate(doc string) (string, error) {
	var check map[string]any
	if err := toml.Unmarshal([]byte(doc), &check); err != nil {
		return "", fmt.Errorf("tomlx: edit produced invalid TOML (refusing to write): %w", err)
	}
	return doc, nil
}
