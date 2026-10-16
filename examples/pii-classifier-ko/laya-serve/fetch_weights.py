"""Download the files listed in SHA256SUMS from a Hugging Face model repository
at one pinned commit, verifying each digest. Standard library only.

usage: fetch_weights.py <org/repo> <40-char commit> <SHA256SUMS> <out dir>
"""

import hashlib
import os
import re
import sys
import urllib.request


def main() -> None:
    repo, revision, sums, out = sys.argv[1:5]
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*", repo):
        sys.exit("HF_REPO must be <org>/<name>")
    if not re.fullmatch(r"[0-9a-f]{40}", revision):
        sys.exit("HF_REVISION must be a full 40-character commit")
    with open(sums, encoding="utf-8") as f:
        entries = [line.split(None, 1) for line in f if line.strip()]
    for digest, name in entries:
        if not re.fullmatch(r"[0-9a-f]{64}", digest):
            sys.exit(f"bad sha256 digest in SHA256SUMS: {digest}")
        if name.strip().lstrip("*").startswith("/") or ".." in name.strip().lstrip("*").split("/"):
            sys.exit(f"unsafe path in SHA256SUMS: {name.strip()}")
    for digest, name in entries:
        name = name.strip().lstrip("*")
        dest = os.path.join(out, name)
        os.makedirs(os.path.dirname(dest), exist_ok=True)
        h = hashlib.sha256()
        url = f"https://huggingface.co/{repo}/resolve/{revision}/{name}"
        with urllib.request.urlopen(url, timeout=600) as resp, open(dest, "wb") as f:
            while chunk := resp.read(1 << 20):
                h.update(chunk)
                f.write(chunk)
        if h.hexdigest() != digest:
            sys.exit(f"sha256 mismatch for {name}")
        print("verified", name, flush=True)


if __name__ == "__main__":
    main()
