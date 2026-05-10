# Demonstrates the placeholder() global, which exposes Caddy placeholders.

def respond(request):
    info = {
        "method": request.method,
        "path": request.path,
        "host": placeholder("http.request.host"),
        "client_ip": placeholder("client_ip", request.remote_addr),
        "user_agent": request.headers.get("User-Agent", ""),
        "uri": placeholder("http.request.uri"),
    }
    return Response(
        json.encode(info),
        content_type="application/json",
    )
