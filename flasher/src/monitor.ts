/** Plain serial console reader for the running firmware's log output. */
export class SerialMonitor {
  private reader: ReadableStreamDefaultReader<Uint8Array> | null = null;
  private stopped = false;
  private loop: Promise<void> | null = null;

  constructor(
    readonly port: SerialPort,
    private onText: (text: string) => void,
    private onClosed: (err?: unknown) => void,
  ) {}

  async start(baudRate = 115200): Promise<void> {
    await this.port.open({ baudRate });
    // Release EN/IO0 so the chip runs normally (some adapters assert them on open).
    await this.port.setSignals({ dataTerminalReady: false, requestToSend: false }).catch(() => {});
    this.loop = this.readLoop();
  }

  private async readLoop(): Promise<void> {
    const decoder = new TextDecoder();
    let error: unknown;
    try {
      while (!this.stopped && this.port.readable) {
        this.reader = this.port.readable.getReader();
        try {
          for (;;) {
            const { value, done } = await this.reader.read();
            if (done) break;
            if (value) this.onText(decoder.decode(value, { stream: true }));
          }
        } finally {
          this.reader.releaseLock();
          this.reader = null;
        }
      }
    } catch (err) {
      if (!this.stopped) error = err;
    }
    await this.port.close().catch(() => {});
    this.onClosed(error);
  }

  /** Pulse EN via RTS (classic auto-reset wiring; also honoured by USB-Serial/JTAG). */
  async resetDevice(): Promise<void> {
    await this.port.setSignals({ dataTerminalReady: false, requestToSend: true });
    await new Promise((r) => setTimeout(r, 120));
    await this.port.setSignals({ dataTerminalReady: false, requestToSend: false });
  }

  async stop(): Promise<void> {
    this.stopped = true;
    await this.reader?.cancel().catch(() => {});
    await this.loop;
  }
}

// eslint-disable-next-line no-control-regex
const ANSI = /\x1b\[[0-9;]*[A-Za-z]/g;

/** Bounded line buffer that strips ANSI colour codes and keeps partial lines. */
export class LogBuffer {
  private lines: string[] = [];
  private partial = "";

  constructor(private maxLines = 5000) {}

  push(text: string): void {
    const parts = (this.partial + text.replace(ANSI, "").replace(/\r\n?/g, "\n")).split("\n");
    this.partial = parts.pop() ?? "";
    this.lines.push(...parts);
    if (this.lines.length > this.maxLines) this.lines.splice(0, this.lines.length - this.maxLines);
  }

  clear(): void {
    this.lines = [];
    this.partial = "";
  }

  text(): string {
    return this.lines.join("\n") + (this.partial ? (this.lines.length ? "\n" : "") + this.partial : "");
  }
}
