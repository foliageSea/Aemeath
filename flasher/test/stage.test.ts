import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
// @ts-expect-error plain ESM script without type declarations
import { collectFirmware, stageFirmware } from "../scripts/stage-firmware.mjs";

function fakeBuild(overrides: Record<string, unknown> = {}) {
  const dir = mkdtempSync(join(tmpdir(), "aemeath-stage-"));
  const build = join(dir, "build");
  mkdirSync(join(build, "bootloader"), { recursive: true });
  mkdirSync(join(build, "partition_table"), { recursive: true });
  writeFileSync(join(build, "bootloader/bootloader.bin"), Buffer.alloc(100, 1));
  writeFileSync(join(build, "partition_table/partition-table.bin"), Buffer.alloc(30, 2));
  writeFileSync(join(build, "aemeath.bin"), Buffer.alloc(500, 3));
  const args = {
    flash_settings: { flash_mode: "dio", flash_size: "16MB", flash_freq: "80m" },
    flash_files: { "0x0": "bootloader/bootloader.bin", "0x8000": "partition_table/partition-table.bin", "0x10000": "aemeath.bin" },
    bootloader: { offset: "0x0", file: "bootloader/bootloader.bin" },
    "partition-table": { offset: "0x8000", file: "partition_table/partition-table.bin" },
    app: { offset: "0x10000", file: "aemeath.bin" },
    extra_esptool_args: { chip: "esp32s3" },
    ...overrides,
  };
  writeFileSync(join(build, "flasher_args.json"), JSON.stringify(args));
  writeFileSync(join(build, "project_description.json"), JSON.stringify({ git_revision: "v6.0.2", project_version: "abc123" }));
  writeFileSync(join(dir, "VERSION"), "1.2.3\n");
  return { dir, build, versionFile: join(dir, "VERSION") };
}

describe("stage-firmware", () => {
  it("writes manifest and copies files", () => {
    const { dir, build, versionFile } = fakeBuild();
    const outDir = join(dir, "out");
    const m = stageFirmware({ buildDir: build, versionFile, outDir });
    expect(m.version).toBe("1.2.3");
    expect(m.parts.map((p: any) => [p.name, p.offset, p.path, p.size])).toEqual([
      ["bootloader", 0, "bootloader.bin", 100],
      ["partition-table", 0x8000, "partition-table.bin", 30],
      ["app", 0x10000, "aemeath.bin", 500],
    ]);
    expect(JSON.parse(readFileSync(join(outDir, "manifest.json"), "utf8")).idfVersion).toBe("v6.0.2");
    expect(existsSync(join(outDir, "aemeath.bin"))).toBe(true);
  });
  it("rejects a missing build, wrong chip or missing file", () => {
    const { dir, build, versionFile } = fakeBuild({ extra_esptool_args: { chip: "esp32" } });
    expect(() => collectFirmware({ buildDir: build, versionFile })).toThrow(/esp32s3/);
    expect(() => collectFirmware({ buildDir: join(dir, "nope"), versionFile })).toThrow(/idf.py build/);
    const other = fakeBuild({ flash_files: { "0x10000": "missing.bin" }, app: { file: "missing.bin" } });
    expect(() => collectFirmware({ buildDir: other.build, versionFile: other.versionFile })).toThrow(/找不到/);
  });
});
