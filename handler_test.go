package starlark

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

// newHandler returns a provisioned Handler rooted at dir, plus a no-op
// "next" handler that records when it is called (used to verify pass-through).
func newHandler(t *testing.T, dir string) (*Handler, *passThroughCounter) {
	t.Helper()
	h := &Handler{Root: dir}
	if err := h.Provision(caddy.Context{Context: context.Background()}); err != nil {
		t.Fatalf("provision: %v", err)
	}
	h.logger = zap.NewNop()
	return h, &passThroughCounter{}
}

type passThroughCounter struct{ n int }

func (p *passThroughCounter) ServeHTTP(w http.ResponseWriter, r *http.Request) error {
	p.n++
	w.WriteHeader(http.StatusTeapot)
	return nil
}

func writeScript(t *testing.T, dir, name, src string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makeRequest(method, target, body string, headers http.Header) *http.Request {
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
	ctx := context.WithValue(r.Context(), caddy.ReplacerCtxKey, repl)
	return r.WithContext(ctx)
}

func serve(t *testing.T, h *Handler, next caddyhttp.Handler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	if err := h.ServeHTTP(w, r, next); err != nil {
		// Caddy normally translates HandlerErrors, but for our tests we just
		// want to surface them as failures unless explicitly checked.
		if he, ok := err.(caddyhttp.HandlerError); ok {
			w.WriteHeader(he.StatusCode)
			_, _ = io.WriteString(w, he.Error())
			return w
		}
		t.Fatalf("ServeHTTP: %v", err)
	}
	return w
}

func TestSimpleStringResponse(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "hello.star", `
def respond(request):
    return "Hello, " + request.args.get("name", "World") + "!"
`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/hello.star?name=Caddy", "", nil))

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%q", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "Hello, Caddy!" {
		t.Errorf("body = %q", got)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestExtensionInferred(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "page.star", `def respond(req): return "page"`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/page", "", nil))
	if w.Code != 200 || w.Body.String() != "page" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}

func TestIndexResolution(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "index.star", `def respond(req): return "home"`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/", "", nil))
	if w.Code != 200 || w.Body.String() != "home" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}

func TestPassThroughWhenNoScript(t *testing.T) {
	dir := t.TempDir()
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/missing.star", "", nil))
	if next.n != 1 {
		t.Fatalf("expected pass-through, next called %d times", next.n)
	}
	if w.Code != http.StatusTeapot {
		t.Fatalf("expected next handler to set 418, got %d", w.Code)
	}
}

func TestNonStarPassThrough(t *testing.T) {
	dir := t.TempDir()
	h, next := newHandler(t, dir)
	serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/foo.png", "", nil))
	if next.n != 1 {
		t.Fatalf("non-.star path should pass through; calls=%d", next.n)
	}
}

func TestResponseObject(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "x.star", `
def respond(req):
    return Response(
        '{"ok": true}',
        status=201,
        headers={"X-Foo": "bar"},
        content_type="application/json",
    )
`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/x.star", "", nil))
	if w.Code != 201 {
		t.Fatalf("status = %d", w.Code)
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("ct = %q", w.Header().Get("Content-Type"))
	}
	if w.Header().Get("X-Foo") != "bar" {
		t.Errorf("X-Foo = %q", w.Header().Get("X-Foo"))
	}
	if w.Body.String() != `{"ok": true}` {
		t.Errorf("body = %q", w.Body.String())
	}
}

func TestTupleReturn(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "x.star", `def respond(req): return ("nope", 404)`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/x.star", "", nil))
	if w.Code != 404 || w.Body.String() != "nope" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}

func TestTupleWithHeaders(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "x.star", `def respond(req): return ("ok", 200, {"X-A": "1"})`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/x.star", "", nil))
	if w.Header().Get("X-A") != "1" {
		t.Fatalf("X-A = %q", w.Header().Get("X-A"))
	}
}

func TestRedirect(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "r.star", `def respond(req): return redirect("/elsewhere", status=301)`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/r.star", "", nil))
	if w.Code != 301 {
		t.Fatalf("status = %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/elsewhere" {
		t.Errorf("Location = %q", loc)
	}
}

func TestAbort(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "a.star", `def respond(req): abort(403, "denied")`)
	h, next := newHandler(t, dir)
	w := httptest.NewRecorder()
	err := h.ServeHTTP(w, makeRequest("GET", "/a.star", "", nil), caddyhttp.HandlerFunc(next.ServeHTTP))
	he, ok := err.(caddyhttp.HandlerError)
	if !ok {
		t.Fatalf("expected HandlerError, got %T %v", err, err)
	}
	if he.StatusCode != 403 {
		t.Fatalf("status = %d", he.StatusCode)
	}
	if !strings.Contains(he.Error(), "denied") {
		t.Errorf("error = %q", he.Error())
	}
}

func TestPlaceholder(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "p.star", `
def respond(req):
    return placeholder("http.request.host") + " / " + ph("{http.request.method}")
`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/p.star", "", nil))
	if !strings.Contains(w.Body.String(), " / GET") {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestRequestHeadersAndCookies(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "h.star", `
def respond(req):
    ua = req.headers.get("user-agent", "")
    sid = req.cookies.get("sid", "")
    return ua + "|" + sid
`)
	h, next := newHandler(t, dir)
	headers := http.Header{}
	headers.Set("User-Agent", "test/1.0")
	headers.Set("Cookie", "sid=abc123")
	r := makeRequest("GET", "/h.star", "", headers)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), r)
	if w.Body.String() != "test/1.0|abc123" {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestForm(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "f.star", `
def respond(req):
    return req.form.get("a", "") + "," + req.form.get("b", "")
`)
	h, next := newHandler(t, dir)
	headers := http.Header{}
	headers.Set("Content-Type", "application/x-www-form-urlencoded")
	r := makeRequest("POST", "/f.star", "a=1&b=two", headers)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), r)
	if w.Body.String() != "1,two" {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestJSONBody(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "j.star", `
def respond(req):
    body = req.json()
    return Response(
        '{"hi":"' + body["name"] + '"}',
        content_type="application/json",
    )
`)
	h, next := newHandler(t, dir)
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	r := makeRequest("POST", "/j.star", `{"name":"world"}`, headers)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), r)
	if w.Body.String() != `{"hi":"world"}` {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestGetlist(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "g.star", `
def respond(req):
    return ",".join(req.args.getlist("x"))
`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/g.star?x=1&x=2&x=3", "", nil))
	if w.Body.String() != "1,2,3" {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestPathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeScript(t, outside, "secret.star", `def respond(req): return "leaked"`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/../"+filepath.Base(outside)+"/secret.star", "", nil))
	if next.n != 1 {
		t.Fatalf("traversal should have passed through; got body=%q code=%d", w.Body.String(), w.Code)
	}
}

func TestCaching(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "c.star", `def respond(req): return "v1"`)
	h, next := newHandler(t, dir)

	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/c.star", "", nil))
	if w.Body.String() != "v1" {
		t.Fatalf("first call body = %q", w.Body.String())
	}
	// Second call: should hit cache (still v1).
	w = serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/c.star", "", nil))
	if w.Body.String() != "v1" {
		t.Fatalf("cached call body = %q", w.Body.String())
	}
}

func TestMaxBodySizeDefault(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "b.star", `def respond(req): return str(len(req.data))`)
	h, next := newHandler(t, dir)
	// 1 KiB POST: well under default 4 MiB.
	body := strings.Repeat("a", 1024)
	headers := http.Header{}
	headers.Set("Content-Type", "text/plain")
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("POST", "/b.star", body, headers))
	if w.Body.String() != "1024" {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestMaxBodySizeExceeded(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "b.star", `def respond(req): return str(len(req.data))`)
	h, next := newHandler(t, dir)
	h.MaxBodySize = 16
	headers := http.Header{}
	headers.Set("Content-Type", "text/plain")
	r := makeRequest("POST", "/b.star", strings.Repeat("a", 64), headers)
	w := httptest.NewRecorder()
	err := h.ServeHTTP(w, r, caddyhttp.HandlerFunc(next.ServeHTTP))
	he, ok := err.(caddyhttp.HandlerError)
	if !ok {
		t.Fatalf("expected HandlerError, got %T %v", err, err)
	}
	if he.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", he.StatusCode)
	}
}

func TestMaxBodySizeUnlimited(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "b.star", `def respond(req): return str(len(req.data))`)
	h, next := newHandler(t, dir)
	h.MaxBodySize = -1
	headers := http.Header{}
	headers.Set("Content-Type", "text/plain")
	body := strings.Repeat("a", 8<<20) // 8 MiB, larger than default 4 MiB
	r := makeRequest("POST", "/b.star", body, headers)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP), r)
	if w.Body.String() != "8388608" {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestEscapeAllChars(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "e.star", `def respond(req): return escape("<a href=\"x\">'&'</a>")`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/e.star", "", nil))
	want := "&lt;a href=&#34;x&#34;&gt;&#39;&amp;&#39;&lt;/a&gt;"
	if w.Body.String() != want {
		t.Fatalf("body = %q, want %q", w.Body.String(), want)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestEscapeIdempotentOnMarkup(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "e.star", `def respond(req): return escape(escape("<x>"))`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/e.star", "", nil))
	if w.Body.String() != "&lt;x&gt;" {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestHtmlFormatter(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "h.star", `
def respond(req):
    return html(
        "<p>Hello, {name}! You said: {msg}</p>",
        name=req.args.get("name", ""),
        msg=req.args.get("msg", ""),
    )
`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/h.star?name=<script>alert(1)</script>&msg=A%26B", "", nil))
	want := "<p>Hello, &lt;script&gt;alert(1)&lt;/script&gt;! You said: A&amp;B</p>"
	if w.Body.String() != want {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestHtmlPassesMarkupThrough(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "h.star", `
def respond(req):
    safe = markup("<b>bold</b>")
    user = "<i>italic</i>"
    return html("<p>{safe} vs {user}</p>", safe=safe, user=user)
`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/h.star", "", nil))
	want := "<p><b>bold</b> vs &lt;i&gt;italic&lt;/i&gt;</p>"
	if w.Body.String() != want {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestHtmlComposes(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "h.star", `
def respond(req):
    inner = html("<p>{x}</p>", x="<a>")
    return html("<div>{i}</div>", i=inner)
`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/h.star", "", nil))
	want := "<div><p>&lt;a&gt;</p></div>"
	if w.Body.String() != want {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestSetCookieSimple(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "c.star", `
def respond(req):
    r = Response("ok")
    r.set_cookie("sid", "abc123")
    return r
`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/c.star", "", nil))
	if got := w.Header().Get("Set-Cookie"); got != "sid=abc123" {
		t.Fatalf("Set-Cookie = %q", got)
	}
}

func TestSetCookieAttributes(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "c.star", `
def respond(req):
    r = Response("ok")
    r.set_cookie("sid", "abc",
                 max_age=3600,
                 path="/app",
                 domain="example.com",
                 secure=True,
                 httponly=True,
                 samesite="strict")
    return r
`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/c.star", "", nil))
	got := w.Header().Get("Set-Cookie")
	for _, want := range []string{"sid=abc", "Max-Age=3600", "Path=/app",
		"Domain=example.com", "HttpOnly", "Secure", "SameSite=Strict"} {
		if !strings.Contains(got, want) {
			t.Errorf("Set-Cookie %q missing %q", got, want)
		}
	}
}

func TestSetCookieMultiple(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "c.star", `
def respond(req):
    r = Response("ok")
    r.set_cookie("a", "1")
    r.set_cookie("b", "2", samesite="lax")
    return r
`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/c.star", "", nil))
	cookies := w.Header()["Set-Cookie"]
	if len(cookies) != 2 {
		t.Fatalf("got %d Set-Cookie headers, want 2: %v", len(cookies), cookies)
	}
	if cookies[0] != "a=1" {
		t.Errorf("cookies[0] = %q", cookies[0])
	}
	if !strings.Contains(cookies[1], "b=2") || !strings.Contains(cookies[1], "SameSite=Lax") {
		t.Errorf("cookies[1] = %q", cookies[1])
	}
}

func TestSetCookieExpires(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "c.star", `
def respond(req):
    r = Response("ok")
    r.set_cookie("sid", "abc", expires=time.from_timestamp(0))
    return r
`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/c.star", "", nil))
	got := w.Header().Get("Set-Cookie")
	if !strings.Contains(got, "Expires=Thu, 01 Jan 1970") {
		t.Fatalf("Set-Cookie %q missing Expires=Thu, 01 Jan 1970", got)
	}
}

func TestQuoteUnquoteRoundtrip(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "u.star", `
def respond(req):
    s = "a b/c?d=&%"
    q = quote(s)
    return q + "|" + unquote(q)
`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/u.star", "", nil))
	want := "a%20b/c%3Fd%3D%26%25|a b/c?d=&%"
	if w.Body.String() != want {
		t.Fatalf("body = %q, want %q", w.Body.String(), want)
	}
}

func TestUrlencodeDict(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "u.star", `def respond(req): return urlencode({"q": "hello world", "lang": "en"})`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/u.star", "", nil))
	// url.Values.Encode sorts keys.
	if w.Body.String() != "lang=en&q=hello+world" {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestUrlencodeMultiDict(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "u.star", `def respond(req): return urlencode(req.args)`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/u.star?x=1&x=2&y=hello+world", "", nil))
	// Sorted by key, with space → +.
	if w.Body.String() != "x=1&x=2&y=hello+world" {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestUrlencodeListValues(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "u.star", `def respond(req): return urlencode({"x": [1, 2, 3], "y": "z"})`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/u.star", "", nil))
	if w.Body.String() != "x=1&x=2&x=3&y=z" {
		t.Fatalf("body = %q", w.Body.String())
	}
}

// makeMultipart builds a multipart/form-data body. fields are
// (name, value) pairs; files are (name, filename, content) triples.
func makeMultipart(t *testing.T, fields [][2]string, files [][3]string) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		fw, err := mw.CreateFormFile(f[0], f[1])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(f[2])); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String(), mw.FormDataContentType()
}

func TestFilesSingle(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "u.star", `
def respond(req):
    f = req.files.get("upload")
    return f.filename + "|" + str(f.size) + "|" + str(f.read())
`)
	body, ct := makeMultipart(t, nil, [][3]string{{"upload", "hi.txt", "hello world"}})
	headers := http.Header{}
	headers.Set("Content-Type", ct)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("POST", "/u.star", body, headers))
	if !strings.HasPrefix(w.Body.String(), "hi.txt|11|") {
		t.Fatalf("body = %q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "hello world") {
		t.Errorf("missing payload: %q", w.Body.String())
	}
}

func TestFilesMultiple(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "u.star", `
def respond(req):
    fs = req.files.getlist("doc")
    parts = [f.filename + "=" + str(f.read()) for f in fs]
    return "|".join(parts)
`)
	body, ct := makeMultipart(t, nil, [][3]string{
		{"doc", "a.txt", "AAA"},
		{"doc", "b.txt", "BBB"},
	})
	headers := http.Header{}
	headers.Set("Content-Type", ct)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("POST", "/u.star", body, headers))
	got := w.Body.String()
	if !strings.Contains(got, "a.txt=") || !strings.Contains(got, "AAA") ||
		!strings.Contains(got, "b.txt=") || !strings.Contains(got, "BBB") {
		t.Fatalf("body = %q", got)
	}
}

func TestFilesAndFormFields(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "u.star", `
def respond(req):
    f = req.files.get("avatar")
    return req.form.get("name", "?") + ":" + f.filename + ":" + str(f.read())
`)
	body, ct := makeMultipart(t,
		[][2]string{{"name", "alice"}},
		[][3]string{{"avatar", "a.png", "PNGDATA"}})
	headers := http.Header{}
	headers.Set("Content-Type", ct)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("POST", "/u.star", body, headers))
	if !strings.Contains(w.Body.String(), "alice:a.png:") || !strings.Contains(w.Body.String(), "PNGDATA") {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestFilesEmptyOnNonMultipart(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "u.star", `def respond(req): return str(len(req.files))`)
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("GET", "/u.star", "", nil))
	if w.Body.String() != "0" {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestFilesContentType(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "u.star", `def respond(req): return req.files.get("f").content_type`)
	// Manually craft so we can set the part's Content-Type.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", `form-data; name="f"; filename="x.json"`)
	hdr.Set("Content-Type", "application/json")
	pw, err := mw.CreatePart(hdr)
	if err != nil {
		t.Fatal(err)
	}
	pw.Write([]byte(`{}`))
	mw.Close()
	headers := http.Header{}
	headers.Set("Content-Type", mw.FormDataContentType())
	h, next := newHandler(t, dir)
	w := serve(t, h, caddyhttp.HandlerFunc(next.ServeHTTP),
		makeRequest("POST", "/u.star", buf.String(), headers))
	if w.Body.String() != "application/json" {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestMissingEntrypoint(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "broken.star", `x = 1`)
	h, next := newHandler(t, dir)
	w := httptest.NewRecorder()
	err := h.ServeHTTP(w, makeRequest("GET", "/broken.star", "", nil), caddyhttp.HandlerFunc(next.ServeHTTP))
	he, ok := err.(caddyhttp.HandlerError)
	if !ok || he.StatusCode != 500 {
		t.Fatalf("expected 500 HandlerError, got %v", err)
	}
}
