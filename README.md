# caddy-starlark

A [Caddy](https://caddyserver.com/) plugin that serves HTTP responses from views written in Starlark.
[Starlark](https://github.com/google/starlark-go) is a sandboxed configuration and scripting language.
The plugin lets you work with Python-like Starlark code instead of Caddy's built-in Go templates.

The Starlark API is modeled on [Flask](https://flask.palletsprojects.com/).
Each request calls a top-level `respond(request)` function in a matching Starlark file.
The `request` object exposes Flask-style attributes (`method`, `path`, `args`, `headers`, `cookies`, `form`, `json()`, `remote_addr`, ...).
The return value of the entry-point function becomes the HTTP response.

caddy-starlark was originally designed and implemented by Claude Opus 4.7 via Claude Code on the web with only high-level human direction.

## Install

Build a Caddy binary that includes this module with [xcaddy](https://github.com/caddyserver/xcaddy):

```sh
xcaddy build --with github.com/dbohdan/caddy-starlark
```

Or, for local development, use the bundled `cmd/caddy`:

```sh
go run ./cmd/caddy run --config examples/Caddyfile --adapter caddyfile
```

## Caddyfile

```Caddyfile
{
    order starlark before file_server
}

:8080 {
    starlark {
        root            ./views       # directory of ".star" files
        extension       .star         # extension to recognize (default ".star")
        entry_point     respond       # function to call (default "respond")
        index           index.star    # view for "/" requests
        cache_programs  true          # cache parsed programs by mtime
        max_body_size   4MB           # request body cap (default 4MiB; "unlimited" disables)
    }
}
```

A request for `/foo` resolves to `<root>/foo.star`;
`/foo/` resolves to `<root>/foo/<index>`.
Requests that don't map to an existing `.star` file fall through to the next handler.
You can layer `starlark` above `file_server` for static assets.

## A first view

```python
# views/index.star
def respond(request):
    name = request.args.get("name", "World")
    return "Hello, " + name + "!"
```

```sh
$ curl 'http://localhost:8080/?name=Caddy'
Hello, Caddy!
```

## The `request` object

Inspired by [`flask.request`](https://flask.palletsprojects.com/en/stable/api/#flask.Request):

| attribute                                | type          | description                                       |
| ---                                      | ---           | ---                                               |
| `request.method`                         | `str`         | HTTP method                                       |
| `request.path`                           | `str`         | URL path                                          |
| `request.full_path`                      | `str`         | path + `?` + raw query                            |
| `request.query_string`                   | `str`         | raw query string                                  |
| `request.url`                            | `str`         | full URL                                          |
| `request.host`                           | `str`         | `Host` header value                               |
| `request.scheme`                         | `str`         | `"http"` or `"https"`                             |
| `request.remote_addr`                    | `str`         | client IP                                         |
| `request.content_type`                   | `str`         | request `Content-Type` header                     |
| `request.content_length`                 | `int`         | declared body length                              |
| `request.args`                           | `MultiDict`   | query parameters                                  |
| `request.headers`                        | `MultiDict`   | request headers (case-insensitive)                |
| `request.cookies`                        | `dict`        | cookies                                           |
| `request.session`                        | `dict`        | signed-cookie session; mutate to persist (requires `secret_key`) |
| `request.form`                           | `MultiDict`   | parsed body (`application/x-www-form-urlencoded` or the non-file parts of `multipart/form-data`) |
| `request.files`                          | files `dict`  | uploaded files (only for `multipart/form-data`)   |
| `request.values`                         | `MultiDict`   | `args` + `form` combined                          |
| `request.data`                           | `bytes`       | raw request body                                  |
| `request.json()`                         | function      | parse the body as JSON                            |
| `request.get_header(name, default=None)` | function      | case-insensitive single-header lookup |

`MultiDict` mirrors Werkzeug's: indexing returns the first value, while `getlist(key)` returns all values.
It also has `get`, `keys`, `values`, `items`, and `to_dict`.

## Redirects

`redirect(location, status=302)` builds a `Location:` response.
**The URL is sent verbatim**; passing user input directly is an open-redirect vulnerability:

```python
# DANGEROUS!
# Attacker controls the destination via `?next=//evil.com`.
return redirect(req.args.get("next", "/"))
```

If you need a "next" parameter, validate it first (e.g., require a leading `/` and forbid `//` to keep it relative to your site):

```python
nxt = req.args.get("next", "/")

if not nxt.startswith("/") or nxt.startswith("//"):
    nxt = "/"

return redirect(nxt)
```

## Response shapes

The entry point may return any of the following, also Flask-style:

```python
def respond(req):
    return "plain text"                                  # 200
    return b"\x00\x01"                                   # 200 bytes
    return ("not found", 404)                            # tuple
    return ("ok", 200, {"X-A": "1"})                     # tuple + headers
    return Response("hello", status=201,                 # full Response
                    headers={"X-K": "v"},
                    content_type="text/plain")
    return None                                          # 204
```

## Cookies

`request.cookies` reads incoming cookies as a `dict[str, str]`.
To _set_ cookies on the response, call `set_cookie` on a `Response` object:

```python
def respond(req):
    r = Response("logged in")

    r.set_cookie(
        "sid", "abc123",
        max_age=3600,         # seconds; or expires=time.from_timestamp(n)
        path="/",
        secure=True,
        httponly=True,
        samesite="strict",    # "lax" | "strict" | "none"
    )

    return r
```

Call `set_cookie` more than once to send multiple cookies.

## Code reuse

You can import symbols from other Starlark files in the same root directory by calling `load("foo.star", ...)`.

```python
# message.star
REPEAT = 5


def message():
    return "Hello, world!"

# greet.star
load("message.star", "message", TIMES="REPEAT")


def respond(request):
    return Response(
        "\n".join([message()] * TIMES),
        content_type="text/plain",
    )
```

## Globals available to views

| name                           | description                                                                                          |
| ---                            | ---                                                                                                  |
| `Response(...)`                | response constructor                                                                                 |
| `redirect(url, status=302)`    | shorthand for a redirect response                                                                    |
| `abort(status, message="")`    | terminate with a Caddy HTTP error                                                                    |
| `escape(value)`                | HTML-escape `& < > " '`, returning `markup` (already-`markup` values pass through)                   |
| `markup(value)`                | wrap a value as already-safe HTML (returns `markup`)                                                 |
| `html(template, **kwargs)`     | format a template, escaping each kwarg unless it's `markup`; returns `markup`                        |
| `quote(s, safe="/")`           | percent-encode unreserved chars (`A-Za-z0-9_.-~` plus `safe`); matches Python's `urllib.parse.quote` |
| `unquote(s)`                   | decode `%XX` escapes; does **not** treat `+` as space (matches Python's `unquote`)                   |
| `urlencode(mapping)`           | build a query-string from a `dict` or `MultiDict`; uses form encoding (space → `+`); list/tuple values produce repeated keys |
| `placeholder(key, default="")` | resolve a [Caddy placeholder](https://caddyserver.com/docs/conventions#placeholders); accepts `"{...}"` or bare key; alias `ph` |
| `json`                         | starlark-go's [`json`](https://github.com/google/starlark-go/blob/master/starlarkjson/json.go) module (`json.encode`, `json.decode`, `json.indent`) |
| `time`                         | starlark-go's `time` module                                                                          |
| `math`                         | starlark-go's `math` module                                                                          |
| `struct`                       | starlark-go's `struct` constructor                                                                   |

## HTML escaping

Returning a string defaults `Content-Type` to `text/html; charset=utf-8`, so any user input you interpolate into a response needs to be escaped to avoid XSS.
The recommended way is the `html(...)` formatter:

```python
def respond(req):
    return html(
        "<p>Hello, {name}! You said: {msg}</p>",
        name=req.args.get("name", ""),
        msg=req.args.get("msg", ""),
    )
```

`html()` escapes each kwarg, so the request `/?name=<script>alert(1)</script>` produces `<p>Hello, &lt;script&gt;alert(1)&lt;/script&gt;! ...</p>`.

If you need to pass already-trusted HTML through, wrap it with `markup()`:

```python
def respond(req):
    nav = markup("<nav>…</nav>")
    return html("<header>{nav}</header><main>{body}</main>",
                nav=nav, body=req.args.get("body", ""))
```

`escape(value)` does the same escaping outside of templates and is idempotent on `markup` values:

```python
def respond(req):
    return "Hello, " + escape(req.args.get("name", "")) + "!"
```

`html()` returns a `markup` value, so calls compose without double-escaping: `html("<div>{x}</div>", x=html("<p>{y}</p>", y=user))` escapes `user` exactly once.

Raw string concatenation of untrusted input is dangerous.
Reach for `html(...)` first.

`escape()` covers HTML element bodies and quoted attribute values (both single- and double-quoted).
It is **not** sufficient for other contexts:

- Inside `<script>` tags, escape with JSON encoding (`json.encode(x)`) rather than `escape(x)`.
- Inside `<style>` blocks or `style="..."` attributes, neither helper is safe.
  Don't interpolate untrusted input there.
- Inside `href="..."` or `src="..."` URLs, use `quote()` for the path component and `urlencode()` for query strings, then `escape()` the result for the attribute.

## URL encoding

Three helpers mirror `urllib.parse`:

```python
quote("a b/c?d=&%")           # → "a%20b/c%3Fd%3D%26%25" (keeps "/")
quote("a/b", safe="")         # → "a%2Fb"
unquote("a%20b%2Fc+")         # → "a b/c+" (does not treat "+" as space)
urlencode({"q": "hello world", "lang": "en"})
                              # → "lang=en&q=hello+world" (form encoding)
urlencode(req.args)           # works on MultiDict; preserves multi-values
urlencode({"x": [1, 2, 3]})   # → "x=1&x=2&x=3"
```

## Caddy placeholders

```python
def respond(req):
    return "request id: " + placeholder("http.request.uuid")
```

Both `"{http.request.uuid}"` and `"http.request.uuid"` work, matching the convenience of Caddy's Go-template `placeholder` function.

## Sessions (signed cookies)

`request.session` is a Flask-style signed-cookie session: an ordinary `dict` decoded from a single cookie that the handler signs with HMAC-SHA256.
This avoids the need to manage server-side storage.

Enable sessions with a `secret_key` in the Caddyfile (32+ bytes recommended):

```Caddyfile
starlark {
    root        ./views
    secret_key  {env.STARLARK_SECRET_KEY}
}
```

In a view:

```python
def respond(req):
    n = req.session.get("count", 0) + 1
    req.session["count"] = n
    return "Visit #" + str(n)
```

The handler snapshots the session at first access.
After the view returns, if the dict has changed, the session cookie is re-issued (`Set-Cookie: session=…; Path=/; HttpOnly; SameSite=Lax`, plus `Secure` automatically on HTTPS).
Calling `req.session.clear()` on a non-empty session sends a delete cookie.
Tampered or unsigned cookies are silently treated as a fresh empty session.

Session cookies are **signed, not encrypted**.
The cookie is base64-encoded JSON readable by anyone who has it.
Don't store secrets in it.
For real authentication, consider the [`caddy-security`](https://github.com/greenpau/caddy-security) plugin or run an upstream service via `forward_auth`.

The `Secure` flag is set when `r.TLS != nil`, i.e., when this Caddy instance terminated the TLS connection itself.
If Caddy sits behind another TLS terminator (a CDN, a load balancer, another reverse proxy) and gets plaintext from upstream, the flag will not be set even though the user's connection is HTTPS.
In that deployment, configure Caddy's [`trusted_proxies`](https://caddyserver.com/docs/caddyfile/options#trusted-proxies) and put a [`header`](https://caddyserver.com/docs/caddyfile/directives/header) directive in front of `starlark` to add `Secure` to the `Set-Cookie`, or set `secret_key` only on HTTPS-only sites.

A `secret_key` shorter than 32 bytes is accepted but produces a log warning on startup; use a 32+ byte random value (e.g., `openssl rand -base64 32`).

| Caddyfile option       | default     | notes                                |
| ---                    | ---         | ---                                  |
| `secret_key <str>`     | (none)      | enables sessions; HMAC-SHA256 key    |
| `session_cookie_name`  | `session`   | cookie name                          |

## File uploads

For `multipart/form-data` requests, `request.files` exposes uploaded parts.
The container supports `get(name)`, `getlist(name)` (Werkzeug style: returns every part for a name), `keys()`, `items()`, iteration, `len()`, and `in`.

Each `FileStorage` value has:

| attr               | type   | description                            |
| ---                | ---    | ---                                    |
| `file.filename`    | `str`  | client-supplied filename               |
| `file.content_type`| `str`  | per-part `Content-Type`                |
| `file.name`        | `str`  | form field name                        |
| `file.size`        | `int`  | declared size in bytes                 |
| `file.read()`      | method | returns the entire body as `bytes`     |

Non-file form fields are available as `request.form`.
Example:

```python
def respond(req):
    f = req.files.get("avatar")

    # Starlark has no `is`.
    if f == None:
        abort(400, "missing 'avatar'")

    note = req.form.get("note", "")

    return Response(
        json.encode({
            "filename": f.filename,
            "size":     f.size,
            "note":     note,
        }),
        content_type="application/json",
    )
```

```sh
curl -F "avatar=@photo.png" -F "note=hello" http://localhost:8080/api/upload.star
```

The handler enforces `max_body_size` for multipart bodies too.
Files above the in-memory threshold (32 MiB or `max_body_size`, whichever is smaller) spill to temp files; those are removed automatically when the view returns.

## Limits

The handler caps request bodies at `max_body_size` (default 4 MiB).
Reads beyond the limit (`request.data`, `request.json()`, `request.form`) fail and the response becomes HTTP 413.
Set `max_body_size unlimited` to disable the cap.

If you'd rather configure the cap globally, Caddy's built-in [`request_body`](https://caddyserver.com/docs/caddyfile/directives/request_body) directive works just as well.
Set `max_body_size unlimited` here and let `request_body` enforce it.

## Date and time

Caddy exposes the current time as placeholders, so they're available through `placeholder()`:

| placeholder            | example                                  |
| ---                    | ---                                      |
| `time.now.http`        | `Sun, 10 May 2026 21:40:29 GMT`          |
| `time.now.common_log`  | `10/May/2026:21:40:29 +0000`             |
| `time.now.year`        | `2026`                                   |
| `time.now.unix`        | `1778449229`                             |
| `time.now.unix_ms`     | `1778449229950`                          |
| `time.now`             | `2026-05-10 21:40:29.95 +0000 UTC ...`   |

`time.now.year`, `time.now.unix`, and `time.now.unix_ms` come back as numeric strings.
Wrap them in `int(...)` if you want an integer.
The bare `time.now` placeholder stringifies a Go `time.Time` value, which includes a monotonic-clock reading (`m=+...`).
While this fine for logging, it is less useful for parsing.
Prefer the formatted variants for display.

For arithmetic or formatting, use the `time` module global instead (starlark-go's [`lib/time`](https://github.com/google/starlark-go/blob/master/lib/time/time.go)):

```python
def respond(req):
    t = time.now()

    return Response(
        json.encode({
            "iso":  t.format("2006-01-02T15:04:05Z07:00"),
            "year": t.year,
            "unix": t.unix,
            "vilnius": t.in_location("Europe/Vilnius").format("15:04 -0700"),
        }),
        content_type="application/json",
    )
```

A `time` value supports `.year`, `.month`, `.day`, `.hour`, `.minute`, `.second`, `.nanosecond`, `.unix`, `.unix_nano`, `.format(layout)`, and `.in_location(tz)`.
It also can perform arithmetic with durations like `time.parse_duration("5m")`.

## A complete example

See the [`examples/`](./examples) directory:

- [`examples/Caddyfile`](./examples/Caddyfile) — server config
- [`examples/views/index.star`](./examples/views/index.star) — HTML home page
- [`examples/views/api/echo.star`](./examples/views/api/echo.star) — JSON echo endpoint
- [`examples/views/api/info.star`](./examples/views/api/info.star) — placeholder demo
- [`examples/views/api/png.star`](./examples/views/api/png.star) — generates a PNG image dynamically (binary response)
- [`examples/views/api/now.star`](./examples/views/api/now.star) — current date and time via both placeholders and the `time` module
- [`examples/views/api/upload.star`](./examples/views/api/upload.star) — file upload using `request.files`
- [`examples/views/api/counter.star`](./examples/views/api/counter.star) — per-visitor visit counter using a signed-cookie session

## Tests

```sh
go test ./...
```

## License

Apache 2.0.
See the file [`LICENSE`](LICENSE).
