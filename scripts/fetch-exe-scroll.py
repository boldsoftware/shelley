#!/usr/bin/env python3
"""Fetch verified exe-scroll release binaries into Shelley's embed directory."""

import argparse
import hashlib
import json
import os
import platform
import shutil
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

RELEASE_TAG = "exe-scroll/v0.5.943506167"
API_URL = "https://api.github.com/repos/boldsoftware/exe.dev/releases/tags/exe-scroll%2Fv0.5.943506167"
TARGETS = (
    ("linux", "amd64"),
    ("linux", "arm64"),
    ("darwin", "amd64"),
    ("darwin", "arm64"),
)


def native_target() -> tuple[str, str]:
    os_name = platform.system().lower()
    machine = platform.machine().lower()
    arch = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(machine, machine)
    return os_name, arch


def request(url: str) -> urllib.request.Request:
    headers = {
        "Accept": "application/vnd.github+json",
        "User-Agent": "shelley-build",
        "X-GitHub-Api-Version": "2022-11-28",
    }
    token = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN")
    if token:
        headers["Authorization"] = f"Bearer {token}"
    return urllib.request.Request(url, headers=headers)


def release() -> dict:
    metadata = cache_root() / f"{RELEASE_TAG.replace('/', '_')}.json"
    try:
        with urllib.request.urlopen(request(API_URL), timeout=30) as response:
            data = json.load(response)
        metadata.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        fd, tmp_name = tempfile.mkstemp(prefix=metadata.name + ".", dir=metadata.parent)
        try:
            with os.fdopen(fd, "w") as output:
                json.dump(data, output)
            os.replace(tmp_name, metadata)
        finally:
            Path(tmp_name).unlink(missing_ok=True)
    except (OSError, urllib.error.URLError):
        if not metadata.is_file():
            raise
        with metadata.open() as cached:
            data = json.load(cached)
    if data.get("tag_name") != RELEASE_TAG:
        raise RuntimeError(f"GitHub returned {data.get('tag_name')!r}, want {RELEASE_TAG!r}")
    return data


def release_assets(data: dict, wanted: tuple[tuple[str, str], ...]) -> dict[tuple[str, str], dict]:
    by_name = {asset.get("name"): asset for asset in data.get("assets", [])}
    return {
        (os_name, arch): by_name.get(f"exe-scroll-{os_name}-{arch}")
        for os_name, arch in wanted
    }


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def cache_root() -> Path:
    configured = os.environ.get("SHELLEY_EXE_SCROLL_CACHE")
    if configured:
        return Path(configured).expanduser()
    xdg = os.environ.get("XDG_CACHE_HOME")
    if xdg:
        return Path(xdg) / "shelley" / "exe-scroll"
    return Path.home() / ".cache" / "shelley" / "exe-scroll"


def download(tag: str, asset: dict) -> Path:
    digest = asset.get("digest", "")
    if not digest.startswith("sha256:"):
        raise RuntimeError(f"{tag}/{asset.get('name')}: GitHub did not provide a SHA-256 digest")
    want = digest.removeprefix("sha256:")
    cached = cache_root() / tag.replace("/", "_") / f"{asset['name']}-{want}"
    if cached.is_file() and sha256(cached) == want:
        return cached

    cached.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd, tmp_name = tempfile.mkstemp(prefix=asset["name"] + ".", dir=cached.parent)
    tmp = Path(tmp_name)
    try:
        with os.fdopen(fd, "wb") as output, urllib.request.urlopen(request(asset["browser_download_url"]), timeout=120) as response:
            shutil.copyfileobj(response, output)
        got = sha256(tmp)
        if got != want:
            raise RuntimeError(f"{tag}/{asset['name']}: SHA-256 {got}, want {want}")
        tmp.chmod(0o755)
        os.replace(tmp, cached)
    finally:
        tmp.unlink(missing_ok=True)
    return cached


def install(source: Path, output: Path) -> None:
    if output.is_file() and sha256(output) == sha256(source):
        return
    output.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(prefix=output.name + ".", dir=output.parent)
    os.close(fd)
    tmp = Path(tmp_name)
    try:
        shutil.copyfile(source, tmp)
        tmp.chmod(0o755)
        os.replace(tmp, output)
    finally:
        tmp.unlink(missing_ok=True)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    targets = parser.add_mutually_exclusive_group(required=True)
    targets.add_argument("--native", action="store_true", help="fetch the current host binary")
    targets.add_argument("--all", action="store_true", help="fetch every Shelley release target")
    targets.add_argument("--os", choices=("linux", "darwin"), help="target operating system")
    parser.add_argument("--arch", choices=("amd64", "arm64"), help="target architecture (required with --os)")
    parser.add_argument("--output-dir", type=Path, default=Path("exescroll/binary"))
    parser.add_argument("--wait", type=int, default=0, metavar="SECONDS", help="wait for release assets to appear")
    args = parser.parse_args()
    if args.os and not args.arch:
        parser.error("--arch is required with --os")
    return args


def main() -> None:
    args = parse_args()
    if args.native:
        wanted = (native_target(),)
    elif args.all:
        wanted = TARGETS
    else:
        wanted = ((args.os, args.arch),)

    unsupported = [f"{os_name}/{arch}" for os_name, arch in wanted if (os_name, arch) not in TARGETS]
    if unsupported:
        raise RuntimeError("unsupported exe-scroll target: " + ", ".join(unsupported))

    deadline = time.monotonic() + args.wait
    while True:
        found = release_assets(release(), wanted)
        missing = [f"{os_name}/{arch}" for (os_name, arch), asset in found.items() if asset is None]
        if not missing:
            break
        if time.monotonic() >= deadline:
            raise RuntimeError(f"{RELEASE_TAG} has no release asset for " + ", ".join(missing))
        print(f"Waiting for {RELEASE_TAG} release assets: " + ", ".join(missing), file=sys.stderr)
        time.sleep(min(15, max(1, deadline - time.monotonic())))

    for (os_name, arch), asset in found.items():
        assert asset is not None
        cached = download(RELEASE_TAG, asset)
        output = args.output_dir / asset["name"]
        install(cached, output)
        print(f"exe-scroll: {os_name}/{arch} <- {RELEASE_TAG} ({output})", file=sys.stderr)


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, urllib.error.URLError) as error:
        print(f"fetch-exe-scroll: {error}", file=sys.stderr)
        sys.exit(1)
