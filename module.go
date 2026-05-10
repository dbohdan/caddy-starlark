// Package starlark provides a Caddy HTTP handler that executes Starlark
// scripts as request handlers. The API exposed inside scripts is modeled
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
	"go.starlark.net/lib/math"
	startime "go.starlark.net/lib/time"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkjson"
	"go.starlark.net/starlarkstruct"
	"go.uber.org/zap"
)

// DefaultMaxBodySize is the request-body cap used when none is configured.
const DefaultMaxBodySize int64 = 4 << 20 // 4 MiB

func init() {
	caddy.RegisterModule(Handler{})
	httpcaddyfile.RegisterHandlerDirective("starlark", parseCaddyfile)
}

// Handler executes Starlark scripts to serve HTTP responses.
//
// For each request, the handler resolves the request path against Root
// (using the configured Extension to fill in a default file extension),
// parses the script, and calls a top-level Entrypoint function (default
// "respond") with a Flask-style request object. The function's return
// value becomes the HTTP response.
type Handler struct {
	// Root is the directory containing .star scripts. Caddy placeholders
	// are expanded. Defaults to "{http.vars.root}" if set, otherwise ".".
	Root string `json:"root,omitempty"`

	// Extension is appended when the URL path has none, and is also the
	// only extension that this handler will execute (other paths fall
	// through to the next handler). Default ".star".
	Extension string `json:"extension,omitempty"`

	// Entrypoint is the name of the top-level function called per request.
	// Default "respond".
	Entrypoint string `json:"entrypoint,omitempty"`

	// Index is the script name resolved when the path ends in "/".
	// Default "index.star".
	Index string `json:"index,omitempty"`

	// CacheScripts caches parsed scripts in memory keyed by absolute
	// path and modification time. Default true.
	CacheScripts *bool `json:"cache_scripts,omitempty"`

	// MaxBodySize caps the request body in bytes. Reads beyond this
	// limit fail and the handler returns HTTP 413. Defaults to 4 MiB.
	// Set to a negative value to disable the cap.
	MaxBodySize int64 `json:"max_body_size,omitempty"`

	logger *zap.Logger
	cache  *programCache
}

type programCache struct {
	mu  sync.RWMutex
	m   map[string]cachedProgram
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
	if h.Entrypoint == "" {
		h.Entrypoint = "respond"
	}
	if h.Index == "" {
		h.Index = "index" + h.Extension
	}
	if h.CacheScripts == nil {
		t := true
		h.CacheScripts = &t
	}
	if h.MaxBodySize == 0 {
		h.MaxBodySize = DefaultMaxBodySize
	}
	h.cache = &programCache{m: make(map[string]cachedProgram)}
	return nil
}

// Validate ensures the configuration is sane.
func (h *Handler) Validate() error {
	if strings.ContainsRune(h.Entrypoint, '/') {
		return fmt.Errorf("entrypoint must be a bare identifier")
	}
	return nil
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	repl := r.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)
	root := repl.ReplaceAll(h.Root, ".")

	scriptPath, err := h.resolveScript(root, r.URL.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return next.ServeHTTP(w, r)
		}
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}

	prog, err := h.loadProgram(scriptPath)
	if err != nil {
		h.logger.Error("starlark parse error",
			zap.String("script", scriptPath),
			zap.Error(err))
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}

	if h.MaxBodySize > 0 && r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, h.MaxBodySize)
	}

	thread := &starlark.Thread{
		Name: "caddy-starlark:" + scriptPath,
		Print: func(_ *starlark.Thread, msg string) {
			h.logger.Info("starlark print",
				zap.String("script", scriptPath),
				zap.String("msg", msg))
		},
		Load: makeLoader(filepath.Dir(scriptPath)),
	}

	predeclared := buildPredeclared(repl)
	globals, err := prog.Init(thread, predeclared)
	if err != nil {
		return caddyhttp.Error(http.StatusInternalServerError,
			fmt.Errorf("initializing %s: %w", scriptPath, err))
	}
	globals.Freeze()

	entry, ok := globals[h.Entrypoint]
	if !ok {
		return caddyhttp.Error(http.StatusInternalServerError,
			fmt.Errorf("%s does not define %q", scriptPath, h.Entrypoint))
	}
	callable, ok := entry.(starlark.Callable)
	if !ok {
		return caddyhttp.Error(http.StatusInternalServerError,
			fmt.Errorf("%s: %q is not callable", scriptPath, h.Entrypoint))
	}

	req := newRequestValue(r)
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
			zap.String("script", scriptPath),
			zap.Error(err))
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}

	return writeResponse(w, result)
}

// resolveScript maps a URL path to a .star file under root. It returns
// fs.ErrNotExist when nothing matches so the request can fall through.
func (h *Handler) resolveScript(root, urlPath string) (string, error) {
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

func (h *Handler) loadProgram(scriptPath string) (*starlark.Program, error) {
	info, err := os.Stat(scriptPath)
	if err != nil {
		return nil, err
	}
	mtime := info.ModTime().UnixNano()

	if h.CacheScripts != nil && *h.CacheScripts {
		h.cache.mu.RLock()
		cached, ok := h.cache.m[scriptPath]
		h.cache.mu.RUnlock()
		if ok && cached.modTime == mtime {
			return cached.mod, nil
		}
	}

	src, err := os.ReadFile(scriptPath)
	if err != nil {
		return nil, err
	}
	_, prog, err := starlark.SourceProgram(scriptPath, src, func(name string) bool {
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

	if h.CacheScripts != nil && *h.CacheScripts {
		h.cache.mu.Lock()
		h.cache.m[scriptPath] = cachedProgram{mod: prog, modTime: mtime}
		h.cache.mu.Unlock()
	}
	return prog, nil
}

// makeLoader implements relative `load("foo.star", ...)` from the script's
// own directory. It does not cache; users wanting caching can split scripts
// into separate top-level routes.
func makeLoader(baseDir string) func(*starlark.Thread, string) (starlark.StringDict, error) {
	return func(thread *starlark.Thread, module string) (starlark.StringDict, error) {
		full := filepath.Join(baseDir, filepath.FromSlash(module))
		src, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		return starlark.ExecFile(thread, full, src, buildPredeclaredNoRepl())
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
		"Response": starlark.NewBuiltin("Response", responseBuiltin),
		"redirect": starlark.NewBuiltin("redirect", redirectBuiltin),
		"abort":    starlark.NewBuiltin("abort", abortBuiltin),
		"escape":   starlark.NewBuiltin("escape", escapeBuiltin),
		"markup":   starlark.NewBuiltin("markup", markupBuiltin),
		"html":     starlark.NewBuiltin("html", htmlBuiltin),
		"quote":    starlark.NewBuiltin("quote", quoteBuiltin),
		"unquote":  starlark.NewBuiltin("unquote", unquoteBuiltin),
		"urlencode": starlark.NewBuiltin("urlencode", urlencodeBuiltin),
		"json":     starlarkjson.Module,
		"time":     startime.Module,
		"math":     math.Module,
		"struct":   starlark.NewBuiltin("struct", starlarkstruct.Make),
	}
}

// parseCaddyfile parses the `starlark` directive. Syntax:
//
//	starlark [<matcher>] {
//	    root <path>
//	    extension <ext>
//	    entrypoint <name>
//	    index <filename>
//	    cache_scripts <true|false>
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
			case "entrypoint":
				if !h.Args(&hand.Entrypoint) {
					return nil, h.ArgErr()
				}
			case "index":
				if !h.Args(&hand.Index) {
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
			case "cache_scripts":
				var v string
				if !h.Args(&v) {
					return nil, h.ArgErr()
				}
				switch strings.ToLower(v) {
				case "true", "on", "yes", "1":
					t := true
					hand.CacheScripts = &t
				case "false", "off", "no", "0":
					f := false
					hand.CacheScripts = &f
				default:
					return nil, h.Errf("invalid cache_scripts value %q", v)
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
