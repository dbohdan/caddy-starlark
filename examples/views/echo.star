# POST JSON here to echo it back:
#   curl -XPOST http://localhost:8080/echo.star -d '{"hi":"there"}'

def respond(request):
    if request.method != "POST":
        abort(405, "use POST")
    body = request.json()
    return Response(
        json.encode({"you_sent": body, "method": request.method}),
        content_type="application/json",
    )
