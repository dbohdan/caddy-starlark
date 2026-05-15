// Package starlark provides a Caddy HTTP handler that executes Starlark
// program as request handlers. The API exposed inside is modeled
// on Flask: a top-level respond(request) function is called for each
// request, and may return a string, a Response, or a (body, status[, headers])
// tuple.
package starlark

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/dustin/go-humanize"
	starlarkjson "go.starlark.net/lib/json"
	"go.starlark.net/lib/math"
	starlarktime "go.starlark.net/lib/time"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"
	"go.uber.org/zap"
)

// DefaultMaxBodySize is the request-body cap used when none is configured.
const DefaultMaxBodySize int64 = 4 << 20 // 4 MiB

func init() {
	caddy.RegisterModule(Handler{})
	httpcaddyfile.RegisterHandlerDirective("starlark", parseCaddyfile)
}

// Handler executes Starlark programs to serve HTTP responses.
//
// For each request, the handler resolves the request path against Root
// (using the configured Extension to fill in a default file extension),
// parses the program, and calls a top-level EntryPoint function (default
// "respond") with a Flask-style request object. The function's return
// value becomes the HTTP response.
type Handler struct {
	// Root is the directory containing .star files. Caddy placeholders
	// are expanded. Defaults to "{http.vars.root}" if set, otherwise ".".
	Root string `json:"root,omitempty"`

	// Extension is appended when the URL path has none, and is also the
	// only extension that this handler will execute (other paths fall
	// through to the next handler). Default ".star".
	Extension string `json:"extension,omitempty"`

	// EntryPoint is the name of the top-level function called per request.
	// Default "respond".
	EntryPoint string `json:"entry_point,omitempty"`

	// Index is the view name resolved when the path ends in "/".
	// Default "index.star".
	Index string `json:"index,omitempty"`

	// CachePrograms caches parsed programs in memory keyed by absolute
	// path and modification time. Default true.
	CachePrograms *bool `json:"cache_programs,omitempty"`

	// MaxBodySize caps the request body in bytes. Reads beyond this
	// limit fail and the handler returns HTTP 413. Defaults to 4 MiB.
	// Set to a negative value to disable the cap.
	MaxBodySize int64 `json:"max_body_size,omitempty"`

	// SecretKey enables signed-cookie sessions when non-empty. Used to
	// sign and verify the session cookie's contents (HMAC-SHA256).
	SecretKey string `json:"secret_key,omitempty"`

	// SessionCookieName overrides the default "session" cookie name.
	SessionCookieName string `json:"session_cookie_name,omitempty"`

	logger *zap.Logger
	cache  *programCache
}

type programCache struct {
	mu sync.RWMutex
	m  map[string]cachedProgram
}

type cachedProgram struct {
	mod     *starlark.Program
	modTime int64
}

// CaddyModule returns the Caddy module information.
func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.starlark",
		New: func() caddy.Module { return new(Handler) },
	}
}

// Provision sets up the handler.
func (h *Handler) Provision(ctx caddy.Context) error {
	h.logger = ctx.Logger()
	if h.Root == "" {
		h.Root = "{http.vars.root}"
	}
	if h.Extension == "" {
		h.Extension = ".star"
	}
	if !strings.HasPrefix(h.Extension, ".") {
		h.Extension = "." + h.Extension
	}
	if h.EntryPoint == "" {
		h.EntryPoint = "respond"
	}
	if h.Index == "" {
		h.Index = "index" + h.Extension
	}
	if h.CachePrograms == nil {
		t := true
		h.CachePrograms = &t
	}
	if h.MaxBodySize == 0 {
		h.MaxBodySize = DefaultMaxBodySize
	}
	if h.SessionCookieName == "" {
		h.SessionCookieName = "session"
	}
	if h.SecretKey != "" && len(h.SecretKey) < 32 {
		h.logger.Warn("secret_key is shorter than 32 bytes; HMAC-SHA256 is meaningfully weaker with short keys",
			zap.Int("length", len(h.SecretKey)))
	}
	h.cache = &programCache{m: make(map[string]cachedProgram)}
	return nil
}

// Validate ensures the configuration is sane.
func (h *Handler) Validate() error {
	if strings.ContainsRune(h.EntryPoint, '/') {
		return fmt.Errorf("entry_point must be a bare identifier")
	}
	return nil
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	repl := r.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)
	root := repl.ReplaceAll(h.Root, ".")

	viewPath, err := h.resolveView(root, r.URL.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return next.ServeHTTP(w, r)
		}
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}

	prog, err := h.loadProgram(viewPath)
	if err != nil {
		h.logger.Error("starlark parse error",
			zap.String("view", viewPath),
			zap.Error(err))
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}

	if h.MaxBodySize > 0 && r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, h.MaxBodySize)
	}

	thread := &starlark.Thread{
		Name: "caddy-starlark:" + viewPath,
		Print: func(_ *starlark.Thread, msg string) {
			h.logger.Info("starlark print",
				zap.String("view", viewPath),
				zap.String("msg", msg))
		},
		Load: makeLoader(filepath.Dir(viewPath)),
	}

	predeclared := buildPredeclared(repl)
	globals, err := prog.Init(thread, predeclared)
	if err != nil {
		return caddyhttp.Error(http.StatusInternalServerError,
			fmt.Errorf("initializing %s: %w", viewPath, err))
	}
	globals.Freeze()

	entry, ok := globals[h.EntryPoint]
	if !ok {
		return caddyhttp.Error(http.StatusInternalServerError,
			fmt.Errorf("%s does not define %q", viewPath, h.EntryPoint))
	}
	callable, ok := entry.(starlark.Callable)
	if !ok {
		return caddyhttp.Error(http.StatusInternalServerError,
			fmt.Errorf("%s: %q is not callable", viewPath, h.EntryPoint))
	}

	req := newRequestValue(r)
	req.setLimits(h.MaxBodySize)
	if h.SecretKey != "" {
		req.configureSessions(thread, h.SessionCookieName, []byte(h.SecretKey))
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	result, err := starlark.Call(thread, callable, starlark.Tuple{req}, nil)
	if err != nil {
		if abortErr, ok := isAbort(err); ok {
			return caddyhttp.Error(abortErr.status, abortErr)
		}
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return caddyhttp.Error(http.StatusRequestEntityTooLarge, err)
		}
		h.logger.Error("starlark execution error",
			zap.String("view", viewPath),
			zap.Error(err))
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}

	if req.sessionData != nil {
		if err := writeSessionCookie(w, r, thread, h.SessionCookieName,
			[]byte(h.SecretKey), req.sessionOriginal, req.sessionData); err != nil {
			h.logger.Error("session write error",
				zap.String("view", viewPath),
				zap.Error(err))
			return caddyhttp.Error(http.StatusInternalServerError, err)
		}
	}

	if err := writeResponse(w, result); err != nil {
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}
	return nil
}

// resolveView maps a URL path to a .star file under root. It returns
// fs.ErrNotExist when nothing matches so the request can fall through.
func (h *Handler) resolveView(root, urlPath string) (string, error) {
	clean := path.Clean("/" + urlPath)
	if strings.HasSuffix(urlPath, "/") || urlPath == "" {
		clean = path.Join(clean, h.Index)
	} else if path.Ext(clean) == "" {
		clean += h.Extension
	}
	if path.Ext(clean) != h.Extension {
		return "", fs.ErrNotExist
	}

	full := filepath.Join(root, filepath.FromSlash(clean))
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, absFull)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fs.ErrNotExist
	}

	info, err := os.Stat(absFull)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fs.ErrNotExist
	}
	return absFull, nil
}

func (h *Handler) loadProgram(viewPath string) (*starlark.Program, error) {
	info, err := os.Stat(viewPath)
	if err != nil {
		return nil, err
	}
	mtime := info.ModTime().UnixNano()

	if h.CachePrograms != nil && *h.CachePrograms {
		h.cache.mu.RLock()
		cached, ok := h.cache.m[viewPath]
		h.cache.mu.RUnlock()
		if ok && cached.modTime == mtime {
			return cached.mod, nil
		}
	}

	src, err := os.ReadFile(viewPath)
	if err != nil {
		return nil, err
	}
	_, prog, err := starlark.SourceProgramOptions(&syntax.FileOptions{}, viewPath, src, func(name string) bool {
		// Predeclared identifiers — anything in buildPredeclared.
		switch name {
		case "Response", "redirect", "abort", "placeholder", "ph",
			"escape", "markup", "html",
			"quote", "unquote", "urlencode",
			"json", "time", "math", "struct":
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}

	if h.CachePrograms != nil && *h.CachePrograms {
		h.cache.mu.Lock()
		h.cache.m[viewPath] = cachedProgram{mod: prog, modTime: mtime}
		h.cache.mu.Unlock()
	}
	return prog, nil
}

// makeLoader implements relative `load("foo.star", ...)` from the view's
// own directory. Loaded paths are confined to baseDir so that a view
// can't escape via "../" segments. It does not cache; users wanting
// caching can split view into separate top-level routes.
func makeLoader(baseDir string) func(*starlark.Thread, string) (starlark.StringDict, error) {
	return func(thread *starlark.Thread, module string) (starlark.StringDict, error) {
		full := filepath.Join(baseDir, filepath.FromSlash(module))
		absBase, err := filepath.Abs(baseDir)
		if err != nil {
			return nil, err
		}
		absFull, err := filepath.Abs(full)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(absBase, absFull)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("load: path %q escapes view root", module)
		}
		src, err := os.ReadFile(absFull)
		if err != nil {
			return nil, err
		}
		return starlark.ExecFileOptions(&syntax.FileOptions{}, thread, absFull, src, buildPredeclaredNoRepl())
	}
}

func buildPredeclared(repl *caddy.Replacer) starlark.StringDict {
	d := buildPredeclaredNoRepl()
	d["placeholder"] = makePlaceholder(repl)
	d["ph"] = d["placeholder"]
	return d
}

func buildPredeclaredNoRepl() starlark.StringDict {
	return starlark.StringDict{
		"Response":  starlark.NewBuiltin("Response", responseBuiltin),
		"redirect":  starlark.NewBuiltin("redirect", redirectBuiltin),
		"abort":     starlark.NewBuiltin("abort", abortBuiltin),
		"escape":    starlark.NewBuiltin("escape", escapeBuiltin),
		"markup":    starlark.NewBuiltin("markup", markupBuiltin),
		"html":      starlark.NewBuiltin("html", htmlBuiltin),
		"quote":     starlark.NewBuiltin("quote", quoteBuiltin),
		"unquote":   starlark.NewBuiltin("unquote", unquoteBuiltin),
		"urlencode": starlark.NewBuiltin("urlencode", urlencodeBuiltin),
		"json":      starlarkjson.Module,
		"time":      starlarktime.Module,
		"math":      math.Module,
		"struct":    starlark.NewBuiltin("struct", starlarkstruct.Make),
	}
}

// parseCaddyfile parses the `starlark` directive. Syntax:
//
//	starlark [<matcher>] {
//	    root <path>
//	    extension <ext>
//	    entry_point <name>
//	    index <filename>
//	    cache_programs <true|false>
//	}
func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	hand := new(Handler)
	for h.Next() {
		args := h.RemainingArgs()
		if len(args) == 1 {
			hand.Root = args[0]
		} else if len(args) > 1 {
			return nil, h.ArgErr()
		}
		for h.NextBlock(0) {
			switch h.Val() {
			case "root":
				if !h.Args(&hand.Root) {
					return nil, h.ArgErr()
				}
			case "extension":
				if !h.Args(&hand.Extension) {
					return nil, h.ArgErr()
				}
			case "entry_point":
				if !h.Args(&hand.EntryPoint) {
					return nil, h.ArgErr()
				}
			case "index":
				if !h.Args(&hand.Index) {
					return nil, h.ArgErr()
				}
			case "secret_key":
				if !h.Args(&hand.SecretKey) {
					return nil, h.ArgErr()
				}
			case "session_cookie_name":
				if !h.Args(&hand.SessionCookieName) {
					return nil, h.ArgErr()
				}
			case "max_body_size":
				var sizeStr string
				if !h.Args(&sizeStr) {
					return nil, h.ArgErr()
				}
				if strings.EqualFold(sizeStr, "unlimited") || sizeStr == "-1" {
					hand.MaxBodySize = -1
				} else {
					n, err := humanize.ParseBytes(sizeStr)
					if err != nil {
						return nil, h.Errf("parsing max_body_size: %v", err)
					}
					hand.MaxBodySize = int64(n)
				}
			case "cache_programs":
				var v string
				if !h.Args(&v) {
					return nil, h.ArgErr()
				}
				switch strings.ToLower(v) {
				case "true", "on", "yes", "1":
					t := true
					hand.CachePrograms = &t
				case "false", "off", "no", "0":
					f := false
					hand.CachePrograms = &f
				default:
					return nil, h.Errf("invalid cache_programs value %q", v)
				}
			default:
				return nil, h.Errf("unknown subdirective %q", h.Val())
			}
		}
	}
	return hand, nil
}

// UnmarshalCaddyfile lets the handler be embedded in `route` blocks.
func (h *Handler) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	helper := httpcaddyfile.Helper{Dispenser: d}
	mh, err := parseCaddyfile(helper)
	if err != nil {
		return err
	}
	*h = *(mh.(*Handler))
	return nil
}

var (
	_ caddy.Provisioner           = (*Handler)(nil)
	_ caddy.Validator             = (*Handler)(nil)
	_ caddyhttp.MiddlewareHandler = (*Handler)(nil)
	_ caddyfile.Unmarshaler       = (*Handler)(nil)
)
