package starlark

import (
	"fmt"
	"net/url"
	"sort"

	"go.starlark.net/starlark"
)

// quoteBuiltin matches Python's urllib.parse.quote(s, safe="/"): leaves
// the unreserved set [A-Za-z0-9_.-~] plus the characters in `safe`
// untouched and percent-encodes everything else.
func quoteBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var s, safe = "", "/"
	if err := starlark.UnpackPositionalArgs("quote", args, kwargs, 1, &s, &safe); err != nil {
		return nil, err
	}
	return starlark.String(quoteString(s, safe)), nil
}

// unquoteBuiltin matches Python's urllib.parse.unquote: percent-decodes
// %XX escapes but does NOT treat '+' as space.
func unquoteBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var s string
	if err := starlark.UnpackPositionalArgs("unquote", args, kwargs, 1, &s); err != nil {
		return nil, err
	}
	out, err := url.PathUnescape(s)
	if err != nil {
		return nil, fmt.Errorf("unquote: %w", err)
	}
	return starlark.String(out), nil
}

const hex = "0123456789ABCDEF"

func quoteString(s, safe string) string {
	safeSet := [256]bool{}
	for i := 'A'; i <= 'Z'; i++ {
		safeSet[i] = true
	}
	for i := 'a'; i <= 'z'; i++ {
		safeSet[i] = true
	}
	for i := '0'; i <= '9'; i++ {
		safeSet[i] = true
	}
	for _, c := range "_.-~" {
		safeSet[c] = true
	}
	for i := 0; i < len(safe); i++ {
		safeSet[safe[i]] = true
	}
	// Fast path: all bytes are safe.
	allSafe := true
	for i := 0; i < len(s); i++ {
		if !safeSet[s[i]] {
			allSafe = false
			break
		}
	}
	if allSafe {
		return s
	}
	out := make([]byte, 0, len(s)+8)
	for i := 0; i < len(s); i++ {
		b := s[i]
		if safeSet[b] {
			out = append(out, b)
		} else {
			out = append(out, '%', hex[b>>4], hex[b&0x0f])
		}
	}
	return string(out)
}

// urlencodeBuiltin builds a query-string from a dict or MultiDict, using
// form-encoding semantics (spaces become "+"). Values are coerced to
// strings via str(). Multi-valued keys preserve their order.
func urlencodeBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var v starlark.Value
	if err := starlark.UnpackPositionalArgs("urlencode", args, kwargs, 1, &v); err != nil {
		return nil, err
	}
	values := url.Values{}
	switch x := v.(type) {
	case *starlark.Dict:
		// Sort keys for deterministic output (matches url.Values.Encode()).
		type kv struct {
			k starlark.String
			v starlark.Value
		}
		entries := make([]kv, 0, x.Len())
		for _, k := range x.Keys() {
			ks, ok := k.(starlark.String)
			if !ok {
				return nil, fmt.Errorf("urlencode: dict keys must be strings, got %s", k.Type())
			}
			val, _, _ := x.Get(k)
			entries = append(entries, kv{ks, val})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].k < entries[j].k })
		for _, e := range entries {
			if err := addURLValue(values, string(e.k), e.v); err != nil {
				return nil, err
			}
		}
	case *multiDict:
		// Preserve insertion order; Encode() will sort by key, matching
		// url.Values semantics for consistency.
		for _, e := range x.items {
			values.Add(e.key, e.val)
		}
	default:
		return nil, fmt.Errorf("urlencode: expected dict or MultiDict, got %s", v.Type())
	}
	return starlark.String(values.Encode()), nil
}

// addURLValue appends a single value or every element of a list/tuple
// to the url.Values entry for key. Other types are stringified.
func addURLValue(values url.Values, key string, v starlark.Value) error {
	switch x := v.(type) {
	case *starlark.List:
		it := x.Iterate()
		defer it.Done()
		var item starlark.Value
		for it.Next(&item) {
			values.Add(key, valueToString(item))
		}
	case starlark.Tuple:
		for _, item := range x {
			values.Add(key, valueToString(item))
		}
	default:
		values.Add(key, valueToString(v))
	}
	return nil
}

func valueToString(v starlark.Value) string {
	if s, ok := v.(starlark.String); ok {
		return string(s)
	}
	return v.String()
}
