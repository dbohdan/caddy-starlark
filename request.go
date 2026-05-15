package starlark

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	starlarkjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

// Request is the Starlark value passed to the view's entry point. The
// surface mirrors flask.request:
//
//	request.method        - HTTP method
//	request.path          - URL path
//	request.full_path     - URL path + "?" + raw query
//	request.url           - full URL
//	request.host          - Host header value
//	request.scheme        - "http" or "https"
//	request.remote_addr   - client IP (no port)
//	request.args          - query parameters (multi-dict)
//	request.headers       - request headers (multi-dict)
//	request.cookies       - cookies as a dict[str,str]
//	request.form          - parsed form body (multi-dict)
//	request.values        - args + form combined (multi-dict)
//	request.data          - raw request body (bytes)
//	request.json()        - parse the body as JSON
//	request.get_header(k) - case-insensitive header lookup
type Request struct {
	r *http.Request

	once     sync.Once
	body     []byte
	bodyErr  error
	formOnce sync.Once
	form     *multiDict

	multipartOnce sync.Once
	multipartErr  error
	files         *filesDict
	multipartForm *multiDict // form fields parsed from multipart parts

	maxBodySize int64

	// Session state, populated lazily on first access to request.session.
	sessionOnce      sync.Once
	sessionErr       error
	sessionData      *starlark.Dict // current; user mutates this
	sessionOriginal  *starlark.Dict // snapshot of decoded cookie (read-only after init)
	sessionThread    *starlark.Thread
	sessionSecret    []byte
	sessionCookieKey string
}

func newRequestValue(r *http.Request) *Request { return &Request{r: r} }

func (req *Request) setLimits(maxBody int64) { req.maxBodySize = maxBody }

// configureSessions enables request.session access. secret nil/empty
// disables sessions: any access raises an error.
func (req *Request) configureSessions(thread *starlark.Thread, cookieName string, secret []byte) {
	req.sessionThread = thread
	req.sessionCookieKey = cookieName
	req.sessionSecret = secret
}

func (req *Request) String() string {
	return fmt.Sprintf("<Request %s %s>", req.r.Method, req.r.URL.Path)
}
func (req *Request) Type() string          { return "Request" }
func (req *Request) Freeze()               {}
func (req *Request) Truth() starlark.Bool  { return starlark.True }
func (req *Request) Hash() (uint32, error) { return 0, fmt.Errorf("Request is unhashable") }

func (req *Request) readBody() ([]byte, error) {
	req.once.Do(func() {
		if req.r.Body == nil {
			return
		}
		req.body, req.bodyErr = io.ReadAll(req.r.Body)
		_ = req.r.Body.Close()
	})
	return req.body, req.bodyErr
}

func (req *Request) parsedForm() (*multiDict, error) {
	ct := req.r.Header.Get("Content-Type")
	mediaType := ct
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = strings.TrimSpace(mediaType[:i])
	}
	if mediaType == "multipart/form-data" {
		if err := req.parseMultipart(); err != nil {
			return nil, err
		}
		return req.multipartForm, nil
	}
	req.formOnce.Do(func() {
		body, _ := req.readBody()
		md := newMultiDict()
		if mediaType == "application/x-www-form-urlencoded" {
			if vals, err := url.ParseQuery(string(body)); err == nil {
				for k, vs := range vals {
					for _, v := range vs {
						md.add(k, v)
					}
				}
			}
		}
		req.form = md
	})
	return req.form, nil
}

const multipartMemoryCap = 32 << 20 // 32 MiB

func (req *Request) parseMultipart() error {
	req.multipartOnce.Do(func() {
		ct := req.r.Header.Get("Content-Type")
		mediaType := ct
		if i := strings.IndexByte(mediaType, ';'); i >= 0 {
			mediaType = strings.TrimSpace(mediaType[:i])
		}
		if mediaType != "multipart/form-data" {
			// Not a multipart request: expose empty files / form rather
			// than surfacing ParseMultipartForm's content-type error.
			req.multipartForm = newMultiDict()
			req.files = newFilesDict()
			return
		}
		maxMem := int64(multipartMemoryCap)
		if req.maxBodySize > 0 && req.maxBodySize < maxMem {
			maxMem = req.maxBodySize
		}
		if err := req.r.ParseMultipartForm(maxMem); err != nil {
			req.multipartErr = err
			return
		}
		mf := req.r.MultipartForm
		fields := newMultiDict()
		files := newFilesDict()
		if mf != nil {
			for k, vs := range mf.Value {
				for _, v := range vs {
					fields.add(k, v)
				}
			}
			for k, headers := range mf.File {
				for _, h := range headers {
					files.add(k, newFileStorage(k, h))
				}
			}
		}
		req.multipartForm = fields
		req.files = files
	})
	return req.multipartErr
}

// Attr exposes the Flask-style fields.
func (req *Request) Attr(name string) (starlark.Value, error) {
	switch name {
	case "method":
		return starlark.String(req.r.Method), nil
	case "path":
		return starlark.String(req.r.URL.Path), nil
	case "full_path":
		if req.r.URL.RawQuery != "" {
			return starlark.String(req.r.URL.Path + "?" + req.r.URL.RawQuery), nil
		}
		return starlark.String(req.r.URL.Path), nil
	case "query_string":
		return starlark.String(req.r.URL.RawQuery), nil
	case "url":
		u := *req.r.URL
		if u.Host == "" {
			u.Host = req.r.Host
		}
		if u.Scheme == "" {
			if req.r.TLS != nil {
				u.Scheme = "https"
			} else {
				u.Scheme = "http"
			}
		}
		return starlark.String(u.String()), nil
	case "host":
		return starlark.String(req.r.Host), nil
	case "scheme":
		if req.r.TLS != nil {
			return starlark.String("https"), nil
		}
		return starlark.String("http"), nil
	case "remote_addr":
		host, _, err := net.SplitHostPort(req.r.RemoteAddr)
		if err != nil {
			return starlark.String(req.r.RemoteAddr), nil
		}
		return starlark.String(host), nil
	case "args":
		md := newMultiDict()
		for k, vs := range req.r.URL.Query() {
			for _, v := range vs {
				md.add(k, v)
			}
		}
		return md, nil
	case "headers":
		md := newMultiDict()
		md.caseInsensitive = true
		for k, vs := range req.r.Header {
			for _, v := range vs {
				md.add(k, v)
			}
		}
		return md, nil
	case "cookies":
		d := starlark.NewDict(0)
		for _, c := range req.r.Cookies() {
			_ = d.SetKey(starlark.String(c.Name), starlark.String(c.Value))
		}
		return d, nil
	case "form":
		f, err := req.parsedForm()
		if err != nil {
			return nil, err
		}
		return f, nil
	case "files":
		if err := req.parseMultipart(); err != nil {
			return nil, err
		}
		if req.files == nil {
			return newFilesDict(), nil
		}
		return req.files, nil
	case "values":
		merged := newMultiDict()
		for k, vs := range req.r.URL.Query() {
			for _, v := range vs {
				merged.add(k, v)
			}
		}
		f, err := req.parsedForm()
		if err != nil {
			return nil, err
		}
		for _, kv := range f.items {
			merged.add(kv.key, kv.val)
		}
		return merged, nil
	case "data":
		body, err := req.readBody()
		if err != nil {
			return nil, err
		}
		return starlark.Bytes(body), nil
	case "content_length":
		return starlark.MakeInt64(req.r.ContentLength), nil
	case "content_type":
		return starlark.String(req.r.Header.Get("Content-Type")), nil
	case "session":
		return req.session()
	case "json":
		return starlark.NewBuiltin("json", req.jsonBuiltin), nil
	case "get_header":
		return starlark.NewBuiltin("get_header", req.getHeaderBuiltin), nil
	}
	return nil, nil
}

func (req *Request) AttrNames() []string {
	return []string{
		"args", "content_length", "content_type", "cookies", "data", "files",
		"form", "full_path", "get_header", "headers", "host", "json", "method",
		"path", "query_string", "remote_addr", "scheme", "session", "url", "values",
	}
}

// session returns the live, mutable Starlark dict for the request session.
// It's an error if sessions weren't configured (no secret_key set).
func (req *Request) session() (starlark.Value, error) {
	req.sessionOnce.Do(func() {
		if len(req.sessionSecret) == 0 {
			req.sessionErr = fmt.Errorf("sessions are not configured (set secret_key in the Caddyfile)")
			return
		}
		current := starlark.NewDict(0)
		if c, err := req.r.Cookie(req.sessionCookieKey); err == nil {
			if decoded, _ := decodeSession(req.sessionThread, c.Value, req.sessionSecret); decoded != nil {
				current = decoded
			}
		}
		req.sessionData = current
		// Snapshot the original by deep-copying via JSON round-trip,
		// so later comparison sees only what changed.
		if js, err := dictToJSON(req.sessionThread, current); err == nil {
			if orig, _ := jsonToDict(req.sessionThread, js); orig != nil {
				req.sessionOriginal = orig
			}
		}
	})
	if req.sessionErr != nil {
		return nil, req.sessionErr
	}
	return req.sessionData, nil
}

func (req *Request) jsonBuiltin(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 || len(kwargs) > 0 {
		return nil, fmt.Errorf("json: takes no arguments")
	}
	body, err := req.readBody()
	if err != nil {
		return nil, err
	}
	decode, _ := starlarkjson.Module.Attr("decode")
	return starlark.Call(thread, decode, starlark.Tuple{starlark.String(body)}, nil)
}

func (req *Request) getHeaderBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name string
	var dflt starlark.Value = starlark.None
	if err := starlark.UnpackPositionalArgs("get_header", args, kwargs, 1, &name, &dflt); err != nil {
		return nil, err
	}
	if v := req.r.Header.Get(name); v != "" {
		return starlark.String(v), nil
	}
	return dflt, nil
}

var _ starlark.HasAttrs = (*Request)(nil)
