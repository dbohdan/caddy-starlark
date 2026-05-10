package starlark

import (
	"fmt"
	"net/textproto"
	"sort"
	"strings"

	"go.starlark.net/starlark"
)

// multiDict is a Werkzeug/Flask-style ImmutableMultiDict: indexing returns
// the first value for a key, but getlist() returns every occurrence. It
// supports `len`, `in`, iteration over deduplicated keys, and the usual
// dict-style methods.
type multiDict struct {
	items           []multiDictEntry
	caseInsensitive bool
}

type multiDictEntry struct{ key, val string }

func newMultiDict() *multiDict { return &multiDict{} }

func (m *multiDict) add(k, v string) { m.items = append(m.items, multiDictEntry{k, v}) }

func (m *multiDict) normalize(k string) string {
	if m.caseInsensitive {
		return textproto.CanonicalMIMEHeaderKey(k)
	}
	return k
}

func (m *multiDict) String() string {
	var b strings.Builder
	b.WriteString("MultiDict([")
	for i, e := range m.items {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "(%q, %q)", e.key, e.val)
	}
	b.WriteString("])")
	return b.String()
}
func (m *multiDict) Type() string          { return "MultiDict" }
func (m *multiDict) Freeze()               {}
func (m *multiDict) Truth() starlark.Bool  { return starlark.Bool(len(m.items) > 0) }
func (m *multiDict) Hash() (uint32, error) { return 0, fmt.Errorf("MultiDict is unhashable") }

// Get implements starlark.Mapping for `md[key]` and `key in md`.
func (m *multiDict) Get(key starlark.Value) (starlark.Value, bool, error) {
	k, ok := key.(starlark.String)
	if !ok {
		return nil, false, nil
	}
	want := m.normalize(string(k))
	for _, e := range m.items {
		if m.normalize(e.key) == want {
			return starlark.String(e.val), true, nil
		}
	}
	return nil, false, nil
}

func (m *multiDict) Len() int { return len(m.items) }

// Iterate yields keys in insertion order, deduplicated. This makes the
// MultiDict iterable like a Python dict.
func (m *multiDict) Iterate() starlark.Iterator {
	seen := make(map[string]bool, len(m.items))
	keys := make([]string, 0, len(m.items))
	for _, e := range m.items {
		k := m.normalize(e.key)
		if !seen[k] {
			seen[k] = true
			keys = append(keys, e.key)
		}
	}
	return &mdIter{keys: keys}
}

type mdIter struct {
	keys []string
	i    int
}

func (it *mdIter) Next(p *starlark.Value) bool {
	if it.i >= len(it.keys) {
		return false
	}
	*p = starlark.String(it.keys[it.i])
	it.i++
	return true
}
func (it *mdIter) Done() {}

func (m *multiDict) Attr(name string) (starlark.Value, error) {
	switch name {
	case "get":
		return starlark.NewBuiltin("get", m.getBuiltin), nil
	case "getlist", "getall":
		return starlark.NewBuiltin(name, m.getlistBuiltin), nil
	case "keys":
		return starlark.NewBuiltin("keys", m.keysBuiltin), nil
	case "values":
		return starlark.NewBuiltin("values", m.valuesBuiltin), nil
	case "items":
		return starlark.NewBuiltin("items", m.itemsBuiltin), nil
	case "to_dict":
		return starlark.NewBuiltin("to_dict", m.toDictBuiltin), nil
	}
	return nil, nil
}

func (m *multiDict) AttrNames() []string {
	return []string{"get", "getlist", "getall", "keys", "values", "items", "to_dict"}
}

func (m *multiDict) getBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var key string
	var dflt starlark.Value = starlark.None
	if err := starlark.UnpackPositionalArgs("get", args, kwargs, 1, &key, &dflt); err != nil {
		return nil, err
	}
	want := m.normalize(key)
	for _, e := range m.items {
		if m.normalize(e.key) == want {
			return starlark.String(e.val), nil
		}
	}
	return dflt, nil
}

func (m *multiDict) getlistBuiltin(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var key string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &key); err != nil {
		return nil, err
	}
	want := m.normalize(key)
	var out []starlark.Value
	for _, e := range m.items {
		if m.normalize(e.key) == want {
			out = append(out, starlark.String(e.val))
		}
	}
	return starlark.NewList(out), nil
}

func (m *multiDict) keysBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 || len(kwargs) > 0 {
		return nil, fmt.Errorf("keys: takes no arguments")
	}
	seen := make(map[string]bool)
	var keys []starlark.Value
	for _, e := range m.items {
		k := m.normalize(e.key)
		if !seen[k] {
			seen[k] = true
			keys = append(keys, starlark.String(e.key))
		}
	}
	sort.SliceStable(keys, func(i, j int) bool {
		return string(keys[i].(starlark.String)) < string(keys[j].(starlark.String))
	})
	return starlark.NewList(keys), nil
}

func (m *multiDict) valuesBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 || len(kwargs) > 0 {
		return nil, fmt.Errorf("values: takes no arguments")
	}
	out := make([]starlark.Value, 0, len(m.items))
	for _, e := range m.items {
		out = append(out, starlark.String(e.val))
	}
	return starlark.NewList(out), nil
}

func (m *multiDict) itemsBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 || len(kwargs) > 0 {
		return nil, fmt.Errorf("items: takes no arguments")
	}
	out := make([]starlark.Value, 0, len(m.items))
	for _, e := range m.items {
		out = append(out, starlark.Tuple{starlark.String(e.key), starlark.String(e.val)})
	}
	return starlark.NewList(out), nil
}

func (m *multiDict) toDictBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 || len(kwargs) > 0 {
		return nil, fmt.Errorf("to_dict: takes no arguments")
	}
	d := starlark.NewDict(0)
	for _, e := range m.items {
		k := starlark.String(e.key)
		if _, found, _ := d.Get(k); !found {
			_ = d.SetKey(k, starlark.String(e.val))
		}
	}
	return d, nil
}

var (
	_ starlark.HasAttrs = (*multiDict)(nil)
	_ starlark.Mapping  = (*multiDict)(nil)
	_ starlark.Sequence = (*multiDict)(nil)
)
