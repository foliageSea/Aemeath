import { describe, expect, it } from "vitest";
import { guessOffset, parseOffset, validateImages } from "../src/localFiles";

describe("parseOffset", () => {
  it("parses hex and decimal", () => {
    expect(parseOffset("0x10000")).toBe(0x10000);
    expect(parseOffset(" 0X8000 ")).toBe(0x8000);
    expect(parseOffset("4096")).toBe(4096);
  });
  it("rejects garbage", () => {
    for (const t of ["", "0x", "10k", "-1", "0x1g", "1.5"]) expect(parseOffset(t)).toBeNull();
  });
});

describe("validateImages", () => {
  it("accepts the standard ESP-IDF layout", () => {
    expect(
      validateImages([
        { name: "bootloader.bin", offset: 0, size: 22496 },
        { name: "partition-table.bin", offset: 0x8000, size: 3072 },
        { name: "aemeath.bin", offset: 0x10000, size: 5757968 },
      ]),
    ).toEqual([]);
  });
  it("requires at least one image", () => {
    expect(validateImages([])).toHaveLength(1);
  });
  it("flags misalignment, overlap, overflow and empty files", () => {
    expect(validateImages([{ name: "a", offset: 0x10010, size: 10 }])[0]).toMatch(/4KB/);
    expect(validateImages([{ name: "a", offset: 0, size: 0x9000 }, { name: "b", offset: 0x8000, size: 10 }])[0]).toMatch(/重叠/);
    expect(validateImages([{ name: "a", offset: 0xff0000, size: 0x20000 }])[0]).toMatch(/超出/);
    expect(validateImages([{ name: "a", offset: 0, size: 0 }])[0]).toMatch(/空文件/);
  });
});

describe("guessOffset", () => {
  it("maps ESP-IDF file names", () => {
    expect(guessOffset("bootloader.bin")).toBe(0);
    expect(guessOffset("partition-table.bin")).toBe(0x8000);
    expect(guessOffset("aemeath.bin")).toBe(0x10000);
    expect(guessOffset("Aemeath-Firmware-0.2.0-esp32s3.bin")).toBe(0);
  });
});
