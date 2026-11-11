#!/usr/bin/env python3
"""Synchronize the release version with MyGO; firmware reads VERSION via CMake."""
import argparse
import json
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parent.parent
PATTERN = r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version", nargs="?", help="Set a new X.Y.Z release version")
    parser.add_argument("--check", action="store_true", help="Check without modifying files")
    parser.add_argument("--tag", help="Also require a release tag matching VERSION (vX.Y.Z or X.Y.Z)")
    args = parser.parse_args()
    if args.version and args.check:
        parser.error("--check cannot be combined with a new version")
    version_file = ROOT / "VERSION"
    version = args.version or version_file.read_text().strip()
    if not re.fullmatch(PATTERN, version):
        parser.error("version must be X.Y.Z with no leading zeros")
    if args.tag is not None and args.tag not in (version, "v" + version):
        parser.error(f"release tag {args.tag!r} does not match VERSION {version}")
    config_file = ROOT / "agent/mygo.json"
    config = json.loads(config_file.read_text())
    if args.check:
        if config["version"] != version:
            parser.error(f"MyGO version {config['version']} != VERSION {version}; run python3 scripts/version.py")
    else:
        if args.version:
            version_file.write_text(version + "\n")
        if config["version"] != version:
            # Preserve the rest of the hand-formatted MyGO configuration.
            config_file.write_text(re.sub(r'("version"\s*:\s*)"[^"]*"',
                                          lambda m: m[1] + json.dumps(version),
                                          config_file.read_text(), count=1))
    print(version)


if __name__ == "__main__":
    main()
