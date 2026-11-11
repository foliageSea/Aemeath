import { createHash, randomBytes } from "node:crypto";
import { describe, expect, it } from "vitest";
import { md5Hex } from "../src/md5";

const ref = (d: Uint8Array) => createHash("md5").update(d).digest("hex");

describe("md5Hex", () => {
  it("matches RFC 1321 vectors", () => {
    expect(md5Hex(new Uint8Array())).toBe("d41d8cd98f00b204e9800998ecf8427e");
    expect(md5Hex(new TextEncoder().encode("abc"))).toBe("900150983cd24fb0d6963f7d28e17f72");
  });
  it("matches node crypto around block boundaries and for large input", () => {
    for (const n of [1, 55, 56, 57, 63, 64, 65, 127, 128, 1000]) {
      const d = new Uint8Array(randomBytes(n));
      expect(md5Hex(d)).toBe(ref(d));
    }
    const big = new Uint8Array(randomBytes(3 * 1024 * 1024 + 17));
    expect(md5Hex(big)).toBe(ref(big));
  });
});
