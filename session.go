package starlark

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	starlarkjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

// Session cookie format: "<b64-payload>.<b64-mac>", URL-safe base64
// without padding (RawURLEncoding). The MAC is HMAC-SHA256(secret,
// b64-payload) — we sign the encoded form so verification doesn't
// depend on JSON canonicalization.

// encodeSession turns the dict's JSON form into a signed cookie value.
func encodeSession(thread *starlark.Thread, d *starlark.Dict, secret []byte) (string, error) {
	js, err := dictToJSON(thread, d)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(js))
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig, nil
}

// decodeSession verifies the signature and returns the decoded dict.
// A returned (nil, nil) means "no cookie or signature mismatch" —
// handler treats this as starting a fresh empty session.
func decodeSession(thread *starlark.Thread, value string, secret []byte) (*starlark.Dict, error) {
	dot := strings.LastIndexByte(value, '.')
	if dot < 0 {
		return nil, nil
	}
	payload, sig := value[:dot], value[dot+1:]
	gotSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil, nil
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	if !hmac.Equal(mac.Sum(nil), gotSig) {
		return nil, nil
	}
	js, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, nil
	}
	return jsonToDict(thread, string(js))
}

func dictToJSON(thread *starlark.Thread, d *starlark.Dict) (string, error) {
	encode, err := starlarkjson.Module.Attr("encode")
	if err != nil || encode == nil {
		return "", errors.New("session: cannot resolve json.encode")
	}
	out, err := starlark.Call(thread, encode, starlark.Tuple{d}, nil)
	if err != nil {
		return "", fmt.Errorf("session encode: %w", err)
	}
	s, ok := out.(starlark.String)
	if !ok {
		return "", fmt.Errorf("session encode: json returned %s, not string", out.Type())
	}
	return string(s), nil
}

func jsonToDict(thread *starlark.Thread, js string) (*starlark.Dict, error) {
	decode, err := starlarkjson.Module.Attr("decode")
	if err != nil || decode == nil {
		return nil, errors.New("session: cannot resolve json.decode")
	}
	out, err := starlark.Call(thread, decode, starlark.Tuple{starlark.String(js)}, nil)
	if err != nil {
		return nil, nil // tampered or wrong format: treat as fresh
	}
	d, ok := out.(*starlark.Dict)
	if !ok {
		return nil, nil
	}
	return d, nil
}

// writeSessionCookie sets the session cookie based on before/after
// snapshots. If the session went from non-empty to empty, a
// Max-Age=0 delete cookie is emitted. If unchanged, nothing happens.
func writeSessionCookie(w http.ResponseWriter, r *http.Request, thread *starlark.Thread, name string, secret []byte, original, current *starlark.Dict) error {
	if original == nil && current == nil {
		return nil
	}

	origJSON := "{}"
	if original != nil {
		s, err := dictToJSON(thread, original)
		if err != nil {
			return err
		}
		origJSON = s
	}
	curJSON := "{}"
	if current != nil {
		s, err := dictToJSON(thread, current)
		if err != nil {
			return err
		}
		curJSON = s
	}
	if origJSON == curJSON {
		return nil
	}

	c := &http.Cookie{
		Name:     name,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	}
	if curJSON == "{}" {
		c.Value = ""
		c.MaxAge = -1 // delete
		http.SetCookie(w, c)
		return nil
	}
	encoded, err := encodeSession(thread, current, secret)
	if err != nil {
		return err
	}
	c.Value = encoded
	http.SetCookie(w, c)
	return nil
}
