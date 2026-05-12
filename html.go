package starlark

import (
	"fmt"
	"strings"

	"go.starlark.net/starlark"
)

// Markup is a string-like value that the html() formatter and writeResponse
// trust as already-safe HTML. It exists so that escape() output and
// markup() output can flow through templates without being double-escaped.
//
// It deliberately does NOT overload + or string formatting: escaping
// happens explicitly via escape() / html(), keeping the rules predictable.
type Markup string

func (m Markup) String() string        { return string(m) }
func (m Markup) Type() string          { return "markup" }
func (m Markup) Freeze()               {}
func (m Markup) Truth() starlark.Bool  { return starlark.Bool(len(m) > 0) }
func (m Markup) Hash() (uint32, error) { return starlark.String(m).Hash() }
func (m Markup) Len() int              { return len(m) }

func (m Markup) Attr(name string) (starlark.Value, error) {
	switch name {
	case "s", "string":
		return starlark.String(m), nil
	}
	return nil, nil
}
func (m Markup) AttrNames() []string { return []string{"s", "string"} }

var _ starlark.HasAttrs = Markup("")

// escapeHTML escapes the five characters that can break out of HTML
// element bodies and double-quoted attributes.
func escapeHTML(s string) string {
	if !strings.ContainsAny(s, `&<>"'`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&#39;")
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// stringOf coerces a Starlark value to its string form for escaping.
// It mirrors Python's str(): String → as-is, Markup → as-is (callers
// decide whether to escape), everything else → val.String().
func stringOf(v starlark.Value) string {
	switch x := v.(type) {
	case starlark.String:
		return string(x)
	case Markup:
		return string(x)
	}
	return v.String()
}

func escapeBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var v starlark.Value
	if err := starlark.UnpackPositionalArgs("escape", args, kwargs, 1, &v); err != nil {
		return nil, err
	}
	if m, ok := v.(Markup); ok {
		return m, nil
	}
	return Markup(escapeHTML(stringOf(v))), nil
}

func markupBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var v starlark.Value
	if err := starlark.UnpackPositionalArgs("markup", args, kwargs, 1, &v); err != nil {
		return nil, err
	}
	if m, ok := v.(Markup); ok {
		return m, nil
	}
	return Markup(stringOf(v)), nil
}

// htmlBuiltin implements html(template, **kwargs): each kwarg is escaped
// unless it's a Markup value, then the template's string.format method
// is invoked with the prepared kwargs. Returns Markup so nested calls
// (e.g. html("<div>{x}</div>", x=html("<p>{y}</p>", y=u))) compose without
// double-escaping.
func htmlBuiltin(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("html: takes 1 positional argument (the template), got %d", len(args))
	}
	var template starlark.String
	switch t := args[0].(type) {
	case starlark.String:
		template = t
	case Markup:
		template = starlark.String(t)
	default:
		return nil, fmt.Errorf("html: template must be string or markup, got %s", args[0].Type())
	}

	prepared := make([]starlark.Tuple, len(kwargs))
	for i, kv := range kwargs {
		k, v := kv[0], kv[1]
		if _, ok := v.(Markup); ok {
			prepared[i] = starlark.Tuple{k, v}
			continue
		}
		prepared[i] = starlark.Tuple{k, starlark.String(escapeHTML(stringOf(v)))}
	}

	formatFn, err := template.Attr("format")
	if err != nil || formatFn == nil {
		return nil, fmt.Errorf("html: cannot resolve string.format")
	}
	out, err := starlark.Call(thread, formatFn, nil, prepared)
	if err != nil {
		return nil, err
	}
	s, ok := out.(starlark.String)
	if !ok {
		return nil, fmt.Errorf("html: format returned %s, not string", out.Type())
	}
	return Markup(s), nil
}
