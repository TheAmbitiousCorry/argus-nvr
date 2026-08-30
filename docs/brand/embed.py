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
    s = re.sub(r"\n\s*", "", (BRAND / name).read_text()).strip()
    head = s[: s.index(">") + 1]
    return {
        "svg": s,
        "inner": s[s.index(">") + 1 : s.rindex("</svg>")],
        "viewbox": re.search(r"viewBox='([^']+)'", head).group(1),
        "width": re.search(r"stroke-width='([^']+)'", head).group(1),
    }


def datauri(svg, colour, opacity=None):
    s = svg.replace("currentColor", colour)
    if opacity is not None:
        # Baked into the drawing rather than applied in CSS, so the mark can sit
        # in a background stack with no wrapper element to fade.
        s = s.replace("<svg ", "<svg stroke-opacity='%s' fill-opacity='%s' " % (opacity, opacity), 1)
    return "data:image/svg+xml," + urllib.parse.quote(s, safe="").replace("#", "%23")


def sub(path, pattern, repl, count=1):
    p = pathlib.Path(path)
    text = p.read_text()
    new, n = re.subn(pattern, lambda _: repl, text, count=count, flags=re.S)
    if n != count:
        sys.exit("expected %d matches, found %d in %s" % (count, n, path))
    p.write_text(new)


eye, mark = load("argus-eye.svg"), load("argus-mark.svg")

# 1. Firmware: one C string literal per path, plus the favicon as a data URI.
lines = "\n    ".join('"%s"' % l.replace('"', r"\"") for l in re.findall(r"<[^>]+>", eye["inner"]))
sub(
    FW / "src/web.cpp",
    r'static const char ARGUS_EYE\[\] =.*?"</svg>";',
    (
        "static const char ARGUS_EYE[] =\n"
        "    \"<svg viewBox='%s' fill='none' stroke='currentColor' stroke-width='%s' \"\n"
        "    \"stroke-linecap='round' stroke-linejoin='round' aria-hidden='true'>\"\n"
        "    %s\n"
        '    "</svg>";'
    )
    % (eye["viewbox"], eye["width"], lines),
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
indented = "\n          ".join(re.findall(r"<[^>]+>", mark["inner"]))
sub(
    NVR / "frontend/src/App.vue",
    r'<svg class="mark".*?</svg>',
    (
        '<svg class="mark" viewBox="%s" fill="none" stroke="currentColor"\n'
        '             stroke-width="%s" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">\n'
        "          %s\n"
        "        </svg>"
    )
    % (mark["viewbox"], mark["width"], indented),
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
