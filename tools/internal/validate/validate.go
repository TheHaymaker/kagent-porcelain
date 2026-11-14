// Package validate holds argument validation shared by the tool servers.
// Every tool validates its own inputs before calling upstream, so malformed
// or hostile arguments (path traversal, other projects, oversized payloads)
// are rejected with a readable message and never reach an API.
package validate

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Error is an invalid tool argument. The tool wrapper renders it as
// "invalid argument: ...".
type Error struct {
	Field  string
	Reason string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Reason) }

// Errorf builds an *Error.
func Errorf(field, format string, args ...any) error {
	return &Error{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// IsInvalid reports whether err is a validation error.
func IsInvalid(err error) bool {
	var ve *Error
	return errors.As(err, &ve)
}

// Match checks that v is non-empty and matches re; desc explains the rule.
func Match(field, v string, re *regexp.Regexp, desc string) error {
	if v == "" {
		return Errorf(field, "is required")
	}
	if !re.MatchString(v) {
		return Errorf(field, "%q is invalid: must be %s", Clip(v, 80), desc)
	}
	return nil
}

// Clip shortens a user-supplied value for inclusion in an error message.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Common patterns.
var (
	// Slug: lowercase DNS-label-ish name used for projects and services.
	Slug = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// GitRef: conservative branch name (no "..", no leading "-", no spaces).
	GitRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	// BQID: BigQuery dataset id (letters, digits, underscores).
	BQDataset = regexp.MustCompile(`^[A-Za-z0-9_]{1,1000}$`)
	// BQTable: BigQuery table id (ASCII subset; no "$" partition decorators).
	BQTable = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,999}$`)
	// EnvName: container env var name.
	EnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

// GitRefName validates a branch name with git's main rules.
func GitRefName(field, v string) error {
	if err := Match(field, v, GitRef, "a branch name of letters, digits, '.', '_', '-', '/'"); err != nil {
		return err
	}
	if strings.Contains(v, "..") || strings.Contains(v, "//") || strings.HasSuffix(v, "/") ||
		strings.HasSuffix(v, ".lock") || strings.HasSuffix(v, ".") || strings.Contains(v, "@{") {
		return Errorf(field, "%q is not a valid git branch name", Clip(v, 80))
	}
	return nil
}

// RepoPath validates a file path inside a repository: relative, no "..", no
// empty or dot segments, no control characters.
func RepoPath(field, v string) error {
	if v == "" {
		return Errorf(field, "is required")
	}
	if len(v) > 1024 {
		return Errorf(field, "is longer than 1024 characters")
	}
	if strings.HasPrefix(v, "/") || strings.HasSuffix(v, "/") {
		return Errorf(field, "%q must be a relative file path", Clip(v, 80))
	}
	for _, seg := range strings.Split(v, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return Errorf(field, "%q must not contain empty, '.' or '..' segments", Clip(v, 80))
		}
		if seg == ".git" {
			return Errorf(field, "%q must not touch .git", Clip(v, 80))
		}
	}
	for _, r := range v {
		if unicode.IsControl(r) || r == '\\' {
			return Errorf(field, "%q contains control characters or backslashes", Clip(v, 80))
		}
	}
	return nil
}

// Text checks free text: required (if req), at most max bytes, no control
// characters other than newline and tab.
func Text(field, v string, req bool, max int) error {
	if v == "" {
		if req {
			return Errorf(field, "is required")
		}
		return nil
	}
	if len(v) > max {
		return Errorf(field, "is longer than %d bytes", max)
	}
	for _, r := range v {
		if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
			return Errorf(field, "contains control characters")
		}
	}
	return nil
}

// Bounded clamps n into [1, max], substituting def when n <= 0.
func Bounded(n, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// labelInvalid matches characters not allowed in a GCP label value.
var labelInvalid = regexp.MustCompile(`[^a-z0-9_-]`)

// LabelValue converts an arbitrary identity (e.g. an email) into a valid GCP
// label value: lowercase, [a-z0-9_-], at most 63 characters.
func LabelValue(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = labelInvalid.ReplaceAllString(s, "_")
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}

var pageTokenRe = regexp.MustCompile(`^[A-Za-z0-9_=+/.~-]+$`)

// PageToken validates an opaque upstream pagination token (empty is fine).
func PageToken(field, v string) error {
	if v == "" {
		return nil
	}
	if len(v) > 4096 || !pageTokenRe.MatchString(v) {
		return Errorf(field, "is malformed; pass back the token from the previous result unchanged")
	}
	return nil
}
