import { ESPLoader, Transport, type IEspLoaderTerminal } from "esptool-js";
import type { FlashImage } from "./manifest";
import { md5Hex } from "./md5";

export const ROM_BAUD = 115200;

export interface ChipInfo {
  chipName: string;
  description: string;
  mac: string;
  flashSize: string | null;
}

export interface FlashProgress {
  imageIndex: number;
  imageName: string;
  /** Overall progress in [0, 1], weighted by image size. */
  fraction: number;
}

/** One connection to the ROM/stub loader; create a new session for every connect. */
export class FlashSession {
  private transport: Transport;
  private loader: ESPLoader;
  info: ChipInfo | null = null;

  constructor(readonly port: SerialPort, baudrate: number, terminal: IEspLoaderTerminal) {
    this.transport = new Transport(port, false);
    this.loader = new ESPLoader({ transport: this.transport, baudrate, romBaudrate: ROM_BAUD, terminal });
  }

  async connect(): Promise<ChipInfo> {
    const description = await this.loader.main();
    const chip = this.loader.chip;
    let mac = "";
    let flashSize: string | null = null;
    try {
      mac = await chip.readMac(this.loader);
    } catch {
      // Secure download mode hides the MAC; not fatal for flashing.
    }
    try {
      flashSize = (await this.loader.detectFlashSize()) ?? null;
    } catch {
      flashSize = null;
    }
    this.info = { chipName: chip.CHIP_NAME, description, mac, flashSize };
    return this.info;
  }

  async flash(images: FlashImage[], eraseAll: boolean, onProgress: (p: FlashProgress) => void): Promise<void> {
    const total = images.reduce((n, img) => n + img.data.length, 0);
    const before = images.map((_, i) => images.slice(0, i).reduce((n, img) => n + img.data.length, 0));
    await this.loader.writeFlash({
      fileArray: images.map((img) => ({ data: img.data, address: img.address })),
      // Bootloader headers already carry the build's dio/16MB/80m settings.
      flashMode: "keep",
      flashFreq: "keep",
      flashSize: "keep",
      eraseAll,
      compress: true,
      reportProgress: (i, written, size) => {
        const part = size > 0 ? Math.min(written / size, 1) : 1;
        onProgress({
          imageIndex: i,
          imageName: images[i]?.name ?? "",
          fraction: (before[i] + part * images[i].data.length) / total,
        });
      },
      calculateMD5Hash: md5Hex,
    });
  }

  async resetAndClose(): Promise<void> {
    try {
      await this.loader.after("hard_reset");
    } finally {
      await this.close();
    }
  }

  async close(): Promise<void> {
    try {
      await this.transport.disconnect();
    } catch {
      // Port may already be gone after the USB device re-enumerated.
    }
  }
}

/** Translate low-level errors into actionable Chinese messages. */
export function describeError(err: unknown): string {
  const e = err as { name?: string; message?: string } | null;
  const name = e?.name ?? "";
  const msg = e?.message ?? String(err);
  if (name === "InvalidStateError" || /already open/i.test(msg))
    return "串口已被占用：请关闭 idf.py monitor、串口助手或其他打开该串口的页面后重试。";
  if (name === "NetworkError" || /Failed to open serial port/i.test(msg))
    return "无法打开串口：设备可能已被其他程序占用或已断开，请重新插拔 USB 后重试。";
  if (/Failed to connect|No serial data received|Timeout|timed out/i.test(msg))
    return "无法进入下载模式：请按住 BOOT 键，短按 RESET（RST）键，再松开 BOOT 后重新连接。";
  if (/device has been lost|device lost|disconnected/i.test(msg)) return "设备已断开连接。";
  return msg;
}

export const isUserCancel = (err: unknown) => (err as { name?: string } | null)?.name === "NotFoundError";
