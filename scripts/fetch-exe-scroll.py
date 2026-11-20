#!/usr/bin/env python3
"""Fetch verified exe-scroll release binaries into Shelley's embed directory."""

import argparse
import hashlib
import os
import platform
import shutil
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

RELEASE_TAG = "exe-scroll/v0.10.971335601"
# Each asset's SHA-256, as RELEASE_TAG's release page lists it; move them with
# the tag. Pinned so that a build asks nothing of GitHub's API, which refuses
# callers without a token after 60 requests an hour.
DIGESTS = {
    ("linux", "amd64"): "c96c3f024ea667d973daebb5b7a534d627e4455d8bef4a67d146a4cd6b8ed574",
    ("linux", "arm64"): "61ca823d87d720ded9c4ef13e1955b00ef1e1820d7e3c9ad7b61b7d6c10ee930",
    ("darwin", "amd64"): "916274cd572c34fac2c072d6d56c93fb6766cf8e04622b40d8443d05fcf9132a",
    ("darwin", "arm64"): "6d3bbfb6c8ca0890e124c7e7b8cfc55bcb5bf4020b5f97fd8df56cf8a1b25d5c",
}
TARGETS = tuple(DIGESTS)
DOWNLOADS = "https://github.com/boldsoftware/exe.dev/releases/download/" + urllib.parse.quote(RELEASE_TAG, safe="")


def native_target() -> tuple[str, str]:
    os_name = platform.system().lower()
    machine = platform.machine().lower()
    arch = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(machine, machine)
    return os_name, arch


def fetch(url: str, deadline: float):
    """Open url, waiting until deadline while it isn't there yet."""
    while True:
        try:
            return urllib.request.urlopen(urllib.request.Request(url, headers={"User-Agent": "shelley-build"}), timeout=120)
        except urllib.error.HTTPError as error:
            if error.code != 404 or time.monotonic() >= deadline:
                raise RuntimeError(f"{url}: {error}") from error
        print(f"Waiting for {url}", file=sys.stderr)
        time.sleep(min(15, max(1, deadline - time.monotonic())))


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


def download(name: str, want: str, deadline: float) -> Path:
    cached = cache_root() / RELEASE_TAG.replace("/", "_") / f"{name}-{want}"
    if cached.is_file() and sha256(cached) == want:
        return cached

    cached.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd, tmp_name = tempfile.mkstemp(prefix=name + ".", dir=cached.parent)
    tmp = Path(tmp_name)
    try:
        with os.fdopen(fd, "wb") as output, fetch(f"{DOWNLOADS}/{name}", deadline) as response:
            shutil.copyfileobj(response, output)
        got = sha256(tmp)
        if got != want:
            raise RuntimeError(f"{RELEASE_TAG}/{name}: SHA-256 {got}, want {want}")
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
    for os_name, arch in wanted:
        name = f"exe-scroll-{os_name}-{arch}"
        output = args.output_dir / name
        install(download(name, DIGESTS[(os_name, arch)], deadline), output)
        print(f"exe-scroll: {os_name}/{arch} <- {RELEASE_TAG} ({output})", file=sys.stderr)


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, urllib.error.URLError) as error:
        print(f"fetch-exe-scroll: {error}", file=sys.stderr)
        sys.exit(1)
