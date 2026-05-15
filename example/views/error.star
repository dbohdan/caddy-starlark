def respond(request):
    code = int(placeholder("http.error.status_code"))
    text = placeholder("http.error.status_text")
    message = placeholder("http.error.message")

    if message.strip() != "":
        if not message.endswith("."):
            message += "."
    elif code == 403:
        message = "You don't have permission to access this resource."
    elif code == 404:
        message = "The requested URL was not found on this server."
    elif code == 500:
        message = "An internal server error has occurred."
    else:
        message = text + "."

    return html(
        """<!DOCTYPE html>
<html lang="en">

<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">

    <title>{code} {text}</title>

    <link rel="icon" href="/favicon.ico" type="image/x-icon">
</head>

<body>
    <h1>Error {code}</h1>

    <p>{message}</p>

    <ul>
        <li><a href="/">Home</a></li>
        <li><a href="#" onclick="javascript:history.back(); return false;">Back</a></li>
        <li><a href="{url}" onclick="javascript:window.location.reload(); return false;">Reload</a></li>
    </ul>
</body>

</html>
""",
        code=code,
        text=text,
        message=message,
        url=request.url,
    ), code
