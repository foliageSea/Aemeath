export const EXPECTED_CHIP = "esp32s3";

export interface ManifestPart {
  name: string;
  offset: number;
  path: string;
  size: number;
  sha256: string;
}

export interface Manifest {
  schema: 1;
  name: string;
  version: string;
  chip: string;
  flash: { mode: string; size: string; freq: string };
  builtAt: string;
  idfVersion: string | null;
  projectVersion: string | null;
  parts: ManifestPart[];
}

export interface FlashImage {
  name: string;
  address: number;
  data: Uint8Array;
}

export class ManifestError extends Error {}

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
const isStr = (v: unknown): v is string => typeof v === "string" && v.length > 0;
const isUint = (v: unknown): v is number => Number.isSafeInteger(v) && (v as number) >= 0;

/** Validate untrusted manifest JSON and return it typed. */
export function parseManifest(raw: unknown): Manifest {
  if (!isObj(raw)) throw new ManifestError("manifest 格式错误");
  if (raw.schema !== 1) throw new ManifestError(`不支持的 manifest 版本 ${String(raw.schema)}`);
  if (!isStr(raw.name) || !isStr(raw.version) || !isStr(raw.builtAt)) throw new ManifestError("manifest 缺少名称、版本或构建时间");
  if (raw.chip !== EXPECTED_CHIP) throw new ManifestError(`固件芯片为 ${String(raw.chip)}，期望 ${EXPECTED_CHIP}`);
  const flash = raw.flash;
  if (!isObj(flash) || !isStr(flash.mode) || !isStr(flash.size) || !isStr(flash.freq)) throw new ManifestError("manifest 缺少 flash 参数");
  if (!Array.isArray(raw.parts) || raw.parts.length === 0) throw new ManifestError("manifest 没有固件分区");
  const parts = raw.parts.map((p, i): ManifestPart => {
    if (!isObj(p) || !isStr(p.name) || !isStr(p.path) || !isUint(p.offset) || !isUint(p.size) || p.size === 0)
      throw new ManifestError(`第 ${i + 1} 个分区字段无效`);
    if (!isStr(p.sha256) || !/^[0-9a-f]{64}$/.test(p.sha256)) throw new ManifestError(`${p.name} 的 sha256 无效`);
    // Paths are resolved relative to the manifest; reject anything that escapes it.
    if (!/^[\w.-]+$/.test(p.path) || p.path.startsWith(".")) throw new ManifestError(`${p.name} 的路径无效`);
    return { name: p.name, offset: p.offset, path: p.path, size: p.size, sha256: p.sha256 };
  });
  return {
    schema: 1,
    name: raw.name,
    version: raw.version,
    chip: raw.chip,
    flash: { mode: flash.mode, size: flash.size, freq: flash.freq },
    builtAt: raw.builtAt,
    idfVersion: isStr(raw.idfVersion) ? raw.idfVersion : null,
    projectVersion: isStr(raw.projectVersion) ? raw.projectVersion : null,
    parts,
  };
}

export async function sha256Hex(data: Uint8Array): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", data as Uint8Array<ArrayBuffer>);
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, "0")).join("");
}

/** Check that downloaded bytes match the manifest entry; throws on mismatch. */
export async function verifyPart(part: ManifestPart, data: Uint8Array): Promise<void> {
  if (data.length !== part.size) throw new ManifestError(`${part.path} 大小不符（${data.length} ≠ ${part.size}）`);
  const hash = await sha256Hex(data);
  if (hash !== part.sha256) throw new ManifestError(`${part.path} 校验失败，文件可能已损坏，请刷新页面重试`);
}

/** Returns null when the site was built without firmware (manifest missing). */
export async function fetchManifest(url: URL, fetchFn: typeof fetch = fetch): Promise<Manifest | null> {
  const res = await fetchFn(url, { cache: "no-cache" });
  if (res.status === 404) return null;
  if (!res.ok) throw new ManifestError(`获取 manifest 失败：HTTP ${res.status}`);
  return parseManifest(await res.json());
}

export async function downloadParts(
  manifest: Manifest,
  manifestUrl: URL,
  onProgress: (loaded: number, total: number) => void,
  fetchFn: typeof fetch = fetch,
): Promise<FlashImage[]> {
  const total = manifest.parts.reduce((n, p) => n + p.size, 0);
  let done = 0;
  const images: FlashImage[] = [];
  for (const part of manifest.parts) {
    // The query string busts caches between firmware versions with identical file names.
    const url = new URL(`${part.path}?v=${part.sha256.slice(0, 12)}`, manifestUrl);
    const res = await fetchFn(url);
    if (!res.ok) throw new ManifestError(`下载 ${part.path} 失败：HTTP ${res.status}`);
    const data = new Uint8Array(await res.arrayBuffer());
    await verifyPart(part, data);
    done += part.size;
    onProgress(done, total);
    images.push({ name: part.name, address: part.offset, data });
  }
  return images;
}
