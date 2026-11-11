#!/usr/bin/env python3
"""Validate the root version and package the exact ESP-IDF flash artifacts."""

import argparse
import json
from pathlib import Path, PurePosixPath
import re
import shlex
import zipfile

# SemVer 2.0: numeric identifiers cannot have leading zeroes in core/prerelease.
NUMBER = r"(?:0|[1-9][0-9]*)"
PRERELEASE = rf"(?:{NUMBER}|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
SEMVER = re.compile(
    rf"{NUMBER}\.{NUMBER}\.{NUMBER}"
    rf"(?:-{PRERELEASE}(?:\.{PRERELEASE})*)?"
    r"(?:[+][0-9A-Za-z-]+(?:[.][0-9A-Za-z-]+)*)?"
)


def read_version(path: Path) -> str:
    version = path.read_text(encoding="utf-8").strip()
    if not SEMVER.fullmatch(version):
        raise ValueError(f"{path}: expected a semantic version such as 0.2.0")
    # ESP-IDF's app descriptor has a 32-byte version field, including the NUL.
    if len(version.encode("ascii")) > 31:
        raise ValueError("VERSION exceeds the ESP-IDF app descriptor limit of 31 bytes")
    return version


def package(build_dir: Path, version: str, output: Path) -> None:
    build_dir = build_dir.resolve()
    description = json.loads((build_dir / "project_description.json").read_text())
    if description["project_version"] != version or description["target"] != "esp32s3":
        raise ValueError("build metadata does not match VERSION / esp32s3")
    metadata = json.loads((build_dir / "flasher_args.json").read_text())
    if metadata["extra_esptool_args"]["chip"] != "esp32s3":
        raise ValueError("flash metadata does not target esp32s3")
    files = metadata["flash_files"]
    for role in ("bootloader", "partition-table", "app"):
        item = metadata[role]
        if files.get(item["offset"]) != item["file"]:
            raise ValueError(f"missing or inconsistent {role} flash artifact")
    flash_args = (build_dir / "flash_args").read_text(encoding="utf-8")
    tokens = shlex.split(flash_args)
    options = metadata["write_flash_args"]
    if len(tokens) % 2 or len(options) % 2:
        raise ValueError("invalid generated flash arguments")
    expected = dict(zip(options[::2], options[1::2])) | files
    if dict(zip(tokens[::2], tokens[1::2])) != expected:
        raise ValueError("flash_args and flasher_args.json disagree")

    artifacts = {}
    for name in files.values():
        relative = PurePosixPath(name)
        source = (build_dir / name).resolve()
        if relative.is_absolute() or ".." in relative.parts or not source.is_relative_to(build_dir):
            raise ValueError(f"flash artifact must stay inside build directory: {name}")
        if not source.is_file() or source.stat().st_size == 0:
            raise ValueError(f"missing or empty flash artifact: {name}")
        artifacts[name] = source

    settings = metadata["flash_settings"]
    stem = f"Aemeath-Firmware-{version}-esp32s3"
    readme = f"""Aemeath firmware {version} — ESP32-S3 (N16R8)

Flash settings from ESP-IDF: {settings['flash_mode']}, {settings['flash_freq']}, {settings['flash_size']}.
The board defaults select QIO; use the generated flash mode above for writing.
Offsets and files:
{chr(10).join(f'  {offset}  {name}' for offset, name in files.items())}

1. Install Python and esptool v5: python -m pip install 'esptool>=5,<6'
2. Unzip this archive, then open a terminal in the extracted directory.
3. Connect the ESP32-S3 using a USB data cable. Replace PORT below with
   its serial port (e.g. /dev/ttyACM0, /dev/cu.usbmodem..., or COM3).
4. Flash the separate images using the included argument file:

   python -m esptool --chip esp32s3 --port PORT --baud 460800 write-flash @flash_args

   If the board does not enter download mode automatically, hold BOOT,
   press/release RESET, release BOOT, and run the command again.
   Press RESET after flashing if needed. Try --baud 115200 on an unstable link.

Alternatively, download the companion merged image {stem}.bin, place it in
this directory, and flash it at address 0x0:

   python -m esptool --chip esp32s3 --port PORT --baud 460800 write-flash {shlex.join(options)} 0x0 {stem}.bin

The merged image includes padding between partitions and overwrites saved
settings in those gaps (including NVS / Wi-Fi configuration). Back up device
settings first if needed. The separate-image command writes only the listed
images. A full erase is not required. Neither command is an OTA update.
"""
    # Validate every input before creating/replacing the output archive.
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED) as archive:
        for name, source in artifacts.items():
            archive.write(source, name)
        archive.write(build_dir / "flash_args", "flash_args")
        archive.write(build_dir / "flasher_args.json", "flasher_args.json")
        archive.writestr("VERSION", version + "\n")
        archive.writestr("README.txt", readme)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    version_parser = commands.add_parser("version", help="validate and print VERSION")
    version_parser.add_argument("version_file", type=Path)
    pack = commands.add_parser("package", help="package a completed ESP-IDF build")
    pack.add_argument("--build-dir", type=Path, required=True)
    pack.add_argument("--version-file", type=Path, required=True)
    pack.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        version = read_version(args.version_file)
        if args.command == "version":
            print(version)
        else:
            package(args.build_dir, version, args.output)
    except (OSError, ValueError, KeyError) as error:
        parser.exit(1, f"error: {error}\n")


if __name__ == "__main__":
    main()
