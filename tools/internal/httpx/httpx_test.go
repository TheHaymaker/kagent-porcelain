package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoDecodesGoogleError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Service not found","status":"NOT_FOUND"}}`))
	}))
	defer srv.Close()
	_, err := Do(context.Background(), srv.Client(), Request{Method: "GET", URL: srv.URL}, nil)
	if !IsNotFound(err) {
		t.Fatalf("want not found, got %v", err)
	}
	if !strings.Contains(err.Error(), "NOT_FOUND: Service not found") {
		t.Fatalf("unexpected message %q", err)
	}
}

func TestDoDecodesGitLabError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"message":{"name":["has already been taken"]}}`))
	}))
	defer srv.Close()
	_, err := Do(context.Background(), srv.Client(), Request{Method: "GET", URL: srv.URL}, nil)
	if StatusOf(err) != 400 || !strings.Contains(err.Error(), "has already been taken") {
		t.Fatalf("unexpected %v", err)
	}
}

func TestDoBoundsErrorMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(strings.Repeat("x", 10000)))
	}))
	defer srv.Close()
	_, err := Do(context.Background(), srv.Client(), Request{Method: "GET", URL: srv.URL}, nil)
	if len(err.Error()) > 1000 {
		t.Fatalf("error not bounded: %d bytes", len(err.Error()))
	}
}

func TestTruncateRuneBoundary(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes each
	got := Truncate(s, 5)
	if !strings.HasPrefix(got, "éé") || strings.Contains(got, "�") {
		t.Fatalf("bad truncation %q", got)
	}
}
