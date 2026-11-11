import "./style.css";
import { ESPRESSIF_VID } from "esptool-js";
import { FlashSession, describeError, isUserCancel, type ChipInfo } from "./flasher";
import { FLASH_SIZE_16MB, guessOffset, hex, parseOffset, validateImages } from "./localFiles";
import { downloadParts, fetchManifest, type FlashImage, type Manifest } from "./manifest";
import { LogBuffer, SerialMonitor } from "./monitor";

const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;
const el = {
  badge: $("fw-badge"),
  unsupported: $("unsupported"),
  baud: $<HTMLSelectElement>("baud"),
  allPorts: $<HTMLInputElement>("all-ports"),
  connect: $<HTMLButtonElement>("btn-connect"),
  disconnect: $<HTMLButtonElement>("btn-disconnect"),
  chipInfo: $("chip-info"),
  connectMsg: $("connect-msg"),
  tabRelease: $<HTMLButtonElement>("tab-release"),
  tabLocal: $<HTMLButtonElement>("tab-local"),
  panelRelease: $("panel-release"),
  panelLocal: $("panel-local"),
  releaseLoading: $("release-loading"),
  releaseMissing: $("release-missing"),
  releaseInfo: $("release-info"),
  releaseMeta: $("release-meta"),
  releaseParts: $("release-parts"),
  localInput: $<HTMLInputElement>("local-input"),
  localTable: $("local-table"),
  localRows: $("local-rows"),
  localErrors: $("local-errors"),
  eraseAll: $<HTMLInputElement>("erase-all"),
  flash: $<HTMLButtonElement>("btn-flash"),
  progressWrap: $("progress-wrap"),
  progressBar: $("progress-bar"),
  progressText: $("progress-text"),
  progressPct: $("progress-pct"),
  flashMsg: $("flash-msg"),
  monitorStart: $<HTMLButtonElement>("btn-monitor"),
  monitorStop: $<HTMLButtonElement>("btn-monitor-stop"),
  reset: $<HTMLButtonElement>("btn-reset"),
  autoscroll: $<HTMLInputElement>("autoscroll"),
  clear: $<HTMLButtonElement>("btn-clear"),
  save: $<HTMLButtonElement>("btn-save"),
  monitorMsg: $("monitor-msg"),
  monitor: $("monitor"),
  esptoolLog: $("esptool-log"),
};

type Mode = "release" | "local";
type Busy = null | "connecting" | "flashing" | "monitor-opening";
interface LocalImage { id: number; file: File; offsetText: string }

const state = {
  supported: "serial" in navigator,
  busy: null as Busy,
  session: null as FlashSession | null,
  chip: null as ChipInfo | null,
  /** Last port the user granted; reused for the monitor after flashing. */
  lastPort: null as SerialPort | null,
  monitor: null as SerialMonitor | null,
  mode: "release" as Mode,
  manifest: null as Manifest | null,
  local: [] as LocalImage[],
};
let localSeq = 0;

const manifestUrl = new URL("firmware/manifest.json", document.baseURI);
const fmtBytes = (n: number) => (n >= 1 << 20 ? (n / (1 << 20)).toFixed(2) + " MB" : (n / 1024).toFixed(1) + " KB");

function setMsg(node: HTMLElement, text: string | null, kind: "ok" | "warn" | "error" | "" = "") {
  node.hidden = !text;
  node.textContent = text ?? "";
  node.className = "msg" + (kind ? " " + kind : "");
}

function fillInfo(dl: HTMLElement, rows: [string, string][]) {
  dl.replaceChildren(
    ...rows.flatMap(([k, v]) => {
      const dt = document.createElement("dt");
      dt.textContent = k;
      const dd = document.createElement("dd");
      dd.textContent = v;
      return [dt, dd];
    }),
  );
}

// ---------- esptool output ----------
const esptoolLog = new LogBuffer(3000);
const terminal = {
  clean: () => { esptoolLog.clear(); el.esptoolLog.textContent = ""; },
  writeLine: (data: string) => terminal.write(data + "\n"),
  write: (data: string) => {
    esptoolLog.push(data);
    el.esptoolLog.textContent = esptoolLog.text();
    el.esptoolLog.scrollTop = el.esptoolLog.scrollHeight;
  },
};

// ---------- flash size checks ----------
function flashSizeBytes(size: string | null): number | null {
  const m = size?.match(/^(\d+)(KB|MB)$/);
  if (!m) return null;
  return Number(m[1]) * (m[2] === "MB" ? 1 << 20 : 1 << 10);
}

/** Returns a blocking problem with the connected chip, or null. */
function chipProblem(chip: ChipInfo): string | null {
  if (chip.chipName !== "ESP32-S3") return `检测到 ${chip.chipName}，本固件仅适用于 ESP32-S3。`;
  const bytes = flashSizeBytes(chip.flashSize);
  if (bytes !== null && bytes < FLASH_SIZE_16MB) return `检测到 Flash 为 ${chip.flashSize}，本固件需要 16MB Flash。`;
  return null;
}

// ---------- render ----------
function localImagesValid(): boolean {
  const ranges = state.local.map((l) => ({ name: l.file.name, offset: parseOffset(l.offsetText) ?? -1, size: l.file.size }));
  const errors: string[] = [];
  for (const r of ranges) if (r.offset < 0) errors.push(`${r.name} 的偏移格式无效（示例：0x10000）`);
  const flashBytes = flashSizeBytes(state.chip?.flashSize ?? null) ?? FLASH_SIZE_16MB;
  if (errors.length === 0) errors.push(...validateImages(ranges, flashBytes));
  el.localErrors.replaceChildren(
    ...(state.local.length ? errors : []).map((e) => Object.assign(document.createElement("li"), { textContent: e })),
  );
  return state.local.length > 0 && errors.length === 0;
}

function render() {
  const { busy, session, monitor } = state;
  const connected = session !== null;
  el.unsupported.hidden = state.supported;
  el.connect.hidden = connected;
  el.disconnect.hidden = !connected;
  el.connect.disabled = !state.supported || busy !== null || monitor !== null;
  el.connect.textContent = busy === "connecting" ? "连接中…" : "连接";
  el.disconnect.disabled = busy !== null;
  el.baud.disabled = connected || busy !== null;
  el.allPorts.disabled = busy !== null;

  el.tabRelease.classList.toggle("active", state.mode === "release");
  el.tabLocal.classList.toggle("active", state.mode === "local");
  el.tabRelease.setAttribute("aria-selected", String(state.mode === "release"));
  el.tabLocal.setAttribute("aria-selected", String(state.mode === "local"));
  el.panelRelease.hidden = state.mode !== "release";
  el.panelLocal.hidden = state.mode !== "local";
  el.tabRelease.disabled = el.tabLocal.disabled = el.localInput.disabled = busy === "flashing";
  el.eraseAll.disabled = busy === "flashing";

  const sourceReady = state.mode === "release" ? state.manifest !== null : localImagesValid();
  const chipOk = state.chip !== null && chipProblem(state.chip) === null;
  el.flash.disabled = !connected || busy !== null || !sourceReady || !chipOk;
  el.flash.textContent = busy === "flashing" ? "刷入中…" : connected ? "开始刷入" : "请先连接设备";

  el.monitorStart.hidden = monitor !== null;
  el.monitorStop.hidden = monitor === null;
  el.monitorStart.disabled = !state.supported || busy !== null;
  el.reset.disabled = monitor === null;
}

// ---------- port selection ----------
async function requestPort(): Promise<SerialPort> {
  const filters = el.allPorts.checked ? [] : [{ usbVendorId: ESPRESSIF_VID }];
  const port = await navigator.serial.requestPort({ filters });
  state.lastPort = port;
  return port;
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

// ---------- connect ----------
async function connect() {
  setMsg(el.connectMsg, null);
  let port: SerialPort;
  try {
    port = await requestPort();
  } catch (err) {
    if (!isUserCancel(err)) setMsg(el.connectMsg, describeError(err), "error");
    return;
  }
  state.busy = "connecting";
  render();
  const session = new FlashSession(port, Number(el.baud.value), terminal);
  try {
    const chip = await session.connect();
    state.session = session;
    state.chip = chip;
    fillInfo(el.chipInfo, [
      ["芯片", chip.description],
      ["MAC", chip.mac || "未知"],
      ["Flash", chip.flashSize ?? "未知"],
    ]);
    el.chipInfo.hidden = false;
    const problem = chipProblem(chip);
    if (problem) setMsg(el.connectMsg, problem, "error");
    else if (!chip.flashSize) setMsg(el.connectMsg, "未能检测 Flash 容量，请确认开发板为 16MB 版本（N16R8）。", "warn");
    else setMsg(el.connectMsg, "已进入下载模式，可以开始刷入。", "ok");
  } catch (err) {
    await session.close();
    setMsg(el.connectMsg, describeError(err), "error");
  } finally {
    state.busy = null;
    render();
  }
}

async function disconnect() {
  const session = state.session;
  state.session = null;
  state.chip = null;
  el.chipInfo.hidden = true;
  setMsg(el.connectMsg, null);
  render();
  // Reboot into the firmware rather than leaving the chip parked in the bootloader.
  await session?.resetAndClose();
}

// ---------- flash ----------
function setProgress(fraction: number, text: string) {
  el.progressWrap.hidden = false;
  const pct = Math.round(fraction * 1000) / 10;
  el.progressBar.style.width = pct + "%";
  el.progressPct.textContent = pct.toFixed(1) + "%";
  el.progressText.textContent = text;
}

async function collectImages(): Promise<FlashImage[]> {
  if (state.mode === "release") {
    const manifest = state.manifest!;
    return downloadParts(manifest, manifestUrl, (loaded, total) =>
      setProgress(0, `下载并校验固件 ${fmtBytes(loaded)} / ${fmtBytes(total)}`),
    );
  }
  const images: FlashImage[] = [];
  for (const l of state.local) {
    images.push({ name: l.file.name, address: parseOffset(l.offsetText)!, data: new Uint8Array(await l.file.arrayBuffer()) });
  }
  return images.sort((a, b) => a.address - b.address);
}

async function flash() {
  const session = state.session;
  if (!session) return;
  const eraseAll = el.eraseAll.checked;
  if (
    eraseAll &&
    !confirm("整片擦除会清除 Wi-Fi 配网、屏幕亮度等全部 NVS 数据，刷入后需重新配网。确定继续？")
  )
    return;

  state.busy = "flashing";
  setMsg(el.flashMsg, null);
  render();
  let started = false;
  let written = false;
  try {
    setProgress(0, "准备固件…");
    const images = await collectImages();
    setProgress(0, eraseAll ? "正在擦除整片 Flash（约需 30 秒）…" : "开始写入…");
    started = true;
    await session.flash(images, eraseAll, (p) =>
      setProgress(p.fraction, `正在写入 ${p.imageName}（${p.imageIndex + 1}/${images.length}）`),
    );
    written = true;
    setProgress(1, "写入完成，校验通过，正在重启设备…");
    state.session = null;
    state.chip = null;
    el.chipInfo.hidden = true;
    setMsg(el.connectMsg, null);
    await session.resetAndClose();
    setProgress(1, "完成");
    setMsg(
      el.flashMsg,
      eraseAll
        ? "刷入成功，设备已重启。首次启动会进入配网模式，请按屏幕提示连接 Aemeath-XXXX 热点完成配网。"
        : "刷入成功，设备已重启。可在下方打开串口日志确认启动情况。",
      "ok",
    );
  } catch (err) {
    const detail = describeError(err);
    if (started && !written) {
      setMsg(el.flashMsg, `刷入未完成：${detail}\n设备可能无法正常启动，请重新进入下载模式后重新刷入。`, "error");
    } else {
      setMsg(el.flashMsg, detail, "error");
    }
    if (!written) {
      // The loader state is unknown after a failure; require a fresh connection.
      state.session = null;
      state.chip = null;
      el.chipInfo.hidden = true;
      await session.close();
    }
  } finally {
    state.busy = null;
    render();
  }
}

// ---------- release manifest ----------
async function loadManifest() {
  try {
    state.manifest = await fetchManifest(manifestUrl);
  } catch (err) {
    state.manifest = null;
    setMsg(el.releaseMissing, `读取发布固件失败：${describeError(err)}`, "error");
    el.releaseLoading.hidden = true;
    state.mode = "local";
    render();
    return;
  }
  el.releaseLoading.hidden = true;
  const m = state.manifest;
  if (!m) {
    el.releaseMissing.hidden = false;
    state.mode = "local";
    render();
    return;
  }
  el.badge.hidden = false;
  el.badge.textContent = "固件 v" + m.version;
  const total = m.parts.reduce((n, p) => n + p.size, 0);
  fillInfo(el.releaseMeta, [
    ["版本", m.version + (m.projectVersion && m.projectVersion !== m.version ? `（${m.projectVersion}）` : "")],
    ["构建时间", new Date(m.builtAt).toLocaleString()],
    ["ESP-IDF", m.idfVersion ?? "未知"],
    ["Flash 参数", `${m.flash.mode} / ${m.flash.size} / ${m.flash.freq}`],
    ["总大小", fmtBytes(total)],
  ]);
  el.releaseParts.replaceChildren(
    ...m.parts.map((p) => {
      const tr = document.createElement("tr");
      for (const [text, mono] of [[p.name, false], [hex(p.offset), true], [p.path, true], [fmtBytes(p.size), false]] as const) {
        const td = document.createElement("td");
        td.textContent = text;
        if (mono) td.className = "mono";
        tr.append(td);
      }
      return tr;
    }),
  );
  el.releaseInfo.hidden = false;
  render();
}

// ---------- local files ----------
function renderLocal() {
  el.localTable.hidden = state.local.length === 0;
  el.localRows.replaceChildren(
    ...state.local.map((l) => {
      const tr = document.createElement("tr");
      const name = document.createElement("td");
      name.textContent = l.file.name;
      const offTd = document.createElement("td");
      const input = Object.assign(document.createElement("input"), { type: "text", value: l.offsetText, className: "offset" });
      input.classList.toggle("invalid", parseOffset(l.offsetText) === null);
      input.addEventListener("input", () => {
        l.offsetText = input.value;
        input.classList.toggle("invalid", parseOffset(input.value) === null);
        render();
      });
      offTd.append(input);
      const size = document.createElement("td");
      size.textContent = fmtBytes(l.file.size);
      const rm = document.createElement("td");
      const btn = Object.assign(document.createElement("button"), { className: "link", textContent: "移除" });
      btn.addEventListener("click", () => {
        state.local = state.local.filter((x) => x.id !== l.id);
        renderLocal();
      });
      rm.append(btn);
      tr.append(name, offTd, size, rm);
      return tr;
    }),
  );
  render();
}

el.localInput.addEventListener("change", () => {
  for (const file of Array.from(el.localInput.files ?? [])) {
    state.local.push({ id: ++localSeq, file, offsetText: hex(guessOffset(file.name)) });
  }
  el.localInput.value = "";
  renderLocal();
});

// ---------- monitor ----------
const monitorLog = new LogBuffer(5000);
let monitorDirty = false;
function scheduleMonitorRender() {
  if (monitorDirty) return;
  monitorDirty = true;
  requestAnimationFrame(() => {
    monitorDirty = false;
    el.monitor.textContent = monitorLog.text();
    if (el.autoscroll.checked) el.monitor.scrollTop = el.monitor.scrollHeight;
  });
}

async function startMonitor() {
  setMsg(el.monitorMsg, null);
  state.busy = "monitor-opening";
  render();
  try {
    if (state.session) {
      // Leave the bootloader and free the port so the firmware's console is visible.
      const s = state.session;
      state.session = null;
      state.chip = null;
      el.chipInfo.hidden = true;
      setMsg(el.connectMsg, null);
      await s.resetAndClose();
    }
    let port = await findGrantedPort(state.lastPort);
    if (!port) {
      state.busy = null;
      render();
      try {
        port = await requestPort();
      } catch (err) {
        if (!isUserCancel(err)) setMsg(el.monitorMsg, describeError(err), "error");
        return;
      }
      state.busy = "monitor-opening";
      render();
    }
    state.lastPort = port;
    const monitor = new SerialMonitor(
      port,
      (text) => { monitorLog.push(text); scheduleMonitorRender(); },
      (err) => {
        if (state.monitor === monitor) state.monitor = null;
        if (err) setMsg(el.monitorMsg, "串口日志已断开：" + describeError(err), "warn");
        render();
      },
    );
    await monitor.start(115200);
    state.monitor = monitor;
    setMsg(el.monitorMsg, null);
  } catch (err) {
    setMsg(el.monitorMsg, describeError(err), "error");
  } finally {
    state.busy = null;
    render();
  }
}

async function stopMonitor() {
  const m = state.monitor;
  state.monitor = null;
  render();
  await m?.stop();
}

// ---------- wiring ----------
el.connect.addEventListener("click", connect);
el.disconnect.addEventListener("click", disconnect);
el.flash.addEventListener("click", flash);
el.tabRelease.addEventListener("click", () => { state.mode = "release"; render(); });
el.tabLocal.addEventListener("click", () => { state.mode = "local"; render(); });
el.monitorStart.addEventListener("click", startMonitor);
el.monitorStop.addEventListener("click", stopMonitor);
el.reset.addEventListener("click", () => {
  state.monitor?.resetDevice().catch((err) => setMsg(el.monitorMsg, describeError(err), "error"));
});
el.clear.addEventListener("click", () => { monitorLog.clear(); el.monitor.textContent = ""; });
el.save.addEventListener("click", () => {
  const blob = new Blob([monitorLog.text()], { type: "text/plain" });
  const a = Object.assign(document.createElement("a"), {
    href: URL.createObjectURL(blob),
    download: `aemeath-${new Date().toISOString().replace(/[:.]/g, "-")}.log`,
  });
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
});
window.addEventListener("beforeunload", (e) => {
  if (state.busy === "flashing") e.preventDefault();
});
if (state.supported) {
  navigator.serial.addEventListener("disconnect", (e) => {
    const port = e.target as SerialPort;
    if (state.session?.port === port && state.busy !== "flashing") {
      state.session = null;
      state.chip = null;
      el.chipInfo.hidden = true;
      setMsg(el.connectMsg, "设备已断开连接。", "warn");
      render();
    }
  });
}

render();
void loadManifest();
