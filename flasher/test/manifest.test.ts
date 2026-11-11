import { createHash } from "node:crypto";
import { describe, expect, it } from "vitest";
import { ManifestError, downloadParts, fetchManifest, parseManifest, verifyPart } from "../src/manifest";

const bytes = (n: number, fill = 7) => new Uint8Array(n).fill(fill);
const sha = (d: Uint8Array) => createHash("sha256").update(d).digest("hex");

const boot = bytes(32, 1);
const app = bytes(64, 2);
const valid = {
  schema: 1,
  name: "Aemeath",
  version: "0.2.0",
  chip: "esp32s3",
  flash: { mode: "dio", size: "16MB", freq: "80m" },
  builtAt: "2026-11-11T00:00:00.000Z",
  idfVersion: "v6.0.2",
  projectVersion: null,
  parts: [
    { name: "bootloader", offset: 0, path: "bootloader.bin", size: boot.length, sha256: sha(boot) },
    { name: "app", offset: 0x10000, path: "aemeath.bin", size: app.length, sha256: sha(app) },
  ],
};
const clone = () => structuredClone(valid) as any;

describe("parseManifest", () => {
  it("accepts a valid manifest", () => {
    const m = parseManifest(valid);
    expect(m.parts).toHaveLength(2);
    expect(m.projectVersion).toBeNull();
  });
  it("rejects wrong chip, schema and missing fields", () => {
    const wrongChip = clone(); wrongChip.chip = "esp32";
    expect(() => parseManifest(wrongChip)).toThrow(/esp32s3/);
    const wrongSchema = clone(); wrongSchema.schema = 2;
    expect(() => parseManifest(wrongSchema)).toThrow(ManifestError);
    const noParts = clone(); noParts.parts = [];
    expect(() => parseManifest(noParts)).toThrow(ManifestError);
    const noFlash = clone(); delete noFlash.flash;
    expect(() => parseManifest(noFlash)).toThrow(ManifestError);
    expect(() => parseManifest(null)).toThrow(ManifestError);
  });
  it("rejects bad hashes and path traversal", () => {
    const badHash = clone(); badHash.parts[0].sha256 = "abc";
    expect(() => parseManifest(badHash)).toThrow(/sha256/);
    for (const path of ["../x.bin", "a/b.bin", ".hidden"]) {
      const m = clone(); m.parts[0].path = path;
      expect(() => parseManifest(m)).toThrow(/路径/);
    }
  });
});

describe("verifyPart", () => {
  const part = valid.parts[0];
  it("passes for matching data", async () => {
    await expect(verifyPart(part, boot)).resolves.toBeUndefined();
  });
  it("fails for size or hash mismatch", async () => {
    await expect(verifyPart(part, bytes(31, 1))).rejects.toThrow(/大小/);
    await expect(verifyPart(part, bytes(32, 9))).rejects.toThrow(/校验失败/);
  });
});

describe("fetch helpers", () => {
  const base = new URL("https://example.test/flasher/firmware/manifest.json");
  const files: Record<string, Uint8Array> = { "bootloader.bin": boot, "aemeath.bin": app };
  const fakeFetch = (async (input: URL | RequestInfo) => {
    const url = new URL(String(input));
    if (url.pathname.endsWith("manifest.json")) return new Response(JSON.stringify(valid));
    const name = url.pathname.split("/").pop()!;
    const data = files[name];
    return data ? new Response(data as Uint8Array<ArrayBuffer>) : new Response(null, { status: 404 });
  }) as typeof fetch;

  it("returns null when the manifest is absent", async () => {
    const notFound = (async () => new Response(null, { status: 404 })) as typeof fetch;
    expect(await fetchManifest(base, notFound)).toBeNull();
  });
  it("downloads and verifies every part relative to the manifest", async () => {
    const m = (await fetchManifest(base, fakeFetch))!;
    const progress: number[] = [];
    const images = await downloadParts(m, base, (loaded) => progress.push(loaded), fakeFetch);
    expect(images.map((i) => [i.name, i.address, i.data.length])).toEqual([
      ["bootloader", 0, 32],
      ["app", 0x10000, 64],
    ]);
    expect(progress).toEqual([32, 96]);
  });
  it("refuses corrupted downloads", async () => {
    files["aemeath.bin"] = bytes(64, 3);
    const m = (await fetchManifest(base, fakeFetch))!;
    await expect(downloadParts(m, base, () => {}, fakeFetch)).rejects.toThrow(/校验失败/);
    files["aemeath.bin"] = app;
  });
});
