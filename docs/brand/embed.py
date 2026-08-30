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
    """The marks are filled outlines in two paths: the creature, and its pupils.
    Nothing is stroked, so there is no stroke width to carry across."""
    s = re.sub(r"\n\s*", "", (BRAND / name).read_text()).strip()
    head = s[: s.index(">") + 1]
    return {
        "svg": s,
        "paths": re.findall(r"<path d='([^']+)'/>", s),
        "viewbox": re.search(r"viewBox='([^']+)'", head).group(1),
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

# 1. Firmware: the two paths as C string literals, plus the favicon as a data
# URI. A coordinate list this long is broken across lines the compiler joins.
def wrap(d, width=96):
    out, line = [], ""
    for piece in re.findall(r"[ML][^ML]*|Z", d):
        if len(line) + len(piece) > width:
            out.append(line)
            line = ""
        line += piece
    if line:
        out.append(line)
    return "\n    ".join('"%s"' % l for l in out)

sub(
    FW / "src/web.cpp",
    r'static const char ARGUS_EYE\[\] =.*?"</svg>";',
    (
        "static const char ARGUS_EYE[] =\n"
        "    \"<svg viewBox='%s' fill='currentColor' fill-rule='nonzero' aria-hidden='true'>\"\n"
        "    \"<path d=\'\"\n    %s\n    \"\'/>\"\n"
        "    \"<path d=\'\"\n    %s\n    \"\'/>\"\n"
        '    "</svg>";'
    )
    % (eye["viewbox"], wrap(eye["paths"][0]), wrap(eye["paths"][1])),
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
    (
        '<svg class="mark" viewBox="%s" fill="currentColor" fill-rule="nonzero" aria-hidden="true">\n'
        '          <path d="%s" />\n'
        '          <path d="%s" />\n'
        "        </svg>"
    )
    % (mark["viewbox"], mark["paths"][0], mark["paths"][1]),
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
