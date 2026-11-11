#!/usr/bin/env node
// Copy the ESP-IDF build outputs into flasher/public/firmware and describe them in manifest.json.
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync, copyFileSync } from "node:fs";
import { basename, dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, "../..");

export const EXPECTED_CHIP = "esp32s3";
// Matches the factory partition in firmware/partitions.csv.
export const APP_PARTITION_SIZE = 6 * 1024 * 1024;
const PART_NAMES = { bootloader: "bootloader", "partition-table": "partition-table", app: "app" };

export class StageError extends Error {}

function readJson(path, hint) {
  if (!existsSync(path)) throw new StageError(`找不到 ${path}${hint ? "，" + hint : ""}`);
  return JSON.parse(readFileSync(path, "utf8"));
}

/** Build the manifest and the list of files to copy, without touching the output directory. */
export function collectFirmware({ buildDir, versionFile, now = new Date() }) {
  const hint = "请先在 firmware/ 目录运行 idf.py build";
  const args = readJson(join(buildDir, "flasher_args.json"), hint);
  const desc = existsSync(join(buildDir, "project_description.json"))
    ? JSON.parse(readFileSync(join(buildDir, "project_description.json"), "utf8"))
    : {};
  const chip = args.extra_esptool_args?.chip;
  if (chip !== EXPECTED_CHIP) throw new StageError(`固件目标芯片为 ${chip}，期望 ${EXPECTED_CHIP}`);
  const settings = args.flash_settings ?? {};
  if (!settings.flash_mode || !settings.flash_size || !settings.flash_freq) {
    throw new StageError("flasher_args.json 缺少 flash_settings");
  }
  if (!existsSync(versionFile)) throw new StageError(`找不到版本文件 ${versionFile}`);
  const version = readFileSync(versionFile, "utf8").trim();

  // Name each flash file by matching it to the bootloader/partition-table/app entries.
  const named = new Map();
  for (const key of Object.keys(PART_NAMES)) {
    if (args[key]?.file) named.set(args[key].file, PART_NAMES[key]);
  }
  const entries = Object.entries(args.flash_files ?? {});
  if (entries.length === 0) throw new StageError("flasher_args.json 没有 flash_files");

  const parts = entries
    .map(([offsetText, file]) => {
      const src = join(buildDir, file);
      if (!existsSync(src)) throw new StageError(`找不到 ${src}，${hint}`);
      const data = readFileSync(src);
      const name = named.get(file) ?? basename(file, ".bin");
      return {
        name,
        offset: Number.parseInt(offsetText, 16),
        path: basename(file),
        size: data.length,
        sha256: createHash("sha256").update(data).digest("hex"),
        src,
      };
    })
    .sort((a, b) => a.offset - b.offset);

  const app = parts.find((p) => p.name === "app");
  if (!app) throw new StageError("flasher_args.json 中没有 app 镜像");
  if (app.size > APP_PARTITION_SIZE) {
    throw new StageError(`app 大小 ${app.size} 超出 factory 分区 ${APP_PARTITION_SIZE}`);
  }
  if (new Set(parts.map((p) => p.path)).size !== parts.length) throw new StageError("固件文件名重复");

  const manifest = {
    schema: 1,
    name: "Aemeath",
    version,
    chip,
    flash: { mode: settings.flash_mode, size: settings.flash_size, freq: settings.flash_freq },
    builtAt: now.toISOString(),
    idfVersion: desc.git_revision ?? null,
    projectVersion: desc.project_version ?? null,
    parts: parts.map(({ src, ...p }) => p),
  };
  return { manifest, files: parts.map((p) => ({ src: p.src, name: p.path })) };
}

export function stageFirmware({ buildDir, versionFile, outDir }) {
  const { manifest, files } = collectFirmware({ buildDir, versionFile });
  rmSync(outDir, { recursive: true, force: true });
  mkdirSync(outDir, { recursive: true });
  for (const f of files) copyFileSync(f.src, join(outDir, f.name));
  writeFileSync(join(outDir, "manifest.json"), JSON.stringify(manifest, null, 2) + "\n");
  return manifest;
}

function parseArgs(argv) {
  const opts = {
    buildDir: join(repoRoot, "firmware/build"),
    versionFile: join(repoRoot, "VERSION"),
    outDir: join(repoRoot, "flasher/public/firmware"),
  };
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    if (flag === "--") continue; // forwarded verbatim by some package managers
    i++;
    const value = argv[i];
    if (!value) throw new StageError(`${flag} 需要参数`);
    if (flag === "--build-dir") opts.buildDir = resolve(value);
    else if (flag === "--version-file") opts.versionFile = resolve(value);
    else if (flag === "--out-dir") opts.outDir = resolve(value);
    else throw new StageError(`未知参数 ${flag}`);
  }
  return opts;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const opts = parseArgs(process.argv.slice(2));
    const m = stageFirmware(opts);
    console.log(`已打包 ${m.name} ${m.version} (${m.parts.map((p) => `${p.name}@0x${p.offset.toString(16)}`).join(", ")}) -> ${opts.outDir}`);
  } catch (err) {
    console.error(`stage-firmware: ${err instanceof Error ? err.message : err}`);
    process.exit(1);
  }
}
