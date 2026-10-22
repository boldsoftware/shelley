#!/usr/bin/env python3
"""Generate version metadata for GitHub Pages.

Generates release.json, commits.json, and index.html.
"""

import json
import subprocess
import sys
import time
import urllib.parse
from pathlib import Path


# The headless-shell release to advertise instead of the newest one, on the
# platforms it has a build for. Chromium releases, and so the
# chromedp/headless-shell builds the release workflow publishes, still trust
# the compromised eMudhra intermediates
# (https://emudhra.we-have-a-problem.com). This release is Chromium
# 156.0.8078.48 for linux/amd64 with their blocklist (chromium da63b94c4ee2)
# applied. Remove the pin once a chromedp build has the blocklist.
HEADLESS_SHELL_PIN = "headless-shell/v156.0.8078.48.p1"

HEADLESS_SHELL_PLATFORMS = ("linux_amd64", "linux_arm64")
RELEASES_URL = "https://github.com/boldsoftware/shelley/releases/download"


def get_headless_shells() -> tuple[str, dict[str, dict[str, str]]]:
    """Choose a headless-shell build for each platform.

    Each platform gets the pinned release's build if it has one, else the
    newest release's that has one. Returns the pinned (or newest) release
    with builds and {platform: {"version": "Chromium 147.0.7727.24", "url": ...}}.
    Shelley releases far outnumber headless-shell releases, so this goes by
    git tags rather than by recent releases.
    """
    tags = subprocess.check_output(
        ["git", "tag", "--list", "headless-shell/v*", "--sort=-version:refname"],
        text=True,
    ).split()
    if not tags:
        print("ERROR: no headless-shell/v* tags found", file=sys.stderr)
        sys.exit(1)
    if HEADLESS_SHELL_PIN:
        if HEADLESS_SHELL_PIN not in tags:
            print(f"ERROR: pinned tag {HEADLESS_SHELL_PIN} not found", file=sys.stderr)
            sys.exit(1)
        tags.remove(HEADLESS_SHELL_PIN)
        tags.insert(0, HEADLESS_SHELL_PIN)
    builds = {}
    first = None
    for tag in tags:
        assets = release_assets(tag)
        for platform in HEADLESS_SHELL_PLATFORMS:
            name = f"headless-shell-{platform.replace('_', '-')}.tar.gz"
            if platform not in builds and name in assets:
                builds[platform] = {
                    "version": f"Chromium {tag.removeprefix('headless-shell/v')}",
                    "url": f"{RELEASES_URL}/{tag}/{name}",
                }
        if tag == HEADLESS_SHELL_PIN and not builds:
            print(f"ERROR: pinned release {HEADLESS_SHELL_PIN} has no headless-shell builds", file=sys.stderr)
            sys.exit(1)
        first = first or (tag if builds else None)
        if len(builds) == len(HEADLESS_SHELL_PLATFORMS):
            return first, builds
    print(f"ERROR: no headless-shell build for {set(HEADLESS_SHELL_PLATFORMS) - set(builds)}", file=sys.stderr)
    sys.exit(1)


def release_assets(tag: str) -> list[str]:
    """Names of the uploaded assets of tag's GitHub release; none if it has
    no release or is a prerelease."""
    for attempt in range(3):
        result = subprocess.run(
            ["gh", "api", f"repos/boldsoftware/shelley/releases/tags/{urllib.parse.quote(tag, safe='')}",
             "--jq", 'select(.prerelease | not) | .assets[] | select(.state == "uploaded") | .name'],
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            return result.stdout.split()
        if "HTTP 404" in result.stderr:
            return []
        time.sleep(10 * (attempt + 1))
    print(f"ERROR: release {tag}: {result.stderr.strip()}", file=sys.stderr)
    sys.exit(1)


def generate_release_json(output_dir: Path) -> None:
    """Generate release.json with latest release information."""
    # Get latest tag - fail if none exists
    result = subprocess.run(
        ["git", "describe", "--tags", "--abbrev=0", "--match", "v*"],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        print("ERROR: No tags found. Run this after creating a release.", file=sys.stderr)
        sys.exit(1)

    latest_tag = result.stdout.strip()
    latest_commit = subprocess.check_output(
        ["git", "rev-list", "-n", "1", latest_tag], text=True
    ).strip()
    latest_commit_short = latest_commit[:7]
    latest_commit_time = subprocess.check_output(
        ["git", "show", "-s", "--format=%cI", latest_commit], text=True
    ).strip()
    # Use for-each-ref to reliably get the tag creation time.
    # 'git show -s --format=%cI <tag>' on annotated tags returns the full
    # tag message instead of just the date.
    published_at = subprocess.check_output(
        ["git", "for-each-ref", "--format=%(creatordate:iso-strict)",
         f"refs/tags/{latest_tag}"], text=True
    ).strip()

    version = latest_tag[1:] if latest_tag.startswith("v") else latest_tag

    base_url = f"https://github.com/boldsoftware/shelley/releases/download/{latest_tag}"

    release_info = {
        "tag_name": latest_tag,
        "version": version,
        "commit": latest_commit_short,
        "commit_full": latest_commit,
        "commit_time": latest_commit_time,
        "published_at": published_at,
        "download_urls": {
            "darwin_amd64": f"{base_url}/shelley_darwin_amd64",
            "darwin_arm64": f"{base_url}/shelley_darwin_arm64",
            "linux_amd64": f"{base_url}/shelley_linux_amd64",
            "linux_arm64": f"{base_url}/shelley_linux_arm64",
        },
        "checksums_url": f"{base_url}/checksums.txt",
    }

    hs_tag, hs_builds = get_headless_shells()
    release_info["headless_shells"] = hs_builds
    # Shelley before headless_shells reads one version for every platform:
    # the pinned (or newest) release, with only the builds it has.
    hs_version = f"Chromium {hs_tag.removeprefix('headless-shell/v')}"
    release_info["headless_shell_version"] = hs_version
    release_info["headless_shell_urls"] = {
        platform: build["url"] for platform, build in hs_builds.items() if build["version"] == hs_version
    }
    for platform, build in hs_builds.items():
        print(f"  headless-shell {platform}: {build['version']}")

    output_path = output_dir / "release.json"
    with open(output_path, "w") as f:
        json.dump(release_info, f, indent=2)
    print(f"Generated {output_path}")


def generate_commits_json(output_dir: Path, count: int = 500) -> None:
    """Generate commits.json with recent commits."""
    output = subprocess.check_output(
        ["git", "log", f"--pretty=format:%h%x00%s", f"-{count}", "HEAD"],
        text=True,
    )

    commits = []
    for line in output.strip().split("\n"):
        if "\x00" in line:
            sha, subject = line.split("\x00", 1)
            commits.append({"sha": sha, "subject": subject})

    output_path = output_dir / "commits.json"
    with open(output_path, "w") as f:
        json.dump(commits, f, indent=2)
    print(f"Generated {output_path} with {len(commits)} commits")


def generate_index_html(output_dir: Path) -> None:
    """Generate index.html."""
    html = """<!DOCTYPE html>
<html>
<head><title>Shelley</title></head>
<body>
<p><a href="https://github.com/boldsoftware/shelley">github.com/boldsoftware/shelley</a></p>
<ul>
<li><a href="release.json">release.json</a></li>
<li><a href="commits.json">commits.json</a></li>
</ul>
</body>
</html>
"""
    output_path = output_dir / "index.html"
    with open(output_path, "w") as f:
        f.write(html)
    print(f"Generated {output_path}")


def main() -> None:
    output_dir = Path(sys.argv[1]) if len(sys.argv) > 1 else Path("_site")
    output_dir.mkdir(parents=True, exist_ok=True)

    generate_release_json(output_dir)
    generate_commits_json(output_dir)
    generate_index_html(output_dir)


if __name__ == "__main__":
    main()
