#!/usr/bin/env python3
"""Regenerates every embedded copy of the marks from docs/brand/*.svg.

The marks appear inline in five places across two repositories, because inlining
is what avoids a second request on a microcontroller and a flash of nothing on
the web. Five hand-maintained copies is five chances to fix a drawing in four of
them, so they are generated from the two files here instead.
"""
import re
import sys
import pathlib
import urllib.parse

BRAND = pathlib.Path(__file__).resolve().parent
NVR = BRAND.parent.parent
FW = NVR.parent / "esp32-cam-fw"


def load(name):
    """Normalises one of the traced marks into something embeddable.

    The files are traced artwork: an XML declaration, a doctype, a size in
    points, and the drawing inside a group that flips the y axis. None of that
    survives being inlined, and the fixed size in particular would override
    whatever the page asks for. What is kept is the viewBox, the transform, and
    the paths, with the black swapped for currentColor so a mark takes the
    colour of whatever it sits in.
    """
    raw = (BRAND / name).read_text()
    viewbox = re.search(r'viewBox="([^"]+)"', raw).group(1)
    transform = re.search(r'transform="([^"]+)"', raw).group(1)
    paths = [re.sub(r"\s+", " ", d).strip()
             for d in re.findall(r'<path d="([^"]+)"', raw)]
    inner = ('<g transform="%s" fill="currentColor" stroke="none">%s</g>'
             % (transform, "".join('<path d="%s"/>' % d for d in paths)))
    return {
        "svg": '<svg xmlns="http://www.w3.org/2000/svg" viewBox="%s">%s</svg>' % (viewbox, inner),
        "inner": inner,
        "paths": paths,
        "transform": transform,
        "viewbox": viewbox,
    }


def datauri(svg, colour, opacity=None):
    s = svg.replace("currentColor", colour)
    if opacity is not None:
        # Baked into the drawing rather than applied in CSS, so the mark can sit
        # in a background stack with no wrapper element to fade.
        s = s.replace("<svg ", "<svg fill-opacity='%s' " % opacity, 1)
    return "data:image/svg+xml," + urllib.parse.quote(s, safe="").replace("#", "%23")


def sub(path, pattern, repl, count=1):
    p = pathlib.Path(path)
    text = p.read_text()
    new, n = re.subn(pattern, lambda _: repl, text, count=count, flags=re.S)
    if n != count:
        sys.exit("expected %d matches, found %d in %s" % (count, n, path))
    p.write_text(new)


eye, mark = load("argus-eye.svg"), load("argus-mark.svg")

# 1. Firmware: the drawing as one C string, split where the compiler rejoins it,
# plus the favicon as a data URI. Double quotes inside the markup become single
# ones so the literal does not need escaping on every attribute.
def wrap(text, width=94):
    text = text.replace('"', "'")
    out, line = [], ""
    for word in re.findall(r"\S+\s*", text):
        if line and len(line) + len(word) > width:
            out.append(line)
            line = ""
        line += word
    if line:
        out.append(line)
    return "\n    ".join('"%s"' % l for l in out)

sub(
    FW / "src/web.cpp",
    r'static const char ARGUS_EYE\[\] =.*?"</svg>";',
    (
        "static const char ARGUS_EYE[] =\n"
        "    \"<svg viewBox='%s' aria-hidden='true'>\"\n"
        "    %s\n"
        '    "</svg>";'
    )
    % (eye["viewbox"], wrap(eye["inner"])),
)
sub(
    FW / "src/web.cpp",
    r'"<link rel=icon href=\\"[^"]*\\">";',
    '"<link rel=icon href=\\"%s\\">";' % datauri(eye["svg"], "#e8e8e8"),
)

# 2. The two files the browser fetches for the favicon.
for name in ("argus-eye.svg", "argus-mark.svg"):
    (NVR / "frontend/public" / name).write_text((BRAND / name).read_text())

# 3. The mark inline in the navigation, so it paints with the first frame.
sub(
    NVR / "frontend/src/App.vue",
    r'<svg class="mark".*?</svg>',
    ('<svg class="mark" viewBox="%s" aria-hidden="true">\n'
     '          <g transform="%s" fill="currentColor" stroke="none">\n%s\n          </g>\n'
     "        </svg>")
    % (mark["viewbox"], mark["transform"],
       "\n".join('            <path d="%s" />' % d for d in mark["paths"])),
)

# 4. Behind the pages.
sub(
    NVR / "frontend/src/style.css",
    r'url\("data:image/svg\+xml,[^"]*"\)',
    'url("%s")' % datauri(mark["svg"], "#ffffff", "0.05"),
)

# 5. On a tile with no picture, in both of its background rules.
sub(
    NVR / "frontend/src/components/CameraStream.vue",
    r'url\("data:image/svg\+xml,[^"]*"\)',
    'url("%s")' % datauri(eye["svg"], "#ffffff", "0.07"),
    count=2,
)

print("regenerated 5 embedded copies from docs/brand/")
