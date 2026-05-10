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
        root         ./scripts        # directory of .star files
        extension    .star            # extension to recognize (default .star)
        entrypoint   respond          # function to call (default respond)
        index        index.star       # script for `/` requests
        cache_scripts true            # cache parsed programs by mtime
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

## Globals available inside scripts

| name                      | description                                                |
| ---                       | ---                                                        |
| `Response(...)`           | response constructor                                       |
| `redirect(url, status=302)` | shorthand for a redirect response                        |
| `abort(status, message="")` | terminate with a Caddy HTTP error                        |
| `placeholder(key, default="")` | resolve a [Caddy placeholder](https://caddyserver.com/docs/conventions#placeholders); accepts `"{...}"` or bare key; alias `ph` |
| `json`                    | starlark-go's [`json`](https://github.com/google/starlark-go/blob/master/starlarkjson/json.go) module (`json.encode`, `json.decode`, `json.indent`) |
| `time`                    | starlark-go's `time` module                                |
| `math`                    | starlark-go's `math` module                                |
| `struct`                  | starlark-go's `struct` constructor                         |

## Caddy placeholders

```python
def respond(req):
    return "request id: " + placeholder("http.request.uuid")
```

Both `"{http.request.uuid}"` and `"http.request.uuid"` work, matching
the convenience of Caddy's Go-template `placeholder` function.

## A complete example

See the [`examples/`](./examples) directory:

- [`examples/Caddyfile`](./examples/Caddyfile) — server config
- [`examples/scripts/index.star`](./examples/scripts/index.star) — HTML home page
- [`examples/scripts/api/echo.star`](./examples/scripts/api/echo.star) — JSON echo endpoint
- [`examples/scripts/api/info.star`](./examples/scripts/api/info.star) — placeholder demo
- [`examples/scripts/api/png.star`](./examples/scripts/api/png.star) — generates a PNG image dynamically (binary response)

## Tests

```sh
go test ./...
```

## License

Apache-2.0.
