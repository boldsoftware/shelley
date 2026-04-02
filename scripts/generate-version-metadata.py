#!/usr/bin/env python3
"""Generate version metadata for GitHub Pages.

Generates release.json, commits.json, and index.html.
"""

import json
import subprocess
import sys
from pathlib import Path


def get_headless_shell_release() -> tuple[str, str] | None:
    """Find the latest headless-shell release from GitHub.

    Returns (version_string, tag) like ('Chromium 147.0.7727.24', 'headless-shell/v147.0.7727.24')
    or None if no release exists.
    """
    import urllib.request

    # List releases and find the latest headless-shell/* tag
    url = "https://api.github.com/repos/boldsoftware/shelley/releases?per_page=20"
    req = urllib.request.Request(url, headers={"Accept": "application/vnd.github+json"})
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            releases = json.load(resp)
    except Exception as e:
        print(f"  Warning: could not fetch releases: {e}", file=sys.stderr)
        return None

    for release in releases:
        tag = release.get("tag_name", "")
        if tag.startswith("headless-shell/v"):
            version = tag.removeprefix("headless-shell/v")
            return f"Chromium {version}", tag

    return None


def generate_release_json(output_dir: Path) -> None:
    """Generate release.json with latest release information."""
    # Get latest tag - fail if none exists
    result = subprocess.run(
        ["git", "describe", "--tags", "--abbrev=0"],
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

    # Find latest headless-shell release (separate release tag namespace)
    hs = get_headless_shell_release()
    if hs:
        hs_version, hs_tag = hs
        hs_base = f"https://github.com/boldsoftware/shelley/releases/download/{hs_tag}"
        release_info["headless_shell_version"] = hs_version
        release_info["headless_shell_urls"] = {
            "linux_amd64": f"{hs_base}/headless-shell-linux-amd64.tar.gz",
            "linux_arm64": f"{hs_base}/headless-shell-linux-arm64.tar.gz",
        }
        print(f"  headless-shell: {hs_version} ({hs_tag})")
    else:
        print("  headless-shell: no release found")

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
