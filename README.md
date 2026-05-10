# caddy-starlark

A [Caddy](https://caddyserver.com/) HTTP handler that runs
[Starlark](https://github.com/google/starlark-go) scripts as request
handlers — Starlark templates, in the spirit of Caddy's built-in
`templates` directive, but with code instead of `{{...}}`.

The script API is modeled on **Flask**: each request calls a top-level
`respond(request)` function. The `request` object exposes Flask-style
attributes (`method`, `path`, `args`, `headers`, `cookies`, `form`,
`json()`, `remote_addr`, ...) and the return value becomes the HTTP
response.

## Install

Build a Caddy binary that includes this module with
[xcaddy](https://github.com/caddyserver/xcaddy):

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
        root          ./scripts       # directory of .star files
        extension     .star           # extension to recognize (default .star)
        entrypoint    respond         # function to call (default respond)
        index         index.star      # script for `/` requests
        cache_scripts true            # cache parsed programs by mtime
        max_body_size 4MB             # request body cap (default 4MiB; "unlimited" disables)
    }
}
```

A request for `/foo` resolves to `<root>/foo.star`; `/foo/` resolves to
`<root>/foo/<index>`. Requests that don't map to an existing `.star`
file fall through to the next handler, so you can layer `starlark`
above `file_server` for static assets.

## A first script

```python
# scripts/index.star
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

| attribute              | type        | description                                       |
| ---                    | ---         | ---                                               |
| `request.method`       | `str`       | HTTP method                                       |
| `request.path`         | `str`       | URL path                                          |
| `request.full_path`    | `str`       | path + `?` + raw query                            |
| `request.query_string` | `str`       | raw query string                                  |
| `request.url`          | `str`       | full URL                                          |
| `request.host`         | `str`       | `Host` header value                               |
| `request.scheme`       | `str`       | `"http"` or `"https"`                             |
| `request.remote_addr`  | `str`       | client IP                                         |
| `request.content_type` | `str`       | request `Content-Type` header                    |
| `request.content_length` | `int`     | declared body length                              |
| `request.args`         | `MultiDict` | query parameters                                  |
| `request.headers`      | `MultiDict` | request headers (case-insensitive)                |
| `request.cookies`      | `dict`      | cookies                                           |
| `request.form`         | `MultiDict` | parsed `application/x-www-form-urlencoded` body   |
| `request.values`       | `MultiDict` | `args` + `form` combined                          |
| `request.data`         | `bytes`     | raw request body                                  |
| `request.json()`       | function    | parse the body as JSON                            |
| `request.get_header(name, default=None)` | function | case-insensitive single-header lookup |

`MultiDict` mirrors Werkzeug's: indexing returns the first value, while
`getlist(key)` returns all values. It also has `get`, `keys`, `values`,
`items`, and `to_dict`.

## Response shapes

The entrypoint may return any of the following — also Flask-style:

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

`request.cookies` reads incoming cookies as a `dict[str, str]`. To
*set* cookies on the response, call `set_cookie` on a `Response` object:

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

## Globals available inside scripts

| name                      | description                                                |
| ---                       | ---                                                        |
| `Response(...)`           | response constructor                                       |
| `redirect(url, status=302)` | shorthand for a redirect response                        |
| `abort(status, message="")` | terminate with a Caddy HTTP error                        |
| `escape(value)`           | HTML-escape `& < > " '`, returning `markup` (already-`markup` values pass through) |
| `markup(value)`           | wrap a value as already-safe HTML (returns `markup`)       |
| `html(template, **kwargs)` | format a template, escaping each kwarg unless it's `markup`; returns `markup` |
| `placeholder(key, default="")` | resolve a [Caddy placeholder](https://caddyserver.com/docs/conventions#placeholders); accepts `"{...}"` or bare key; alias `ph` |
| `json`                    | starlark-go's [`json`](https://github.com/google/starlark-go/blob/master/starlarkjson/json.go) module (`json.encode`, `json.decode`, `json.indent`) |
| `time`                    | starlark-go's `time` module                                |
| `math`                    | starlark-go's `math` module                                |
| `struct`                  | starlark-go's `struct` constructor                         |

## HTML escaping

Returning a string defaults `Content-Type` to `text/html; charset=utf-8`,
so any user input you interpolate into a response needs to be escaped to
avoid XSS. The recommended idiom is the `html(...)` formatter:

```python
def respond(req):
    return html(
        "<p>Hello, {name}! You said: {msg}</p>",
        name=req.args.get("name", ""),
        msg=req.args.get("msg", ""),
    )
```

`html()` escapes each kwarg, so the request
`/?name=<script>alert(1)</script>` produces
`<p>Hello, &lt;script&gt;alert(1)&lt;/script&gt;! ...</p>`.

If you need to pass already-trusted HTML through, wrap it with
`markup()`:

```python
def respond(req):
    nav = markup("<nav>…</nav>")
    return html("<header>{nav}</header><main>{body}</main>",
                nav=nav, body=req.args.get("body", ""))
```

`escape(value)` does the same escaping outside of templates and is
idempotent on `markup` values:

```python
def respond(req):
    return "Hello, " + escape(req.args.get("name", "")) + "!"
```

`html()` returns a `markup` value, so calls compose without
double-escaping: `html("<div>{x}</div>", x=html("<p>{y}</p>", y=user))`
escapes `user` exactly once.

These helpers are not autoescape — raw string concatenation of
untrusted input remains a footgun. Reach for `html(...)` first.

## Caddy placeholders

```python
def respond(req):
    return "request id: " + placeholder("http.request.uuid")
```

Both `"{http.request.uuid}"` and `"http.request.uuid"` work, matching
the convenience of Caddy's Go-template `placeholder` function.

## Limits

The handler caps request bodies at `max_body_size` (default 4 MiB).
Reads beyond the limit (`request.data`, `request.json()`, `request.form`)
fail and the response becomes HTTP 413. Set `max_body_size unlimited`
to disable the cap.

If you'd rather configure the cap globally, Caddy's built-in
[`request_body`](https://caddyserver.com/docs/caddyfile/directives/request_body)
directive works just as well — set `max_body_size unlimited` here and
let `request_body` enforce it.

## Date and time

Caddy exposes the current time as placeholders, so they're available
through `placeholder()`:

| placeholder            | example                                  |
| ---                    | ---                                      |
| `time.now.http`        | `Sun, 10 May 2026 21:40:29 GMT`          |
| `time.now.common_log`  | `10/May/2026:21:40:29 +0000`             |
| `time.now.year`        | `2026`                                   |
| `time.now.unix`        | `1778449229`                             |
| `time.now.unix_ms`     | `1778449229950`                          |
| `time.now`             | `2026-05-10 21:40:29.95 +0000 UTC ...`   |

`time.now.year`, `time.now.unix`, and `time.now.unix_ms` come back as
numeric strings — wrap them in `int(...)` if you want an integer.
The bare `time.now` placeholder stringifies a Go `time.Time` value,
which includes a monotonic-clock reading (`m=+...`) — fine for logging,
less useful for parsing. Prefer the formatted variants for display.

For arithmetic or formatting, use the `time` module global instead
(it's starlark-go's [`lib/time`](https://github.com/google/starlark-go/blob/master/lib/time/time.go)):

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

A `time` value supports `.year`, `.month`, `.day`, `.hour`, `.minute`,
`.second`, `.nanosecond`, `.unix`, `.unix_nano`, `.format(layout)`, and
`.in_location(tz)`. It also participates in arithmetic with
`time.parse_duration("5m")` and friends.

## A complete example

See the [`examples/`](./examples) directory:

- [`examples/Caddyfile`](./examples/Caddyfile) — server config
- [`examples/scripts/index.star`](./examples/scripts/index.star) — HTML home page
- [`examples/scripts/api/echo.star`](./examples/scripts/api/echo.star) — JSON echo endpoint
- [`examples/scripts/api/info.star`](./examples/scripts/api/info.star) — placeholder demo
- [`examples/scripts/api/png.star`](./examples/scripts/api/png.star) — generates a PNG image dynamically (binary response)
- [`examples/scripts/api/now.star`](./examples/scripts/api/now.star) — current date and time via both placeholders and the `time` module

## Tests

```sh
go test ./...
```

## License

Apache-2.0.
