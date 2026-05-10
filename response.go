package starlark

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/caddyserver/caddy/v2"
	"go.starlark.net/starlark"
)

// Response is the constructable response value returned to Go from Starlark.
//
//	Response("hi")
//	Response("hi", status=201)
//	Response("hi", status=200, headers={"X-Foo": "bar"})
//	Response(body=b"\x00\x01", status=200, content_type="application/octet-stream")
type Response struct {
	Body        starlark.Value // String or Bytes
	Status      int
	Headers     *starlark.Dict
	ContentType string
}

func (r *Response) String() string {
	return fmt.Sprintf("<Response status=%d>", r.Status)
}
func (r *Response) Type() string          { return "Response" }
func (r *Response) Freeze()               { r.Headers.Freeze() }
func (r *Response) Truth() starlark.Bool  { return starlark.True }
func (r *Response) Hash() (uint32, error) { return 0, fmt.Errorf("Response is unhashable") }

func (r *Response) Attr(name string) (starlark.Value, error) {
	switch name {
	case "body":
		return r.Body, nil
	case "status":
		return starlark.MakeInt(r.Status), nil
	case "headers":
		return r.Headers, nil
	}
	return nil, nil
}
func (r *Response) AttrNames() []string { return []string{"body", "status", "headers"} }

func responseBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var body starlark.Value = starlark.String("")
	status := 200
	var headers *starlark.Dict
	contentType := ""
	if err := starlark.UnpackArgs("Response", args, kwargs,
		"body?", &body,
		"status?", &status,
		"headers?", &headers,
		"content_type?", &contentType,
	); err != nil {
		return nil, err
	}
	switch body.(type) {
	case starlark.String, starlark.Bytes, Markup:
		// ok
	case starlark.NoneType:
		body = starlark.String("")
	default:
		return nil, fmt.Errorf("Response: body must be string, bytes, markup, or None")
	}
	if headers == nil {
		headers = starlark.NewDict(0)
	}
	return &Response{
		Body:        body,
		Status:      status,
		Headers:     headers,
		ContentType: contentType,
	}, nil
}

func redirectBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var location string
	status := 302
	if err := starlark.UnpackArgs("redirect", args, kwargs,
		"location", &location,
		"status?", &status,
	); err != nil {
		return nil, err
	}
	headers := starlark.NewDict(1)
	_ = headers.SetKey(starlark.String("Location"), starlark.String(location))
	return &Response{
		Body:    starlark.String(""),
		Status:  status,
		Headers: headers,
	}, nil
}

// abortError flows up through starlark.Call as a regular error and is then
// recognized by the handler to produce a Caddy HTTP error.
type abortError struct {
	status  int
	message string
}

func (e *abortError) Error() string {
	if e.message != "" {
		return fmt.Sprintf("HTTP %d: %s", e.status, e.message)
	}
	return fmt.Sprintf("HTTP %d", e.status)
}

func isAbort(err error) (*abortError, bool) {
	var ae *abortError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

func abortBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var status int
	var message string
	if err := starlark.UnpackArgs("abort", args, kwargs,
		"status", &status,
		"message?", &message,
	); err != nil {
		return nil, err
	}
	return nil, &abortError{status: status, message: message}
}

func makePlaceholder(repl *caddy.Replacer) *starlark.Builtin {
	return starlark.NewBuiltin("placeholder", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var key string
		var dflt starlark.Value = starlark.String("")
		if err := starlark.UnpackPositionalArgs("placeholder", args, kwargs, 1, &key, &dflt); err != nil {
			return nil, err
		}
		// Allow callers to pass either "{http.request.host}" or "http.request.host".
		stripped := key
		if len(stripped) >= 2 && stripped[0] == '{' && stripped[len(stripped)-1] == '}' {
			stripped = stripped[1 : len(stripped)-1]
		}
		val, ok := repl.Get(stripped)
		if !ok {
			return dflt, nil
		}
		switch v := val.(type) {
		case string:
			return starlark.String(v), nil
		case int:
			return starlark.MakeInt(v), nil
		case int64:
			return starlark.MakeInt64(v), nil
		case float64:
			return starlark.Float(v), nil
		case bool:
			return starlark.Bool(v), nil
		default:
			return starlark.String(fmt.Sprint(v)), nil
		}
	})
}

// writeResponse converts a Starlark return value into an HTTP response.
//
// Supported return shapes (Flask-style):
//   - str / bytes                       → 200 with body
//   - Response(...)                     → as configured
//   - (body, status)                    → body + status
//   - (body, status, headers_dict)      → body + status + headers
//   - dict(body=..., status=..., headers=...)  → as configured
//   - None                              → 204 No Content
func writeResponse(w http.ResponseWriter, v starlark.Value) error {
	resp, err := coerceResponse(v)
	if err != nil {
		return err
	}
	for _, k := range resp.Headers.Keys() {
		ks, ok := k.(starlark.String)
		if !ok {
			return fmt.Errorf("response header keys must be strings, got %s", k.Type())
		}
		val, _, err := resp.Headers.Get(k)
		if err != nil {
			return err
		}
		switch hv := val.(type) {
		case starlark.String:
			w.Header().Set(string(ks), string(hv))
		case *starlark.List:
			it := hv.Iterate()
			defer it.Done()
			var item starlark.Value
			w.Header().Del(string(ks))
			for it.Next(&item) {
				s, ok := item.(starlark.String)
				if !ok {
					return fmt.Errorf("response header %q values must be strings", string(ks))
				}
				w.Header().Add(string(ks), string(s))
			}
		default:
			w.Header().Set(string(ks), val.String())
		}
	}
	if resp.ContentType != "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}
	var body []byte
	switch b := resp.Body.(type) {
	case starlark.String:
		body = []byte(b)
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
	case Markup:
		body = []byte(b)
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
	case starlark.Bytes:
		body = []byte(b)
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
	}
	if w.Header().Get("Content-Length") == "" {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}
	w.WriteHeader(resp.Status)
	_, err = w.Write(body)
	return err
}

func coerceResponse(v starlark.Value) (*Response, error) {
	switch x := v.(type) {
	case *Response:
		return x, nil
	case starlark.String:
		return &Response{Body: x, Status: 200, Headers: starlark.NewDict(0)}, nil
	case Markup:
		return &Response{Body: x, Status: 200, Headers: starlark.NewDict(0)}, nil
	case starlark.Bytes:
		return &Response{Body: x, Status: 200, Headers: starlark.NewDict(0)}, nil
	case starlark.NoneType:
		return &Response{Body: starlark.String(""), Status: 204, Headers: starlark.NewDict(0)}, nil
	case starlark.Tuple:
		return coerceTuple(x)
	case *starlark.Dict:
		return coerceDict(x)
	}
	return nil, fmt.Errorf("unsupported return type %s; return a string, bytes, Response, tuple, or dict", v.Type())
}

func coerceTuple(t starlark.Tuple) (*Response, error) {
	if len(t) < 1 || len(t) > 3 {
		return nil, fmt.Errorf("response tuple must have 1-3 elements, got %d", len(t))
	}
	resp := &Response{Status: 200, Headers: starlark.NewDict(0)}
	switch b := t[0].(type) {
	case starlark.String, starlark.Bytes, Markup:
		resp.Body = b
	default:
		return nil, fmt.Errorf("first tuple element must be string, bytes, or markup, got %s", t[0].Type())
	}
	if len(t) >= 2 {
		s, err := starlark.AsInt32(t[1])
		if err != nil {
			return nil, fmt.Errorf("status must be int: %w", err)
		}
		resp.Status = s
	}
	if len(t) == 3 {
		d, ok := t[2].(*starlark.Dict)
		if !ok {
			return nil, fmt.Errorf("third tuple element must be a dict of headers, got %s", t[2].Type())
		}
		resp.Headers = d
	}
	return resp, nil
}

func coerceDict(d *starlark.Dict) (*Response, error) {
	resp := &Response{Status: 200, Headers: starlark.NewDict(0), Body: starlark.String("")}
	for _, k := range d.Keys() {
		ks, ok := k.(starlark.String)
		if !ok {
			continue
		}
		v, _, _ := d.Get(k)
		switch string(ks) {
		case "body":
			switch b := v.(type) {
			case starlark.String, starlark.Bytes, Markup:
				resp.Body = b
			default:
				return nil, fmt.Errorf("body must be string, bytes, or markup")
			}
		case "status":
			s, err := starlark.AsInt32(v)
			if err != nil {
				return nil, err
			}
			resp.Status = s
		case "headers":
			hd, ok := v.(*starlark.Dict)
			if !ok {
				return nil, fmt.Errorf("headers must be a dict")
			}
			resp.Headers = hd
		case "content_type":
			s, ok := v.(starlark.String)
			if !ok {
				return nil, fmt.Errorf("content_type must be a string")
			}
			resp.ContentType = string(s)
		}
	}
	return resp, nil
}
