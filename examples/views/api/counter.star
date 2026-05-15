# Per-visitor visit counter using the signed-cookie session.
#
# Visit http://localhost:8080/api/counter.star several times in the same
# browser session — each refresh increments the count, with no
# server-side storage. Open a private window or another browser to get
# a fresh counter.
#
# Requires `secret_key` to be set in the Caddyfile.

PAGE = """<p>You've visited this page <b>{n}</b> times.</p>
<p>Try <a href="?reset=1">/api/counter.star?reset=1</a> to clear.</p>"""

def respond(request):
    if request.args.get("reset"):
        request.session.clear()
        return redirect("/api/counter.star")

    n = request.session.get("count", 0) + 1
    request.session["count"] = n
    return html(PAGE, n=str(n))
