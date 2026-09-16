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


def canonical_url() -> str:
    """The site's own absolute URL, taken from the home page's canonical link."""
    index = PUBLIC / "index.html"
    if not index.is_file():
        return ""

    match = CANONICAL.search(index.read_text(encoding="utf-8", errors="replace"))
    if not match:
        print("no canonical link on the home page; assuming the site root",
              file=sys.stderr)
        return ""

    return next(filter(None, match.groups()), "")


def base_path(url: str) -> str:
    """The subpath the site is served from."""
    path = urlparse(url).path or "/"

    return path if path.endswith("/") else path + "/"


def site_origin(url: str) -> str:
    """The scheme and host the site is published under, or empty if unknown.

    Links to our own host are not external, however they are spelled. Treating
    them as external is how the "Get started" button shipped pointing at
    https://corium-os.github.io/docs/... -- correct host, missing /Corium/, a
    404 for every visitor who pressed it. Hugo's absURL drops the baseURL path
    when handed a leading slash, so this is a mistake the templates invite.
    """
    parsed = urlparse(url)

    if not parsed.scheme or not parsed.netloc:
        return ""

    return f"{parsed.scheme}://{parsed.netloc}"


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

    canonical = canonical_url()
    prefix = base_path(canonical)
    origin = site_origin(canonical)

    print(f"site is served from {prefix!r}"
          + (f" on {origin}" if origin else ""))

    broken: list[tuple[str, str]] = []
    checked = 0

    for page in PUBLIC.rglob("*.html"):
        text = page.read_text(encoding="utf-8", errors="replace")
        source = page.relative_to(PUBLIC)

        for match in ATTRIBUTE.finditer(text):
            target = next(filter(None, match.groups()), "")

            # Internal links only: other sites are not ours to guarantee, and
            # fragments resolve within the page.
            #
            # A link to our own host counts as internal even when it is spelled
            # in full. That is not pedantry: the one class of breakage this
            # script was written for -- a link that loses the subpath GitHub
            # Pages serves from -- produces exactly that shape.
            if origin and target.startswith(origin):
                target = target[len(origin):] or "/"
            elif not target.startswith("/"):
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
