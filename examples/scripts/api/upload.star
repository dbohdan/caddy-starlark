# File upload demo. Try:
#   echo "hello" > /tmp/up.txt
#   curl -F "f=@/tmp/up.txt" -F "note=hi there" http://localhost:8080/api/upload.star

def respond(request):
    if request.method != "POST":
        abort(405, "POST a multipart/form-data body")

    f = request.files.get("f")
    if f == None:
        abort(400, "no file under field name 'f'")

    return Response(
        json.encode({
            "filename":     f.filename,
            "content_type": f.content_type,
            "size":         f.size,
            "preview":      str(f.read())[:200],
            "note":         request.form.get("note", ""),
        }),
        content_type="application/json",
    )
