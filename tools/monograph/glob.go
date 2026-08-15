package main

import (
	"path"
	"strings"
)

// matchGlob reports whether a slash-separated path matches a glob pattern,
// supporting `**` for "any number of path segments".
//
// path.Match alone is not enough: it treats `*` as never crossing a `/`, and has
// no `**`. Since `produces` globs are almost always of the form
// `some/dir/**`, that case is handled directly and the rest falls through to
// path.Match segment by segment.
func matchGlob(pattern, name string) bool {
	pattern = strings.TrimPrefix(pattern, "./")
	name = strings.TrimPrefix(name, "./")

	// Fast path: a trailing `/**` matches the directory and everything under it.
	if suffix := strings.TrimSuffix(pattern, "/**"); suffix != pattern {
		return name == suffix || strings.HasPrefix(name, suffix+"/")
	}

	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

// matchSegments matches pattern segments against name segments, where a `**`
// segment consumes zero or more name segments.
func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// Try consuming 0..len(name) segments.
			rest := pat[1:]
			if len(rest) == 0 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if matchSegments(rest, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], name[0])
		if err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// matchAnyGlob reports whether name matches any of the patterns.
func matchAnyGlob(patterns []string, name string) bool {
	for _, p := range patterns {
		if matchGlob(p, name) {
			return true
		}
	}
	return false
}
