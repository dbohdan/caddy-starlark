# Show the current date and time, both via Caddy placeholders and the
# starlark-go `time` module.
#
#   curl http://localhost:8080/now.star
#   curl http://localhost:8080/now.star?tz=Europe/Vilnius

def respond(request):
    tz = request.args.get("tz", "UTC")

    # starlark-go's `time` module gives a real time value with .year,
    # .month, .format(), arithmetic, etc.
    now = time.now()

    info = {
        # Pre-formatted strings provided by Caddy.
        "placeholder_http":       placeholder("time.now.http"),
        "placeholder_common_log": placeholder("time.now.common_log"),
        "placeholder_unix":       int(placeholder("time.now.unix")),
        "placeholder_year":       int(placeholder("time.now.year")),

        # From the starlark-go time module.
        "iso8601":     now.format("2006-01-02T15:04:05Z07:00"),
        "year":        now.year,
        "month":       now.month,
        "day":         now.day,
        "hour":        now.hour,
        "minute":      now.minute,
        "in_timezone": now.in_location(tz).format("2006-01-02 15:04:05 -0700") if time.is_valid_timezone(tz) else "invalid timezone: " + tz,
    }
    return Response(json.encode(info), content_type="application/json")
