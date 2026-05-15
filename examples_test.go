package starlark

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// makeRequestWithPlaceholders creates a request with custom Caddy placeholder values.
func makeRequestWithPlaceholders(method, target, body string, headers http.Header, placeholders map[string]string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	for k, vs := range headers {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	repl := caddyhttp.NewTestReplacer(r)
	for k, v := range placeholders {
		repl.Set(k, v)
	}
	ctx := context.WithValue(r.Context(), caddy.ReplacerCtxKey, repl)
	return r.WithContext(ctx)
}

func TestIndexStar(t *testing.T) {
	h, next := newHandler(t, "examples/views")

	// Default greeting
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), makeRequest("GET", "/index.star", "", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Hello, stranger!") {
		t.Errorf("body missing default greeting; got %q", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}

	// Custom name
	w = serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), makeRequest("GET", "/index.star?name=Caddy", "", nil))
	if !strings.Contains(w.Body.String(), "Hello, Caddy!") {
		t.Errorf("body missing custom greeting; got %q", w.Body.String())
	}
}

func TestErrorStar(t *testing.T) {
	h, next := newHandler(t, "examples/views")

	// 404 with default message
	r := makeRequestWithPlaceholders("GET", "/error.star", "", nil, map[string]string{
		"http.error.status_code": "404",
		"http.error.status_text": "Not Found",
		"http.error.message":     "",
	})
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), r)
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Error 404") {
		t.Errorf("body missing error code; got %q", body)
	}
	if !strings.Contains(body, "The requested URL was not found on this server.") {
		t.Errorf("body missing default 404 message; got %q", body)
	}

	// 500 with custom message
	r = makeRequestWithPlaceholders("GET", "/error.star", "", nil, map[string]string{
		"http.error.status_code": "500",
		"http.error.status_text": "Internal Server Error",
		"http.error.message":     "Database connection failed.",
	})
	w = serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), r)
	if w.Code != 500 {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	body = w.Body.String()
	if !strings.Contains(body, "Database connection failed.") {
		t.Errorf("body missing custom message; got %q", body)
	}
}

func TestUploadStar(t *testing.T) {
	h, next := newHandler(t, "examples/views")

	// GET returns upload form
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), makeRequest("GET", "/upload.star", "", nil))
	if w.Code != 200 {
		t.Fatalf("GET status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<form") {
		t.Errorf("GET body missing form; got %q", w.Body.String())
	}

	// POST success with file and note
	body, ct := makeMultipart(t, [][2]string{{"note", "hi there"}}, [][3]string{{"f", "up.txt", "hello"}})
	headers := http.Header{}
	headers.Set("Content-Type", ct)
	r := makeRequest("POST", "/upload.star", body, headers)
	w = serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), r)
	if w.Code != 200 {
		t.Fatalf("POST status = %d, want 200; body=%q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("POST Content-Type = %q, want application/json", ct)
	}
	if !strings.Contains(w.Body.String(), `"filename":"up.txt"`) {
		t.Errorf("POST body missing filename; got %q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"note":"hi there"`) {
		t.Errorf("POST body missing note; got %q", w.Body.String())
	}

	// POST failure: missing file
	body, ct = makeMultipart(t, [][2]string{{"note", "hi"}}, nil)
	headers = http.Header{}
	headers.Set("Content-Type", ct)
	r = makeRequest("POST", "/upload.star", body, headers)
	w = httptest.NewRecorder()
	err := h.ServeHTTP(w, r, caddyhttp.HandlerFunc(next.ServeHTTP))
	he, ok := err.(caddyhttp.HandlerError)
	if !ok {
		t.Fatalf("expected HandlerError for missing file, got %T %v", err, err)
	}
	if he.StatusCode != 400 {
		t.Errorf("missing file status = %d, want 400", he.StatusCode)
	}

	// Failure: wrong method
	r = makeRequest("PUT", "/upload.star", "", nil)
	w = httptest.NewRecorder()
	err = h.ServeHTTP(w, r, caddyhttp.HandlerFunc(next.ServeHTTP))
	he, ok = err.(caddyhttp.HandlerError)
	if !ok {
		t.Fatalf("expected HandlerError for PUT, got %T %v", err, err)
	}
	if he.StatusCode != 405 {
		t.Errorf("PUT status = %d, want 405", he.StatusCode)
	}
}

func TestInfoStar(t *testing.T) {
	h, next := newHandler(t, "examples/views")
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), makeRequest("GET", "/info.star", "", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{"method", "path", "host", "client_ip", "user_agent", "uri"} {
		if _, ok := result[key]; !ok {
			t.Errorf("missing key %q in JSON", key)
		}
	}
	if result["method"] != "GET" {
		t.Errorf("method = %v, want GET", result["method"])
	}
}

func TestCounterStar(t *testing.T) {
	h, next := newSessionHandler(t, "examples/views")

	// First visit
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), makeRequest("GET", "/counter.star", "", nil))
	if w.Code != 200 {
		t.Fatalf("first visit status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "1") {
		t.Errorf("first visit body missing count 1; got %q", w.Body.String())
	}
	cookie := extractSessionCookie(t, w)
	if cookie == "" {
		t.Fatal("no session cookie set on first visit")
	}

	// Second visit with cookie
	headers := http.Header{}
	headers.Set("Cookie", "session="+cookie)
	w = serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), makeRequest("GET", "/counter.star", "", headers))
	if !strings.Contains(w.Body.String(), "2") {
		t.Errorf("second visit body missing count 2; got %q", w.Body.String())
	}

	// Reset clears session and redirects
	w = serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), makeRequest("GET", "/counter.star?reset=1", "", headers))
	if w.Code != 302 {
		t.Fatalf("reset status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/counter.star" {
		t.Errorf("Location = %q, want /counter.star", loc)
	}
	raw := w.Header().Get("Set-Cookie")
	if raw == "" {
		t.Errorf("expected Set-Cookie after reset, got none")
	} else if !strings.Contains(raw, "Max-Age=0") && !strings.Contains(raw, "Expires=") {
		t.Errorf("expected session delete cookie, got %q", raw)
	}
}

func TestNowStar(t *testing.T) {
	h, next := newHandler(t, "examples/views")

	// Default timezone
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), makeRequest("GET", "/now.star", "", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{"placeholder_http", "placeholder_common_log", "placeholder_unix", "placeholder_year", "iso8601", "year", "month", "day", "hour", "minute", "in_timezone"} {
		if _, ok := result[key]; !ok {
			t.Errorf("missing key %q in JSON", key)
		}
	}

	// Invalid timezone returns error message in JSON
	w = serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), makeRequest("GET", "/now.star?tz=NotARealZone", "", nil))
	if w.Code != 200 {
		t.Fatalf("invalid tz status = %d, want 200", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !strings.Contains(result["in_timezone"].(string), "invalid timezone") {
		t.Errorf("in_timezone = %v, want invalid timezone message", result["in_timezone"])
	}
}

func TestPngStar(t *testing.T) {
	h, next := newHandler(t, "examples/views")

	// Success
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), makeRequest("GET", "/png.star?size=64&color=ff8800", "", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	body := w.Body.Bytes()
	pngSig := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if len(body) < len(pngSig) {
		t.Errorf("PNG body too short: %d bytes", len(body))
	} else if !bytes.Equal(body[:len(pngSig)], pngSig) {
		t.Errorf("body missing PNG signature; got % x", body[:len(pngSig)])
	}

	// Failure: size too small
	w = httptest.NewRecorder()
	err := h.ServeHTTP(w, makeRequest("GET", "/png.star?size=0", "", nil), caddyhttp.HandlerFunc(next.ServeHTTP))
	he, ok := err.(caddyhttp.HandlerError)
	if !ok {
		t.Fatalf("expected HandlerError for small size, got %T %v", err, err)
	}
	if he.StatusCode != 400 {
		t.Errorf("small size status = %d, want 400", he.StatusCode)
	}

	// Failure: size too large
	w = httptest.NewRecorder()
	err = h.ServeHTTP(w, makeRequest("GET", "/png.star?size=300", "", nil), caddyhttp.HandlerFunc(next.ServeHTTP))
	he, ok = err.(caddyhttp.HandlerError)
	if !ok {
		t.Fatalf("expected HandlerError for large size, got %T %v", err, err)
	}
	if he.StatusCode != 400 {
		t.Errorf("large size status = %d, want 400", he.StatusCode)
	}
}

func TestEchoStar(t *testing.T) {
	h, next := newHandler(t, "examples/views")

	// Success: POST JSON
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	r := makeRequest("POST", "/echo.star", `{"hi":"there"}`, headers)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), r)
	if w.Code != 200 {
		t.Fatalf("POST status = %d, want 200; body=%q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if !strings.Contains(w.Body.String(), `"hi":"there"`) {
		t.Errorf("body missing echoed data; got %q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"method":"POST"`) {
		t.Errorf("body missing method; got %q", w.Body.String())
	}

	// Failure: GET
	r = makeRequest("GET", "/echo.star", "", nil)
	w = httptest.NewRecorder()
	err := h.ServeHTTP(w, r, caddyhttp.HandlerFunc(next.ServeHTTP))
	he, ok := err.(caddyhttp.HandlerError)
	if !ok {
		t.Fatalf("expected HandlerError for GET, got %T %v", err, err)
	}
	if he.StatusCode != 405 {
		t.Errorf("GET status = %d, want 405", he.StatusCode)
	}
}
