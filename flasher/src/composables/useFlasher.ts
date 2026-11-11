import { computed, markRaw, reactive, ref, shallowRef } from "vue";
import { ESPRESSIF_VID } from "esptool-js";
import { FlashSession, describeError, isUserCancel, type ChipInfo } from "@/flasher";
import { FLASH_SIZE_16MB, guessOffset, hex, parseOffset, validateImages } from "@/localFiles";
import { downloadParts, fetchManifest, type FlashImage, type Manifest } from "@/manifest";
import { LogBuffer, SerialMonitor } from "@/monitor";

export type Tone = "ok" | "warn" | "error" | "info";
export interface Notice { tone: Tone; text: string }
export type Mode = "release" | "local";
export type Busy = null | "connecting" | "flashing" | "monitor-opening";
export interface LocalImage { id: number; file: File; offsetText: string }

export const BAUD_RATES = [115200, 460800, 921600] as const;

const manifestUrl = new URL("firmware/manifest.json", document.baseURI);

export const fmtBytes = (n: number) =>
  n >= 1 << 20 ? (n / (1 << 20)).toFixed(2) + " MB" : (n / 1024).toFixed(1) + " KB";

function flashSizeBytes(size: string | null | undefined): number | null {
  const m = size?.match(/^(\d+)(KB|MB)$/);
  if (!m) return null;
  return Number(m[1]) * (m[2] === "MB" ? 1 << 20 : 1 << 10);
}

/** Returns a blocking problem with the connected chip, or null. */
export function chipProblem(chip: ChipInfo): string | null {
  if (chip.chipName !== "ESP32-S3") return `检测到 ${chip.chipName}，本固件仅适用于 ESP32-S3。`;
  const bytes = flashSizeBytes(chip.flashSize);
  if (bytes !== null && bytes < FLASH_SIZE_16MB) return `检测到 Flash 为 ${chip.flashSize}，本固件需要 16MB Flash。`;
  return null;
}

function samePortInfo(a: SerialPortInfo, b: SerialPortInfo) {
  return a.usbVendorId === b.usbVendorId && a.usbProductId === b.usbProductId;
}

/**
 * USB-Serial/JTAG re-enumerates when the chip resets, which can invalidate the old
 * SerialPort object. Prefer the original port, then wait briefly for a granted port
 * with the same VID/PID to reappear.
 */
async function findGrantedPort(previous: SerialPort | null, timeoutMs = 4000): Promise<SerialPort | null> {
  if (!previous) return null;
  const info = previous.getInfo();
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const ports = await navigator.serial.getPorts();
    if (ports.includes(previous)) return previous;
    const match = ports.find((p) => samePortInfo(p.getInfo(), info));
    if (match) return match;
    await new Promise((r) => setTimeout(r, 300));
  }
  return null;
}

function createFlasher() {
  const supported = typeof navigator !== "undefined" && "serial" in navigator;

  // Native/serial objects must never be wrapped in reactive proxies.
  const session = shallowRef<FlashSession | null>(null);
  const monitor = shallowRef<SerialMonitor | null>(null);
  let lastPort: SerialPort | null = null;

  const busy = ref<Busy>(null);
  const chip = ref<ChipInfo | null>(null);
  const baud = ref<string>(String(BAUD_RATES[0]));
  const allPorts = ref(false);
  const mode = ref<Mode>("release");
  const manifest = shallowRef<Manifest | null>(null);
  const manifestState = ref<"loading" | "ready" | "missing" | "error">("loading");
  const local = ref<LocalImage[]>([]);
  const eraseAll = ref(false);
  const notices = reactive<{ connect: Notice | null; flash: Notice | null; monitor: Notice | null; manifest: Notice | null }>({
    connect: null,
    flash: null,
    monitor: null,
    manifest: null,
  });
  const progress = reactive({ visible: false, fraction: 0, text: "" });
  let localSeq = 0;

  // ---- logs ----
  const esptoolBuffer = new LogBuffer(3000);
  const esptoolLog = ref("");
  const terminal = {
    clean: () => { esptoolBuffer.clear(); esptoolLog.value = ""; },
    writeLine: (data: string) => terminal.write(data + "\n"),
    write: (data: string) => { esptoolBuffer.push(data); esptoolLog.value = esptoolBuffer.text(); },
  };
  const monitorBuffer = new LogBuffer(5000);
  const monitorLog = ref("");
  let monitorDirty = false;
  function pushMonitor(text: string) {
    monitorBuffer.push(text);
    if (monitorDirty) return;
    monitorDirty = true;
    requestAnimationFrame(() => { monitorDirty = false; monitorLog.value = monitorBuffer.text(); });
  }
  function clearMonitor() { monitorBuffer.clear(); monitorLog.value = ""; }

  // ---- derived ----
  const connected = computed(() => session.value !== null);
  const chipError = computed(() => (chip.value ? chipProblem(chip.value) : null));
  const localErrors = computed(() => {
    if (local.value.length === 0) return [];
    const ranges = local.value.map((l) => ({ name: l.file.name, offset: parseOffset(l.offsetText) ?? -1, size: l.file.size }));
    const bad = ranges.filter((r) => r.offset < 0).map((r) => `${r.name} 的偏移格式无效（示例：0x10000）`);
    if (bad.length) return bad;
    return validateImages(ranges, flashSizeBytes(chip.value?.flashSize) ?? FLASH_SIZE_16MB);
  });
  const sourceReady = computed(() =>
    mode.value === "release" ? manifest.value !== null : local.value.length > 0 && localErrors.value.length === 0,
  );
  const canFlash = computed(
    () => connected.value && busy.value === null && sourceReady.value && chip.value !== null && chipError.value === null,
  );
  /** 1 = connect, 2 = flash, 3 = monitor; drives the step indicator. */
  const step = computed(() => (monitor.value ? 3 : connected.value ? 2 : notices.flash?.tone === "ok" ? 3 : 1));

  function setProgress(fraction: number, text: string) {
    progress.visible = true;
    progress.fraction = fraction;
    progress.text = text;
  }

  function dropSession() {
    session.value = null;
    chip.value = null;
  }

  async function requestPort(): Promise<SerialPort> {
    const filters = allPorts.value ? [] : [{ usbVendorId: ESPRESSIF_VID }];
    const port = await navigator.serial.requestPort({ filters });
    lastPort = port;
    return port;
  }

  // ---- connect ----
  async function connect() {
    notices.connect = null;
    let port: SerialPort;
    try {
      port = await requestPort();
    } catch (err) {
      if (!isUserCancel(err)) notices.connect = { tone: "error", text: describeError(err) };
      return;
    }
    busy.value = "connecting";
    const s = markRaw(new FlashSession(port, Number(baud.value), terminal));
    try {
      const info = await s.connect();
      session.value = s;
      chip.value = info;
      const problem = chipProblem(info);
      if (problem) notices.connect = { tone: "error", text: problem };
      else if (!info.flashSize) notices.connect = { tone: "warn", text: "未能检测 Flash 容量，请确认开发板为 16MB 版本（N16R8）。" };
      else notices.connect = { tone: "ok", text: "已进入下载模式，可以开始刷入。" };
    } catch (err) {
      await s.close();
      notices.connect = { tone: "error", text: describeError(err) };
    } finally {
      busy.value = null;
    }
  }

  async function disconnect() {
    const s = session.value;
    dropSession();
    notices.connect = null;
    // Reboot into the firmware rather than leaving the chip parked in the bootloader.
    await s?.resetAndClose();
  }

  // ---- flash ----
  async function collectImages(): Promise<FlashImage[]> {
    if (mode.value === "release") {
      return downloadParts(manifest.value!, manifestUrl, (loaded, total) =>
        setProgress(0, `下载并校验固件 ${fmtBytes(loaded)} / ${fmtBytes(total)}`),
      );
    }
    const images: FlashImage[] = [];
    for (const l of local.value) {
      images.push({ name: l.file.name, address: parseOffset(l.offsetText)!, data: new Uint8Array(await l.file.arrayBuffer()) });
    }
    return images.sort((a, b) => a.address - b.address);
  }

  async function flash() {
    const s = session.value;
    if (!s || !canFlash.value) return;
    const erase = eraseAll.value;
    busy.value = "flashing";
    notices.flash = null;
    let started = false;
    let written = false;
    try {
      setProgress(0, "准备固件…");
      const images = await collectImages();
      setProgress(0, erase ? "正在擦除整片 Flash（约需 30 秒）…" : "开始写入…");
      started = true;
      await s.flash(images, erase, (p) =>
        setProgress(p.fraction, `正在写入 ${p.imageName}（${p.imageIndex + 1}/${images.length}）`),
      );
      written = true;
      setProgress(1, "写入完成，校验通过，正在重启设备…");
      dropSession();
      notices.connect = null;
      await s.resetAndClose();
      setProgress(1, "完成");
      notices.flash = {
        tone: "ok",
        text: erase
          ? "刷入成功，设备已重启。首次启动会进入配网模式，请按屏幕提示连接 Aemeath-XXXX 热点完成配网。"
          : "刷入成功，设备已重启。可在串口日志中确认启动情况。",
      };
    } catch (err) {
      const detail = describeError(err);
      notices.flash = started && !written
        ? { tone: "error", text: `刷入未完成：${detail}\n设备可能无法正常启动，请重新进入下载模式后重新刷入。` }
        : { tone: "error", text: detail };
      if (!written) {
        // The loader state is unknown after a failure; require a fresh connection.
        dropSession();
        await s.close();
      }
    } finally {
      busy.value = null;
    }
  }

  // ---- manifest ----
  async function loadManifest() {
    try {
      manifest.value = await fetchManifest(manifestUrl);
      manifestState.value = manifest.value ? "ready" : "missing";
    } catch (err) {
      manifest.value = null;
      manifestState.value = "error";
      notices.manifest = { tone: "error", text: `读取发布固件失败：${describeError(err)}` };
    }
    if (!manifest.value) mode.value = "local";
  }

  // ---- local files ----
  function addLocalFiles(files: FileList | File[]) {
    for (const file of Array.from(files)) {
      local.value.push({ id: ++localSeq, file: markRaw(file), offsetText: hex(guessOffset(file.name)) });
    }
  }
  function removeLocal(id: number) {
    local.value = local.value.filter((l) => l.id !== id);
  }

  // ---- monitor ----
  async function startMonitor() {
    notices.monitor = null;
    busy.value = "monitor-opening";
    try {
      if (session.value) {
        // Leave the bootloader and free the port so the firmware's console is visible.
        const s = session.value;
        dropSession();
        notices.connect = null;
        await s.resetAndClose();
      }
      let port = await findGrantedPort(lastPort);
      if (!port) {
        busy.value = null;
        try {
          port = await requestPort();
        } catch (err) {
          if (!isUserCancel(err)) notices.monitor = { tone: "error", text: describeError(err) };
          return;
        }
        busy.value = "monitor-opening";
      }
      lastPort = port;
      const m: SerialMonitor = markRaw(
        new SerialMonitor(port, pushMonitor, (err) => {
          if (monitor.value === m) monitor.value = null;
          if (err) notices.monitor = { tone: "warn", text: "串口日志已断开：" + describeError(err) };
        }),
      );
      await m.start(115200);
      monitor.value = m;
    } catch (err) {
      notices.monitor = { tone: "error", text: describeError(err) };
    } finally {
      busy.value = null;
    }
  }

  async function stopMonitor() {
    const m = monitor.value;
    monitor.value = null;
    await m?.stop();
  }

  async function resetDevice() {
    try {
      await monitor.value?.resetDevice();
    } catch (err) {
      notices.monitor = { tone: "error", text: describeError(err) };
    }
  }

  function downloadMonitorLog() {
    const blob = new Blob([monitorBuffer.text()], { type: "text/plain" });
    const a = Object.assign(document.createElement("a"), {
      href: URL.createObjectURL(blob),
      download: `aemeath-${new Date().toISOString().replace(/[:.]/g, "-")}.log`,
    });
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  }

  // ---- global listeners ----
  if (typeof window !== "undefined") {
    window.addEventListener("beforeunload", (e) => {
      if (busy.value === "flashing") e.preventDefault();
    });
  }
  if (supported) {
    navigator.serial.addEventListener("disconnect", (e) => {
      if (session.value?.port === e.target && busy.value !== "flashing") {
        dropSession();
        notices.connect = { tone: "warn", text: "设备已断开连接。" };
      }
    });
  }

  return {
    supported, busy, chip, baud, allPorts, mode, manifest, manifestState, local, eraseAll, notices, progress,
    esptoolLog, monitorLog, connected, chipError, localErrors, canFlash, step, monitor,
    connect, disconnect, flash, loadManifest, addLocalFiles, removeLocal,
    startMonitor, stopMonitor, resetDevice, clearMonitor, downloadMonitorLog, clearEsptool: terminal.clean,
  };
}

let instance: ReturnType<typeof createFlasher> | null = null;

/** Shared flasher state for all cards. */
export function useFlasher() {
  instance ??= createFlasher();
  return instance;
}
