// Package httpx holds the small HTTP plumbing shared by the tool servers: a
// Doer interface (so every upstream client can be replaced by an httptest
// fake), JSON request helpers with bounded response sizes, and readable
// upstream API errors.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Doer is the subset of *http.Client the tool servers use. Production code
// passes an oauth2-authenticated client (Google) or a client whose transport
// adds the GitLab token; tests pass the client of an httptest.Server.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// MaxResponseBytes bounds every upstream response body we read.
const MaxResponseBytes = 8 << 20

// maxErrorMessage bounds the upstream message we surface in errors.
const maxErrorMessage = 600

// APIError is a non-2xx upstream response.
type APIError struct {
	Status  int
	Reason  string // e.g. Google's error.status ("NOT_FOUND"), optional
	Message string
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "upstream returned HTTP %d", e.Status)
	if e.Reason != "" {
		fmt.Fprintf(&b, " %s", e.Reason)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	return b.String()
}

// IsNotFound reports whether err is an upstream 404.
func IsNotFound(err error) bool { return StatusOf(err) == http.StatusNotFound }

// StatusOf returns the HTTP status of an APIError, or 0.
func StatusOf(err error) int {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// Request describes one JSON call.
type Request struct {
	Method string
	URL    string
	Header http.Header
	Body   any // marshalled as JSON when non-nil
}

// Do performs req and decodes a JSON response into out (if non-nil). It returns
// the response headers so callers can read pagination headers.
func Do(ctx context.Context, d Doer, req Request, out any) (http.Header, error) {
	var body io.Reader
	if req.Body != nil {
		b, err := json.Marshal(req.Body)
		if err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	hreq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	for k, vs := range req.Header {
		for _, v := range vs {
			hreq.Header.Add(k, v)
		}
	}
	if req.Body != nil {
		hreq.Header.Set("Content-Type", "application/json")
	}
	hreq.Header.Set("Accept", "application/json")
	resp, err := d.Do(hreq)
	if err != nil {
		// Do not include the URL-bearing *url.Error verbatim beyond its
		// operation; URLs never carry credentials here, but keep it short.
		return nil, fmt.Errorf("calling upstream: %w", unwrapURLError(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return resp.Header, fmt.Errorf("reading upstream response: %w", err)
	}
	if len(data) > MaxResponseBytes {
		return resp.Header, fmt.Errorf("upstream response exceeds %d bytes", MaxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.Header, parseError(resp.StatusCode, data)
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.Header, fmt.Errorf("decoding upstream response: %w", err)
		}
	}
	return resp.Header, nil
}

func unwrapURLError(err error) error {
	// *url.Error formats as `Get "https://...": cause`; keep only the cause
	// plus context errors so messages stay short and URL-free.
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok && u.Unwrap() != nil {
		return u.Unwrap()
	}
	return err
}

// parseError understands Google ({"error":{"code","message","status"}}) and
// GitLab ({"message": ...} / {"error": ..., "error_description": ...}) shapes.
func parseError(status int, data []byte) error {
	ae := &APIError{Status: status}
	var generic map[string]any
	if json.Unmarshal(data, &generic) == nil {
		switch e := generic["error"].(type) {
		case map[string]any:
			ae.Message, _ = e["message"].(string)
			ae.Reason, _ = e["status"].(string)
		case string:
			ae.Message = e
			if d, ok := generic["error_description"].(string); ok {
				ae.Message += ": " + d
			}
		}
		if ae.Message == "" {
			if m, ok := generic["message"]; ok {
				switch mv := m.(type) {
				case string:
					ae.Message = mv
				default:
					b, _ := json.Marshal(mv)
					ae.Message = string(b)
				}
			}
		}
	}
	if ae.Message == "" {
		ae.Message = strings.TrimSpace(string(data))
	}
	ae.Message = Truncate(oneLine(ae.Message), maxErrorMessage)
	return ae
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Truncate shortens s to at most n bytes (on a rune boundary), adding an
// ellipsis marker when it cut something.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…[truncated]"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
