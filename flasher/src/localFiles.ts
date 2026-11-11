export const FLASH_SECTOR = 0x1000;
export const FLASH_SIZE_16MB = 16 * 1024 * 1024;

export interface ImageRange {
  name: string;
  offset: number;
  size: number;
}

/** Parse "0x10000" or "65536"; returns null for anything else. */
export function parseOffset(text: string): number | null {
  const t = text.trim().toLowerCase();
  let n: number;
  if (/^0x[0-9a-f]+$/.test(t)) n = Number.parseInt(t.slice(2), 16);
  else if (/^\d+$/.test(t)) n = Number.parseInt(t, 10);
  else return null;
  return Number.isSafeInteger(n) ? n : null;
}

export const hex = (n: number) => "0x" + n.toString(16).toUpperCase().padStart(5, "0");

/** Return human-readable problems; an empty array means the images can be flashed. */
export function validateImages(images: ImageRange[], flashSize = FLASH_SIZE_16MB): string[] {
  const errors: string[] = [];
  if (images.length === 0) return ["请至少添加一个固件文件"];
  for (const img of images) {
    if (img.size === 0) errors.push(`${img.name} 是空文件`);
    if (img.offset % FLASH_SECTOR !== 0) errors.push(`${img.name} 的偏移 ${hex(img.offset)} 未按 4KB 对齐`);
    if (img.offset + img.size > flashSize)
      errors.push(`${img.name} 结束地址 ${hex(img.offset + img.size)} 超出 Flash 容量 ${hex(flashSize)}`);
  }
  const sorted = [...images].sort((a, b) => a.offset - b.offset);
  for (let i = 1; i < sorted.length; i++) {
    const prev = sorted[i - 1];
    if (prev.offset + prev.size > sorted[i].offset) errors.push(`${prev.name} 与 ${sorted[i].name} 地址区间重叠`);
  }
  return errors;
}

/** Guess an offset from common ESP-IDF file names. */
export function guessOffset(fileName: string): number {
  const n = fileName.toLowerCase();
  if (n.includes("bootloader")) return 0x0;
  if (n.includes("partition")) return 0x8000;
  if (n.includes("merged") || n.includes("factory") || n.includes("firmware-")) return 0x0;
  return 0x10000;
}
