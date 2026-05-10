# Visit http://localhost:8080/ to see this page.
# `request` is a Flask-style request object.
#
# html(template, **kwargs) escapes each kwarg, so user input from the
# query string can't break out of the surrounding HTML.

def respond(request):
    return html(
        """<!doctype html>
<html><body>
<h1>Hello, {name}!</h1>
<p>You requested <code>{path}</code> over {scheme} from {addr}.</p>
<p>Try <a href="?name=Caddy">/?name=Caddy</a>, <a href="/api/echo.star">/api/echo</a>, or <a href="/api/info.star">/api/info</a>.</p>
</body></html>
""",
        name=request.args.get("name", "stranger"),
        path=request.path,
        scheme=request.scheme,
        addr=request.remote_addr,
    )
