package mcpx

import (
	"regexp"
	"strings"
	"sync"
)

// Redactor removes secret values and token-shaped strings from text that is
// about to leave the server (tool errors, logs).
type Redactor struct {
	mu      sync.RWMutex
	sources []func() []string
}

// NewRedactor returns a Redactor with only the built-in patterns.
func NewRedactor() *Redactor { return &Redactor{} }

// AddSource registers a function returning current secret values (e.g. a
// token re-read from a mounted Secret file).
func (r *Redactor) AddSource(f func() []string) {
	r.mu.Lock()
	r.sources = append(r.sources, f)
	r.mu.Unlock()
}

var tokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`glpat-[A-Za-z0-9_\-]{10,}`),                      // GitLab PAT
	regexp.MustCompile(`gl(?:dt|oas|rt|cbt|ptt|ft)-[A-Za-z0-9_\-]{10,}`), // other GitLab tokens
	regexp.MustCompile(`ya29\.[A-Za-z0-9_\-\.]{10,}`),                    // Google OAuth access token
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9_\-\.=+/]{8,}`),
	regexp.MustCompile(`(?i)(private-token[:=]\s*)[^\s"',]+`),
}

// Redact scrubs s.
func (r *Redactor) Redact(s string) string {
	if s == "" {
		return s
	}
	r.mu.RLock()
	srcs := r.sources
	r.mu.RUnlock()
	for _, src := range srcs {
		for _, secret := range src() {
			if len(secret) >= 4 {
				s = strings.ReplaceAll(s, secret, "[REDACTED]")
			}
		}
	}
	for _, re := range tokenPatterns {
		if re.NumSubexp() > 0 {
			s = re.ReplaceAllString(s, "${1}[REDACTED]")
		} else {
			s = re.ReplaceAllString(s, "[REDACTED]")
		}
	}
	return s
}
