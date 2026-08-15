package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// manifest is a parsed monograph.toml: a flat table of quoted strings and
// arrays of quoted strings.
type manifest struct {
	scalars map[string]string
	lists   map[string][]string
	path    string
}

func (m manifest) str(key string) string    { return m.scalars[key] }
func (m manifest) list(key string) []string { return m.lists[key] }

func (m manifest) has(key string) bool {
	if _, ok := m.scalars[key]; ok {
		return true
	}
	_, ok := m.lists[key]
	return ok
}

// readManifest parses a monograph.toml.
//
// This is a deliberately tiny reader, not a TOML implementation: manifests are
// flat `key = "value"` pairs and `key = ["a", "b"]` arrays with `#` comments.
// Keeping the tool free of third-party parsers makes it trivial to run inside a
// container. Anything fancier is rejected loudly rather than misinterpreted
// quietly.
func readManifest(path string) (manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return manifest{}, err
	}
	defer f.Close()

	m := manifest{
		scalars: map[string]string{},
		lists:   map[string][]string{},
		path:    path,
	}

	sc := bufio.NewScanner(f)
	// An array may span lines, so accumulate until the closing bracket.
	var (
		pendingKey  string
		pendingBody strings.Builder
		startLine   int
	)

	for line := 1; sc.Scan(); line++ {
		raw := sc.Text()
		text := strings.TrimSpace(raw)

		if pendingKey != "" {
			pendingBody.WriteString(" ")
			pendingBody.WriteString(stripComment(text))
			if strings.Contains(text, "]") {
				items, err := parseArray(path, startLine, pendingKey, pendingBody.String())
				if err != nil {
					return manifest{}, err
				}
				m.lists[pendingKey] = items
				pendingKey = ""
				pendingBody.Reset()
			}
			continue
		}

		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if strings.HasPrefix(text, "[") {
			return manifest{}, fmt.Errorf("%s:%d: tables are not supported in monograph.toml", path, line)
		}

		key, rawValue, ok := strings.Cut(text, "=")
		if !ok {
			return manifest{}, fmt.Errorf("%s:%d: expected key = \"value\" or key = [\"a\", \"b\"]", path, line)
		}
		key = strings.TrimSpace(key)
		if m.has(key) {
			return manifest{}, fmt.Errorf("%s:%d: duplicate key %q", path, line, key)
		}
		value := strings.TrimSpace(stripComment(rawValue))

		if strings.HasPrefix(value, "[") {
			if !strings.Contains(value, "]") {
				pendingKey, startLine = key, line
				pendingBody.WriteString(value)
				continue
			}
			items, err := parseArray(path, line, key, value)
			if err != nil {
				return manifest{}, err
			}
			m.lists[key] = items
			continue
		}

		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return manifest{}, fmt.Errorf("%s:%d: value for %q must be a quoted string", path, line, key)
		}
		m.scalars[key] = unquoted
	}

	if pendingKey != "" {
		return manifest{}, fmt.Errorf("%s:%d: unterminated array for %q", path, startLine, pendingKey)
	}
	return m, sc.Err()
}

// parseArray reads `["a", "b"]` into a slice, rejecting anything else.
func parseArray(path string, line int, key, body string) ([]string, error) {
	open := strings.Index(body, "[")
	closeIdx := strings.LastIndex(body, "]")
	if open < 0 || closeIdx < open {
		return nil, fmt.Errorf("%s:%d: malformed array for %q", path, line, key)
	}
	inner := strings.TrimSpace(body[open+1 : closeIdx])
	if inner == "" {
		return []string{}, nil
	}

	var items []string
	for _, part := range strings.Split(inner, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue // trailing comma
		}
		unquoted, err := strconv.Unquote(part)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: array entry %s for %q must be a quoted string", path, line, part, key)
		}
		items = append(items, unquoted)
	}
	return items, nil
}

// stripComment drops a trailing `#` comment that is not inside a quoted value.
func stripComment(s string) string {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			// Manifest values never contain escaped quotes; strconv.Unquote
			// would reject them anyway.
			inQuote = !inQuote
		case '#':
			if !inQuote {
				return s[:i]
			}
		}
	}
	return s
}

// targetFromManifest validates a parsed manifest into a Target.
func targetFromManifest(root string, m manifest) (Target, error) {
	t := Target{
		Name:       m.str("name"),
		Kind:       m.str("kind"),
		Owner:      m.str("owner"),
		Image:      m.str("image"),
		TestCmd:    m.str("testCmd"),
		CodegenCmd: m.str("codegenCmd"),
		Produces:   m.list("produces"),
		Root:       root,
	}
	if t.Name == "" {
		return Target{}, fmt.Errorf("%s: missing required key \"name\"", m.path)
	}
	if t.Kind == "" {
		return Target{}, fmt.Errorf("%s: missing required key \"kind\"", m.path)
	}
	if t.TestCmd != "" && t.Image == "" {
		return Target{}, fmt.Errorf("%s: target %q has testCmd but no image", m.path, t.Name)
	}
	if t.CodegenCmd != "" && len(t.Produces) == 0 {
		return Target{}, fmt.Errorf("%s: target %q has codegenCmd but declares no produces globs", m.path, t.Name)
	}
	if len(t.Produces) > 0 && t.CodegenCmd == "" {
		return Target{}, fmt.Errorf("%s: target %q declares produces but no codegenCmd to create them", m.path, t.Name)
	}
	if t.CodegenCmd != "" && t.Image == "" {
		return Target{}, fmt.Errorf("%s: target %q has codegenCmd but no image", m.path, t.Name)
	}

	// Dependencies must never be declared — they are derived from real imports.
	// Outputs (`produces`) are a different thing: they say what this target
	// creates, not what it needs.
	for _, banned := range []string{"deps", "dependencies", "dependsOn"} {
		if m.has(banned) {
			return Target{}, fmt.Errorf("%s: %q is not allowed; dependencies are derived from source, not declared", m.path, banned)
		}
	}

	// A produces glob must stay inside the declaring target: a target may not
	// claim to own another target's files.
	for _, g := range t.Produces {
		if root != "." && !strings.HasPrefix(g, root+"/") && g != root {
			return Target{}, fmt.Errorf("%s: produces glob %q is outside target root %q", m.path, g, root)
		}
	}
	return t, nil
}
