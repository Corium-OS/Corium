#!/usr/bin/env python3
"""Generate the site's documentation pages from the repository's docs/.

The Markdown in docs/ is the single source of truth. It carries no Hugo front
matter, so that it stays readable on GitHub and in an editor; this script adds
what Hugo needs on the way in.

Run via `mise run docs-sync`. Paths are resolved from this file's location, so
running it directly with python3 works from anywhere too.
"""

import os
import pathlib
import re
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
SOURCE = REPO / "docs"
TARGET = pathlib.Path(__file__).resolve().parent / "content" / "docs"

# Which source file lands where, and in what order. Listing pages explicitly
# rather than globbing keeps the navigation deliberate: a new document appears
# in the sidebar because someone decided where it belongs.
PAGES = [
    # (source, section, slug, title, weight, description)
    #
    # The title is set here rather than taken from the document's first heading.
    # Hugo derives a page's URL from its title unless a slug is given, and an
    # ADR heading like "1. Base the OS on fedora-bootc rather than Fedora
    # CoreOS" makes for an unusable one.
    ("concepts.md", "guides", "concepts", "Concepts", 105,
     "How Corium works and why it is shaped this way: the image, the "
     "filesystem contract, first boot, and node identity."),
    ("quickstart.md", "guides", "quickstart", "Quick start", 110,
     "From a published image to a working Kubernetes node in four steps, with "
     "the mistakes that cost the most time."),
    ("install/downloads.md", "install", "downloads", "Downloads", 300,
     "Where the installer ISO, the qcow2 and the image are published, why "
     "nothing is attached to the release, and how to check what you got."),
    ("upgrades.md", "guides", "upgrades", "Upgrades", 120,
     "How a Corium node moves to a new image, how to roll one back, and how to "
     "upgrade a cluster without losing quorum."),
    ("cilium.md", "guides", "cilium", "Cilium", 125,
     "Replacing kube-router with Cilium from the node's own cloud-config: "
     "what k0s stops doing, what installs the chart, and the kube-proxy-free "
     "variant."),
    ("raid.md", "guides", "raid", "Software RAID", 130,
     "Declaring mdadm arrays on a node's spare disks, why they are built "
     "before k0s starts, and where a RAID root filesystem stands today."),
    ("comparison.md", "reference", "comparison", "Comparison", 201,
     "How Corium compares to Talos, Kairos, Flatcar, Bottlerocket and running "
     "k0s on an ordinary distribution -- and when not to use it."),
    ("features.md", "reference", "feature-support", "Feature support", 205,
     "What Corium models, what it passes through to k0s, and what it "
     "deliberately does not do."),
    ("reference.md", "reference", "configuration", "Configuration", 210,
     "Every field of the corium: schema, where the configuration comes from, "
     "and what Corium does with it."),
    ("cli.md", "reference", "cli", "cctl", 215,
     "Every cctl command, the three things it needs to work, what each role "
     "reaches, and how to read a refusal."),
    ("install/proxmox.md", "install", "proxmox", "Proxmox", 301,
     "A single node on a Proxmox host, from the published qcow2 to a cluster "
     "that schedules work, with the commands and the output from a real run."),
    ("install/ha-cluster.md", "install", "ha-cluster", "HA cluster", 302,
     "Building a three-controller cluster by hand: what each node is told, why "
     "the joiners wait for a token that does not exist yet, and how to check it."),
    ("install/openstack.md", "install", "openstack", "OpenStack", 303,
     "A node on an OpenStack cloud from the published qcow2: registering a UEFI "
     "image, and how the corium: block reaches it through the metadata service."),
    ("adr/0001-base-image.md", "reference", "adr-0001-base-image",
     "ADR 1 — Base image", 910,
     "Why the operating system is based on fedora-bootc rather than Fedora "
     "CoreOS, and what that trades away."),
    ("adr/0002-root-filesystem.md", "reference", "adr-0002-root-filesystem",
     "ADR 2 — Root filesystem", 920,
     "Why the root filesystem is ext4 rather than xfs, and why the reason is "
     "about who can build the image."),
    ("adr/0003-software-raid-scope.md", "reference", "adr-0003-software-raid-scope",
     "ADR 3 — Software RAID scope", 930,
     "Why software RAID covers a node's spare disks and not its root "
     "filesystem, and what upstream would have to change."),
    ("adr/0004-management-api.md", "reference", "adr-0004-management-api",
     "ADR 4 — Management API", 940,
     "What a node-local management API is allowed to do, why there is no exec, "
     "and the three ways an operator CA reaches a node — including a "
     "maintenance mode that puts nothing in cloud-init."),
]

# Links between documents change shape on the site: docs/reference.md becomes
# /docs/reference/configuration/, and a link to a source file has to point at
# GitHub because the site does not serve the repository.
# Derived from PAGES rather than written out again. This was a second list
# once, and it drifted the first time a page was added: links to the new page
# kept resolving to GitHub, which looks deliberate and reads as a dead end.
PAGE_URLS = {
    source: f"/docs/{section}/{slug}/"
    for source, section, slug, *_ in PAGES
}

REPO_BLOB = "https://github.com/Corium-OS/Corium/blob/main"


def strip_first_heading(text: str) -> str:
    """Remove the first heading; Doks renders the title from front matter."""
    return re.sub(r"^#\s+.+$\n+", "", text, count=1, flags=re.M)


def rewrite_links(text: str, source: str) -> str:
    """Point links at the site where a page exists, and at GitHub otherwise."""
    source_dir = os.path.dirname(source)

    def replace(match: "re.Match[str]") -> str:
        label, target = match.group(1), match.group(2)

        if target.startswith(("http://", "https://", "#", "mailto:")):
            return match.group(0)

        anchor = ""
        if "#" in target:
            target, anchor = target.split("#", 1)
            anchor = "#" + anchor

        # Resolve the link relative to the document it appears in.
        resolved = os.path.normpath(os.path.join(source_dir, target)) if source_dir else target

        if resolved in PAGE_URLS:
            return f"[{label}]({PAGE_URLS[resolved]}{anchor})"

        # Anything else lives in the repository, not on this site.
        repo_path = os.path.normpath(os.path.join("docs", resolved))
        return f"[{label}]({REPO_BLOB}/{repo_path}{anchor})"

    return re.sub(r"\[([^\]]+)\]\(([^)]+)\)", replace, text)


def sync_logo() -> None:
    """Copy the project mark into the site's asset tree.

    docs/assets/logo.png is the one canonical copy; the theme generates every
    favicon size from website/assets/favicon.png, so it has to live there too.
    Copying rather than committing twice means the two cannot drift.
    """
    source = SOURCE / "assets" / "logo.png"
    if not source.is_file():
        return

    destination = pathlib.Path(__file__).resolve().parent / "assets" / "favicon.png"
    destination.parent.mkdir(parents=True, exist_ok=True)

    if not destination.is_file() or destination.read_bytes() != source.read_bytes():
        destination.write_bytes(source.read_bytes())
        print("docs/assets/logo.png -> assets/favicon.png")


def main() -> int:
    if not SOURCE.is_dir():
        print(f"no docs directory at {SOURCE}", file=sys.stderr)
        return 1

    sync_logo()

    written = 0

    for source, section, slug, title, weight, description in PAGES:
        path = SOURCE / source
        if not path.is_file():
            print(f"missing source document: {path}", file=sys.stderr)
            return 1

        text = path.read_text(encoding="utf-8")
        body = rewrite_links(strip_first_heading(text), source)

        front_matter = (
            "---\n"
            f'title: "{title}"\n'
            f'description: "{description}"\n'
            f'slug: "{slug}"\n'
            "draft: false\n"
            f"weight: {weight}\n"
            "toc: true\n"
            "---\n\n"
            "<!-- Generated by website/sync-docs.py from docs/"
            f"{source}. Edit that file, not this one. -->\n\n"
        )

        destination = TARGET / section / f"{slug}.md"
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_text(front_matter + body, encoding="utf-8")
        print(f"{source} -> content/docs/{section}/{slug}.md")
        written += 1

    print(f"{written} pages generated")
    return 0


if __name__ == "__main__":
    sys.exit(main())
