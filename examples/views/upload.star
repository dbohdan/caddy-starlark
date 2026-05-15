# File upload demo. Try:
#   echo "hello" > /tmp/up.txt
#   curl -F "f=@/tmp/up.txt" -F "note=hi there" http://localhost:8080/upload.star

FORM_HTML = """<!doctype html>
<html><body>
<h1>Upload</h1>
<form method="POST" enctype="multipart/form-data">
  <p><input type="file" name="f" required></p>
  <p><input type="text" name="note" placeholder="note"></p>
  <p><button type="submit">Upload</button></p>
</form>
</body></html>
"""


def respond(request):
    if request.method == "GET":
        return FORM_HTML

    if request.method != "POST":
        abort(405, "POST a multipart/form-data body")

    f = request.files.get("f")
    if f == None:
        abort(400, "no file under field name 'f'")

    return Response(
        json.encode(
            {
                "filename": f.filename,
                "content_type": f.content_type,
                "size": f.size,
                "preview": str(f.read())[:200],
                "note": request.form.get("note", ""),
            }
        ),
        content_type="application/json",
    )
