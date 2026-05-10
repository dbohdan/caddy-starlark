# Visit http://localhost:8080/ to see this page.
# `request` is a Flask-style request object.

def respond(request):
    name = request.args.get("name", "stranger")
    return """<!doctype html>
<html><body>
<h1>Hello, {name}!</h1>
<p>You requested <code>{path}</code> over {scheme} from {addr}.</p>
<p>Try <a href="?name=Caddy">/?name=Caddy</a>, <a href="/api/echo.star">/api/echo</a>, or <a href="/api/info.star">/api/info</a>.</p>
</body></html>
""".format(
        name=name,
        path=request.path,
        scheme=request.scheme,
        addr=request.remote_addr,
    )
