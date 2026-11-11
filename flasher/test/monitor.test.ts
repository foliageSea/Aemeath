import { describe, expect, it } from "vitest";
import { LogBuffer } from "../src/monitor";

describe("LogBuffer", () => {
  it("joins partial chunks, normalises CRLF and strips ANSI", () => {
    const b = new LogBuffer();
    b.push("\x1b[0;32mI (12) boot: hel");
    b.push("lo\x1b[0m\r\nnext");
    expect(b.text()).toBe("I (12) boot: hello\nnext");
  });
  it("caps the number of lines", () => {
    const b = new LogBuffer(3);
    b.push("1\n2\n3\n4\n5\n");
    expect(b.text()).toBe("3\n4\n5");
  });
});
