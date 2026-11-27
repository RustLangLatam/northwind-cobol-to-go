# Checks the unpacked deliverable under generated/ against the signed manifest.
# For every manifest entry: the file exists, its size and sha256 match.
# Files under generated/ that the manifest does not list are reported as extra.
# Usage: python3 -I evidence/check_manifest.py   (run from any directory)
import hashlib
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
GEN = os.path.join(ROOT, "generated")
MANIFEST = os.path.join(HERE, "signature", "manifest.json")


def sha256_and_size(path):
    h = hashlib.sha256()
    size = 0
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
            size += len(chunk)
    return h.hexdigest(), size


def tree_files():
    found = set()
    for dirpath, _dirs, files in os.walk(GEN):
        for name in files:
            full = os.path.join(dirpath, name)
            found.add(os.path.relpath(full, GEN).replace(os.sep, "/"))
    return found


def main():
    with open(MANIFEST, encoding="utf-8") as f:
        entries = json.load(f)["files"]
    problems = []
    listed = set()
    for e in entries:
        rel = e["path"]
        listed.add(rel)
        full = os.path.join(GEN, *rel.split("/"))
        if not os.path.isfile(full) or os.path.islink(full):
            problems.append(f"missing: {rel}")
            continue
        digest, size = sha256_and_size(full)
        if size != e["size"]:
            problems.append(f"size {size} != {e['size']}: {rel}")
        if digest != e["sha256"]:
            problems.append(f"sha256 differs: {rel}")
    for rel in sorted(tree_files() - listed):
        problems.append(f"extra (not in manifest): {rel}")
    if problems:
        print("FAILED")
        for p in problems:
            print(" ", p)
        return 1
    print(f"OK: {len(entries)} files under generated/ match manifest.json (sha256, size); no extra files")
    return 0


if __name__ == "__main__":
    sys.exit(main())
