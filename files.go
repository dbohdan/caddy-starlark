package starlark

import (
	"fmt"
	"io"
	"mime/multipart"

	"go.starlark.net/starlark"
)

// FileStorage is the Starlark value exposed for each part of a
// multipart upload, modeled on Werkzeug/Flask's FileStorage.
//
//	file.filename     - client-supplied filename
//	file.content_type - Content-Type from the part header
//	file.name         - form field name
//	file.size         - declared size in bytes
//	file.read()       - read the entire content as bytes
type FileStorage struct {
	field  string
	header *multipart.FileHeader
}

func newFileStorage(field string, h *multipart.FileHeader) *FileStorage {
	return &FileStorage{field: field, header: h}
}

func (f *FileStorage) String() string {
	return fmt.Sprintf("<FileStorage %q field=%q size=%d>",
		f.header.Filename, f.field, f.header.Size)
}
func (f *FileStorage) Type() string          { return "FileStorage" }
func (f *FileStorage) Freeze()               {}
func (f *FileStorage) Truth() starlark.Bool  { return starlark.True }
func (f *FileStorage) Hash() (uint32, error) { return 0, fmt.Errorf("FileStorage is unhashable") }

func (f *FileStorage) Attr(name string) (starlark.Value, error) {
	switch name {
	case "filename":
		return starlark.String(f.header.Filename), nil
	case "content_type":
		return starlark.String(f.header.Header.Get("Content-Type")), nil
	case "name":
		return starlark.String(f.field), nil
	case "size":
		return starlark.MakeInt64(f.header.Size), nil
	case "read":
		return starlark.NewBuiltin("read", f.readBuiltin), nil
	}
	return nil, nil
}

func (f *FileStorage) AttrNames() []string {
	return []string{"content_type", "filename", "name", "read", "size"}
}

func (f *FileStorage) readBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 || len(kwargs) > 0 {
		return nil, fmt.Errorf("read: takes no arguments")
	}
	rc, err := f.header.Open()
	if err != nil {
		return nil, fmt.Errorf("opening %q: %w", f.header.Filename, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", f.header.Filename, err)
	}
	return starlark.Bytes(data), nil
}

var _ starlark.HasAttrs = (*FileStorage)(nil)

// filesDict is a MultiDict-like view of uploaded files, mapping form
// field name → one or more FileStorage values. Mirrors multiDict's
// surface (get / getlist / keys / values / items / len / iteration / `in`).
type filesDict struct {
	items []filesEntry
}

type filesEntry struct {
	key  string
	file *FileStorage
}

func newFilesDict() *filesDict { return &filesDict{} }
func (d *filesDict) add(k string, f *FileStorage) {
	d.items = append(d.items, filesEntry{k, f})
}

func (d *filesDict) String() string        { return fmt.Sprintf("<files dict len=%d>", len(d.items)) }
func (d *filesDict) Type() string          { return "files" }
func (d *filesDict) Freeze()               {}
func (d *filesDict) Truth() starlark.Bool  { return starlark.Bool(len(d.items) > 0) }
func (d *filesDict) Hash() (uint32, error) { return 0, fmt.Errorf("files is unhashable") }

func (d *filesDict) Get(key starlark.Value) (starlark.Value, bool, error) {
	k, ok := key.(starlark.String)
	if !ok {
		return nil, false, nil
	}
	want := string(k)
	for _, e := range d.items {
		if e.key == want {
			return e.file, true, nil
		}
	}
	return nil, false, nil
}

func (d *filesDict) Len() int { return len(d.items) }

func (d *filesDict) Iterate() starlark.Iterator {
	seen := make(map[string]bool, len(d.items))
	keys := make([]string, 0, len(d.items))
	for _, e := range d.items {
		if !seen[e.key] {
			seen[e.key] = true
			keys = append(keys, e.key)
		}
	}
	return &filesIter{keys: keys}
}

type filesIter struct {
	keys []string
	i    int
}

func (it *filesIter) Next(p *starlark.Value) bool {
	if it.i >= len(it.keys) {
		return false
	}
	*p = starlark.String(it.keys[it.i])
	it.i++
	return true
}
func (it *filesIter) Done() {}

func (d *filesDict) Attr(name string) (starlark.Value, error) {
	switch name {
	case "get":
		return starlark.NewBuiltin("get", d.getBuiltin), nil
	case "getlist", "getall":
		return starlark.NewBuiltin(name, d.getlistBuiltin), nil
	case "keys":
		return starlark.NewBuiltin("keys", d.keysBuiltin), nil
	case "items":
		return starlark.NewBuiltin("items", d.itemsBuiltin), nil
	}
	return nil, nil
}

func (d *filesDict) AttrNames() []string {
	return []string{"get", "getall", "getlist", "items", "keys"}
}

func (d *filesDict) getBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var key string
	var dflt starlark.Value = starlark.None
	if err := starlark.UnpackPositionalArgs("get", args, kwargs, 1, &key, &dflt); err != nil {
		return nil, err
	}
	for _, e := range d.items {
		if e.key == key {
			return e.file, nil
		}
	}
	return dflt, nil
}

func (d *filesDict) getlistBuiltin(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var key string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &key); err != nil {
		return nil, err
	}
	var out []starlark.Value
	for _, e := range d.items {
		if e.key == key {
			out = append(out, e.file)
		}
	}
	return starlark.NewList(out), nil
}

func (d *filesDict) keysBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 || len(kwargs) > 0 {
		return nil, fmt.Errorf("keys: takes no arguments")
	}
	seen := make(map[string]bool)
	var keys []starlark.Value
	for _, e := range d.items {
		if !seen[e.key] {
			seen[e.key] = true
			keys = append(keys, starlark.String(e.key))
		}
	}
	return starlark.NewList(keys), nil
}

func (d *filesDict) itemsBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 || len(kwargs) > 0 {
		return nil, fmt.Errorf("items: takes no arguments")
	}
	out := make([]starlark.Value, 0, len(d.items))
	for _, e := range d.items {
		out = append(out, starlark.Tuple{starlark.String(e.key), e.file})
	}
	return starlark.NewList(out), nil
}

var (
	_ starlark.HasAttrs = (*filesDict)(nil)
	_ starlark.Mapping  = (*filesDict)(nil)
	_ starlark.Sequence = (*filesDict)(nil)
)
