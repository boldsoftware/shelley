#!/usr/bin/env python3
"""Extract headless-shell from chromedp/headless-shell:stable for each arch.

Produces headless-shell-linux-{amd64,arm64}.tar.gz tarballs and a
headless-shell-version.txt with the Chromium version string.

Usage: ./extract-headless-shell.py <output_dir>
"""

import json
import os
import subprocess
import sys
import tempfile
import urllib.request
from pathlib import Path

IMAGE = "chromedp/headless-shell:stable"
ARCHES = ["amd64", "arm64"]
REGISTRY_API = "https://registry.hub.docker.com/v2/repositories/chromedp/headless-shell/tags"


def resolve_stable_version() -> str:
    """Resolve :stable to a version by matching Docker Hub digests."""
    url = f"{REGISTRY_API}?page_size=100&ordering=last_updated"
    with urllib.request.urlopen(url, timeout=30) as resp:
        data = json.load(resp)

    stable_digest = None
    for tag in data["results"]:
        if tag["name"] == "stable":
            stable_digest = tag["digest"]
            break

    if not stable_digest:
        print("ERROR: 'stable' tag not found in registry", file=sys.stderr)
        sys.exit(1)

    for tag in data["results"]:
        name = tag["name"]
        if name[:1].isdigit() and tag["digest"] == stable_digest:
            return name

    print("ERROR: no version tag matches stable digest", file=sys.stderr)
    sys.exit(1)


def docker(*args: str, capture: bool = False, quiet: bool = False) -> str:
    """Run a docker command, returning stdout if capture=True."""
    kwargs: dict = {"check": True, "text": True}
    if capture or quiet:
        kwargs["capture_output"] = True
    result = subprocess.run(["docker", *args], **kwargs)
    return result.stdout.strip() if capture else ""


def extract_arch(arch: str, output_dir: Path) -> None:
    """Extract headless-shell for a single architecture into a tarball."""
    print(f"Extracting headless-shell for {arch}...")

    with tempfile.TemporaryDirectory() as tmpdir:
        cid = docker("create", "--platform", f"linux/{arch}", IMAGE, "/bin/true", capture=True)
        try:
            docker("cp", f"{cid}:/headless-shell/.", f"{tmpdir}/headless-shell/")
        finally:
            subprocess.run(["docker", "rm", cid], capture_output=True)

        tarball = output_dir / f"headless-shell-linux-{arch}.tar.gz"
        subprocess.run(
            ["tar", "-czf", str(tarball), "-C", tmpdir, "headless-shell/"],
            check=True,
        )
        size_mb = tarball.stat().st_size / (1024 * 1024)
        print(f"  Created {tarball} ({size_mb:.0f}M)")


def main() -> None:
    # --resolve-version: just print the version number and exit
    if len(sys.argv) == 2 and sys.argv[1] == "--resolve-version":
        print(resolve_stable_version())
        return

    if len(sys.argv) < 2:
        print(f"Usage: {sys.argv[0]} <output_dir>", file=sys.stderr)
        sys.exit(1)

    output_dir = Path(sys.argv[1])
    output_dir.mkdir(parents=True, exist_ok=True)

    # Resolve version from registry
    print(f"Resolving {IMAGE} version...")
    version = resolve_stable_version()
    chromium_version = f"Chromium {version}"
    print(f"  {chromium_version}")
    (output_dir / "headless-shell-version.txt").write_text(chromium_version + "\n")

    # Pull for all platforms
    for arch in ARCHES:
        print(f"Pulling {IMAGE} for linux/{arch}...")
        docker("pull", "--platform", f"linux/{arch}", IMAGE, quiet=True)

    # Extract
    for arch in ARCHES:
        extract_arch(arch, output_dir)

    print(f"Done. Version: {chromium_version}")


if __name__ == "__main__":
    main()
