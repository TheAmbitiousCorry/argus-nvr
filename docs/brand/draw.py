#!/usr/bin/env python3
"""Draws the Argus marks as closed outlines in a single path.

The marks were strokes: a dozen open centrelines with round caps, which a
renderer paints correctly and every other tool treats as a dozen separate
objects. This emits filled outlines instead, so each mark is one path with one
fill and no stroke at all, and the pupil is the only thing left beside it.

There is no boolean union here and none is needed. Every subpath is closed and
wound the same way, so `fill-rule: nonzero` paints their union: where two
subpaths overlap the winding numbers add rather than cancel, and the overlap
fills solid. Holes are the exception and are wound backwards on purpose.

Geometry is generated rather than typed. Hand-writing lashes is how some eyes
ended up with two brows and others three, and hand-fitting a neck to a coil is
how they came to cross it.
"""
import math
import pathlib
import re

BRAND = pathlib.Path(__file__).resolve().parent

# How finely curves are sampled. These marks are drawn at 17px in a sidebar and
# 46px on a sign-in page, and one of them is compiled into a microcontroller
# with 30% of its flash left, so points are not free. Raise it if a mark is ever
# printed large.
DETAIL = 0.45


# --- curves -----------------------------------------------------------------

def quad(p0, p1, p2, n=None):
    """Samples a quadratic Bezier into a polyline."""
    n = n or max(6, round(24 * DETAIL))
    out = []
    for i in range(n + 1):
        t = i / n
        u = 1 - t
        out.append((u * u * p0[0] + 2 * u * t * p1[0] + t * t * p2[0],
                    u * u * p0[1] + 2 * u * t * p1[1] + t * t * p2[1]))
    return out


def cubic(p0, p1, p2, p3, n=None):
    n = n or max(6, round(26 * DETAIL))
    out = []
    for i in range(n + 1):
        t = i / n
        u = 1 - t
        out.append((u**3 * p0[0] + 3 * u * u * t * p1[0] + 3 * u * t * t * p2[0] + t**3 * p3[0],
                    u**3 * p0[1] + 3 * u * u * t * p1[1] + 3 * u * t * t * p2[1] + t**3 * p3[1]))
    return out


def trace(d_attr):
    """Samples a path written as M followed by relative c and s curves.

    A command letter may be followed by several coordinate sets, which is how
    the coil is written: one s carrying six curves. Reading only the first set
    per letter is why it came out as a stump.

    The necks and the coil are copied verbatim from the drawing this replaced,
    because their shape was the part worth keeping and re-deriving it by hand
    turned five curves into five straight lines converging on a point.
    """
    tokens = re.findall(r"[MmCcSs]|-?\d*\.?\d+", d_attr)
    pts, cur, prev2, cmd = [], (0.0, 0.0), None, None
    i = 0
    while i < len(tokens):
        if tokens[i] in "MmCcSs":
            cmd = tokens[i]
            i += 1
            continue
        n = lambda k: float(tokens[i + k])
        if cmd in "Mm":
            cur = (n(0), n(1)); i += 2
            pts.append(cur); prev2 = None
            cmd = "c" if cmd == "m" else "C"  # a further set after M is a lineto
        elif cmd in "Cc":
            p1 = (cur[0] + n(0), cur[1] + n(1))
            p2 = (cur[0] + n(2), cur[1] + n(3))
            p3 = (cur[0] + n(4), cur[1] + n(5)); i += 6
            pts += cubic(cur, p1, p2, p3)[1:]; prev2, cur = p2, p3
        elif cmd in "Ss":
            p1 = (2 * cur[0] - prev2[0], 2 * cur[1] - prev2[1]) if prev2 else cur
            p2 = (cur[0] + n(0), cur[1] + n(1))
            p3 = (cur[0] + n(2), cur[1] + n(3)); i += 4
            pts += cubic(cur, p1, p2, p3)[1:]; prev2, cur = p2, p3
        else:
            i += 1
    return pts


def normals(pts):
    """Unit normals per point, averaged across each joint so an offset corner
    does not pinch."""
    out = []
    for i, _ in enumerate(pts):
        a = pts[max(i - 1, 0)]
        b = pts[min(i + 1, len(pts) - 1)]
        dx, dy = b[0] - a[0], b[1] - a[1]
        d = math.hypot(dx, dy) or 1.0
        out.append((-dy / d, dx / d))
    return out


def cap(centre, frm, to, n=None):
    """A round cap as an arc from one offset edge to the other, so an outlined
    line ends the way a stroked one with round caps did."""
    a0 = math.atan2(frm[1] - centre[1], frm[0] - centre[0])
    a1 = math.atan2(to[1] - centre[1], to[0] - centre[0])
    r = math.hypot(frm[0] - centre[0], frm[1] - centre[1])
    while a1 - a0 > math.pi:
        a1 -= 2 * math.pi
    while a0 - a1 > math.pi:
        a1 += 2 * math.pi
    n = n or max(3, round(8 * DETAIL))
    return [(centre[0] + math.cos(a0 + (a1 - a0) * i / n) * r,
             centre[1] + math.sin(a0 + (a1 - a0) * i / n) * r) for i in range(1, n)]


def ribbon(pts, w):
    """An open polyline as a closed outline: out along one side, round the end,
    back along the other, round the start."""
    h = w / 2
    nm = normals(pts)
    left = [(p[0] + n[0] * h, p[1] + n[1] * h) for p, n in zip(pts, nm)]
    right = [(p[0] - n[0] * h, p[1] - n[1] * h) for p, n in zip(pts, nm)]
    return left + cap(pts[-1], left[-1], right[-1]) + right[::-1] + cap(pts[0], right[0], left[0])


def loop(pts, w, hole=True):
    """A closed curve as a ring: an outer offset, and an inner offset wound
    backwards so it reads as a hole rather than filling the middle."""
    h = w / 2
    nm = normals(pts)
    outer = [(p[0] + n[0] * h, p[1] + n[1] * h) for p, n in zip(pts, nm)]
    inner = [(p[0] - n[0] * h, p[1] - n[1] * h) for p, n in zip(pts, nm)]
    return [outer, inner[::-1] if hole else inner]


def ring(cx, cy, r, w, n=None):
    n = n or max(10, round(40 * DETAIL))
    pts = [(cx + math.cos(2 * math.pi * i / n) * r, cy + math.sin(2 * math.pi * i / n) * r)
           for i in range(n)]
    return loop(pts, w)


def disc(cx, cy, r, n=None):
    n = n or max(8, round(28 * DETAIL))
    return [[(cx + math.cos(2 * math.pi * i / n) * r, cy + math.sin(2 * math.pi * i / n) * r)
             for i in range(n)]]


def d(subpaths):
    out = []
    for sp in subpaths:
        fmt = lambda p: "%s %s" % (("%.1f" % p[0]).rstrip("0").rstrip("."),
                                   ("%.1f" % p[1]).rstrip("0").rstrip("."))
        out.append("M" + fmt(sp[0]) + "".join("L" + fmt(p) for p in sp[1:]) + "Z")
    return "".join(out)


# --- the parts of the creature ----------------------------------------------

def eye(cx, cy, hw, hh, w, lashes=5, spread=118, gap=3.2, reach=0.95):
    """A lens, an iris ring, and a fan of lashes. Every eye gets the same number
    of lashes at the same angles whatever its size, because they are placed by
    angle rather than by hand."""
    lens = (quad((cx - hw, cy), (cx, cy - hh * 2), (cx + hw, cy))
            + quad((cx + hw, cy), (cx, cy + hh * 2), (cx - hw, cy))[1:])
    parts = loop(lens, w)
    # The ring has to clear its own thickness or its inner offset collapses
    # through the centre and the hole turns inside out, which is how the irises
    # came out as little squares.
    parts += ring(cx, cy, max(min(hw, hh) * 0.50, w * 0.95), w)
    for i in range(lashes):
        a = math.radians(-90 - spread / 2 + spread * i / (lashes - 1))
        ca, sa = math.cos(a), math.sin(a)
        ax, ay = hw * 0.66 + gap, hh * 1.25 + gap
        r = min(hw, hh) * reach + w * 0.5
        parts.append(ribbon([(cx + ca * ax, cy + sa * ay),
                             (cx + ca * (ax + r), cy + sa * (ay + r))], w))
    return parts


def neck(p0, p1, p2, w):
    return [ribbon(quad(p0, p1, p2), w)]


def coil(cx, cy, rx, ry, w, turns=1.55, n=None):
    """One spiral drawn from the outside in. Self-overlap is fine: the subpaths
    are all wound the same way, so nonzero fill unions them."""
    n = n or max(40, round(110 * DETAIL))
    pts = []
    for i in range(n + 1):
        t = i / n
        a = t * turns * 2 * math.pi
        k = 1 - t * 0.78
        pts.append((cx + math.cos(a + math.pi) * rx * k, cy + math.sin(a + math.pi) * ry * k))
    return [ribbon(pts, w)]


# --- the two marks ----------------------------------------------------------

# The necks and the coil, exactly as they were drawn in the version this
# replaces. Their shape is the part that was worth keeping.
NECKS = [
    "M60 31c0 12-5 16-5 28s5 14 5 22",
    "M24 46c0 12 6 15 10 24s2 14 8 18",
    "M96 46c0 12-6 15-10 24s-2 14-8 18",
    "M12 73c4 8 12 9 18 14s4 10 8 13",
    "M108 73c-4 8-12 9-18 14s-4 10-8 13",
]
COIL = ("M60 79c14 0 24 6 24 14s-10 14-24 14-24-6-24-14 9-11 18-11 14 4 14 9"
        "-4 6-8 6-6-2-6-4")


def build_mark():
    """The composition that was there first, rebuilt as outlines: eyes fanned
    wide across an arc, necks curving down and inward, a coil beneath. What
    changed is that every eye now gets the same five lashes, and they are short
    enough not to land on the eye beside them."""
    W = 4.4
    EYES = [(60, 22, 14.0, 9.0), (24, 38, 13.0, 8.0), (96, 38, 13.0, 8.0),
            (10, 66, 10.0, 6.0), (110, 66, 10.0, 6.0)]
    parts = [ribbon(trace(n), W) for n in NECKS]
    parts.append(ribbon(trace(COIL), W))
    pupils = []
    for x, y, hw, hh in EYES:
        parts += eye(x, y, hw, hh, W, gap=2.4, reach=0.62)
        pupils += disc(x, y, max(min(hw, hh) * 0.19, W * 0.36))
    return "0 0 120 110", parts, pupils


def build_eye():
    W = 4.6
    parts = neck((42, 80), (48, 64), (42, 48), W)
    parts += eye(42, 32, 24, 15, W)
    parts += coil(42, 93, 13, 10.5, W, turns=1.5)
    return "0 0 84 116", parts, disc(42, 32, 3.6)


def write(name, viewbox, parts, pupils):
    svg = ("<svg xmlns='http://www.w3.org/2000/svg' viewBox='%s' fill='currentColor' "
           "fill-rule='nonzero'>\n"
           "  <path d='%s'/>\n"
           "  <path d='%s'/>\n"
           "</svg>\n" % (viewbox, d(parts), d(pupils)))
    (BRAND / name).write_text(svg)
    return len(svg)


if __name__ == "__main__":
    print("argus-mark.svg %5d bytes" % write("argus-mark.svg", *build_mark()))
    print("argus-eye.svg  %5d bytes" % write("argus-eye.svg", *build_eye()))
