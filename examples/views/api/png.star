# Generate a PNG dynamically. Demonstrates returning binary content.
#
# Pure Starlark implementation of CRC32, Adler32, and the uncompressed
# deflate stream — no helpers needed beyond the bytes() builtin.
#
#   curl -o square.png 'http://localhost:8080/api/png.star?color=ff8800&size=64'

# Precomputed CRC32 table (polynomial 0xedb88320).
# Computed once pergprogram load, since the handler caches parsed programs by mtime.
def _make_crc_table():
    table = []
    for n in range(256):
        c = n
        for _ in range(8):
            c = (0xedb88320 ^ (c >> 1)) if (c & 1) else (c >> 1)
        table.append(c)
    return table

CRC_TABLE = _make_crc_table()

def crc32(ints):
    crc = 0xffffffff
    for x in ints:
        crc = CRC_TABLE[(crc ^ x) & 0xff] ^ (crc >> 8)
    return crc ^ 0xffffffff

def adler32(ints):
    a, b = 1, 0
    for x in ints:
        a = (a + x) % 65521
        b = (b + a) % 65521
    return (b << 16) | a

def be32(n):
    return [(n >> 24) & 0xff, (n >> 16) & 0xff, (n >> 8) & 0xff, n & 0xff]

def le16(n):
    return [n & 0xff, (n >> 8) & 0xff]

def ascii4(s):
    return [ord(s[0]), ord(s[1]), ord(s[2]), ord(s[3])]

def chunk(type_str, data):
    body = ascii4(type_str) + data
    return be32(len(data)) + body + be32(crc32(body))

def deflate_stored(data):
    # One zlib block, BTYPE=00 (stored), BFINAL=1.
    n = len(data)
    nlen = n ^ 0xffff
    return [0x78, 0x01, 0x01] + le16(n) + le16(nlen) + data + be32(adler32(data))

HEXD = "0123456789abcdef"

def parse_color(s):
    fallback = (255, 0, 0)
    if len(s) != 6:
        return fallback
    s = s.lower()
    out = []
    for i in range(0, 6, 2):
        hi = HEXD.find(s[i])
        lo = HEXD.find(s[i + 1])
        if hi < 0 or lo < 0:
            return fallback
        out.append(hi * 16 + lo)
    return (out[0], out[1], out[2])

def respond(request):
    size_str = request.args.get("size", "32")
    size = int(size_str)
    if size < 1 or size > 256:
        abort(400, "size must be between 1 and 256")
    r, g, b = parse_color(request.args.get("color", "ff0000"))

    # IHDR data: width, height, bit_depth=8, color_type=2 (RGB),
    # compression=0, filter=0, interlace=0.
    ihdr = be32(size) + be32(size) + [8, 2, 0, 0, 0]

    # Raw scanlines: filter byte 0 ("None") then `size` pixels of (R,G,B).
    scanline = [0] + [r, g, b] * size
    raw = scanline * size

    sig = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]
    png = sig + chunk("IHDR", ihdr) + chunk("IDAT", deflate_stored(raw)) + chunk("IEND", [])

    return Response(bytes(png), content_type="image/png")
