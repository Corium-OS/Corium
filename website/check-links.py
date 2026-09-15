#!/usr/bin/env python3
"""Check that every internal link in the built site resolves to a real file.

This exists because the site shipped with broken links twice: an absolute path
in a menu that dropped the subpath GitHub Pages serves from, and a theme bug
that prefixed an icon twice. Both were invisible locally, because a site built
without the production baseURL does not reproduce either.

Run against website/public after `hugo`.
"""

from __future__ import annotations

import pathlib
import re
import sys
from urllib.parse import unquote, urlparse

PUBLIC = pathlib.Path(__file__).resolve().parent / "public"

# Matches href= and src=, quoted or not: Hugo's minifier drops the quotes.
ATTRIBUTE = re.compile(r'(?:href|src)=(?:"([^"]+)"|\'([^\']+)\'|([^\s>]+))')


# The home page's canonical link is the site's absolute URL, which is where the
# subpath comes from. Deriving it from any old link instead is not safe: the
# first absolute link on the page is whatever the theme happened to emit, and a
# single stray link would redefine the prefix and mark every other link broken.
CANONICAL = re.compile(r'rel=(?:"canonical"|\'canonical\'|canonical)[^>]*?'
                       r'href=(?:"([^"]+)"|\'([^\']+)\'|([^\s>]+))')


def base_path() -> str:
    """The subpath the site is served from."""
    index = PUBLIC / "index.html"
    if not index.is_file():
        return "/"

    match = CANONICAL.search(index.read_text(encoding="utf-8", errors="replace"))
    if not match:
        print("no canonical link on the home page; assuming the site root",
              file=sys.stderr)
        return "/"

    url = next(filter(None, match.groups()), "")
    path = urlparse(url).path or "/"

    return path if path.endswith("/") else path + "/"


def resolve(target: str, prefix: str) -> pathlib.Path | None:
    """Map an internal URL onto the file that should satisfy it."""
    path = unquote(urlparse(target).path)

    if not path.startswith(prefix):
        return None

    relative = path[len(prefix):].lstrip("/")
    candidate = PUBLIC / relative

    # A directory URL is served by its index.html.
    if target.endswith("/") or not relative:
        return candidate / "index.html"

    return candidate


def main() -> int:
    if not PUBLIC.is_dir():
        print(f"no built site at {PUBLIC}; run hugo first", file=sys.stderr)
        return 1

    prefix = base_path()
    print(f"site is served from {prefix!r}")

    broken: list[tuple[str, str]] = []
    checked = 0

    for page in PUBLIC.rglob("*.html"):
        text = page.read_text(encoding="utf-8", errors="replace")
        source = page.relative_to(PUBLIC)

        for match in ATTRIBUTE.finditer(text):
            target = next(filter(None, match.groups()), "")

            # Only internal, absolute links: external ones are not ours to
            # guarantee, and fragments resolve within the page.
            if not target.startswith("/"):
                continue

            checked += 1
            resolved = resolve(target, prefix)

            if resolved is None:
                broken.append((str(source), f"{target} (outside {prefix})"))
            elif not resolved.exists():
                broken.append((str(source), target))

    if broken:
        print(f"\n{len(broken)} broken link(s):", file=sys.stderr)
        for source, target in sorted(set(broken)):
            print(f"  {source}: {target}", file=sys.stderr)
        return 1

    print(f"{checked} internal links checked, all resolve")
    return 0


if __name__ == "__main__":
    sys.exit(main())
