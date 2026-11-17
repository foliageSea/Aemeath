# 语音功能验证记录

本次验证环境：macOS ARM64、Go 模块声明版本 1.27.3、ESP-IDF 6.0.2、LVGL 9.6.0~1、esp_codec_dev 1.6.2、sherpa-onnx Go/native 2.0.1。验证日期：2026-11-09。

## I²S DMA 内存不足导致设备无法采音（2026-11-17）

- 连接实板串口后确认根因：`i2s_alloc_dma_desc: allocate DMA buffer failed` → `i2s_channel_init_std_mode` → `audio init: ESP_ERR_NO_MEM`，发生在 codec 初始化之前，不是唤醒模型或 ASR 识别失败。原始记录见 [初始化失败串口日志](../firmware/build/voice-init-diagnostic.log)。
- ESP32-S3 双工 I²S 需要内部 DMA 内存：每方向 4 × 320 × 2 声道 × 2 字节，即双向 PCM 缓冲共 10,240 字节，另有描述符及驱动分配；PSRAM 空闲不能替代内部 DMA 内存。原 48 KiB 内部预留还与 FreeRTOS 等内部专用分配共享，两个 voice 任务栈就占用 20 KiB。
- `sdkconfig.defaults` 与本地实际构建配置均将内部预留提高到 128 KiB，普通 malloc 内部优先阈值从 4096 降到 1024 字节，让 UI/网络普通分配更早使用 PSRAM。保留 16 kHz／320 样本／4 DMA 块及 AEC，不削减音频协议或把 DMA 缓冲搬到 PSRAM。
- 实测仅增大预留池仍不够：96 KiB 初始化后 DMA 空闲仅 803 字节，128 KiB 也仅 1367 字节，表明内部专用分配占主导。进一步将仅做 PCM/AEC 和同步入发送队列的 `voice_mic` 12 KiB CPU 工作栈通过 IDF `xTaskCreateWithCaps` 显式放在 PSRAM；任务 TCB、I²S DMA／描述符和 ISR 状态仍在内部 RAM，栈大小不缩减，保留不执行 flash/NVS 的边界；未来若添加该永久任务的删除路径必须使用 `vTaskDeleteWithCaps`。默认配置显式启用 `FREERTOS_TASK_CREATE_ALLOW_EXT_MEM`，主机会话测试断言 capture 栈能力和大小。
- 音频初始化新增分阶段错误与 DMA 总空闲／最大连续块快照，成功时也记录余量；IDF 可能在返回失败前回收部分 DMA，因此失败快照不能当作失败瞬间的精确峰值。每个新连接在首个 AEC 处理后麦克风帧入发送队列时记录 `capture streaming`，不记录用户音频内容。
- 新增 TX 和 RX DMA 初始化内存失败注入，各连续失败三次后成功重试，检查所有队列、AEC、codec、I²S 及陈旧 DMA 状态清理，且失败期间不配置 codec、不启用通道，重试后实际驱动播放回调。音频和 voice/WebSocket 主机 ASan/UBSan 测试以及 ESP-IDF 6.0.2 构建通过。

- 经用户明确授权，最终仅向同一 ESP32-S3（MAC `E8:F6:0A:A5:B7:54`）应用偏移 `0x10000` 写入 5,446,480 字节，esptool 哈希校验通过；未写 bootloader、分区表、NVS 或背景分区。尝试读取原应用备份时多次出现串口 packet header 错误，未获得完整备份；随后的应用写入与哈希校验均成功。
- 最终实板日志确认：新构建启动、96/128 KiB 对照后采用 128 KiB 预留与 PSRAM capture 栈；音频初始化后内部空闲 17,327 字节、DMA 空闲 13,599 字节、DMA 最大连续块 13,312 字节，随后 `capture streaming: 16000 Hz mono, 320 samples/frame`；观察到一次连接关闭后自动重连，新 epoch 再次开始采音，45 秒串口观察未出现 `audio init`／`capture/AEC failed`、内存不足或 panic。Wi-Fi、5% 背光和背景 revision 9 均恢复。记录见 [最终烧录后串口日志](../firmware/build/voice-dma-psram-stack-post-flash.log)。
- 本次实机确认的是 codec／I²S／AEC 初始化、真实麦克风帧入队及重连恢复；未主动调用付费 ASR/LLM/TTS，没有声称真人唤醒成功率、扬声器音质或长时间压力测试完成。

## 设备初始化错误被状态错误覆盖（2026-11-17）

- 桌面日志确认：设备在首轮前报告 `Audio hardware initialization failed` 后，Agent 使用同一错误通路向设备回发 `error` 和 `state:error,turn:0`；旧固件拒绝零轮次非 idle 状态，回报 `Invalid voice state`，进而覆盖原始故障并反复回传。
- Agent 收到设备错误后只取消本地录音、播放和云任务并发布错误快照，不向设备回显 cancel/error/state。自身的首轮前错误只发送 error 消息，兼容旧固件；正常活动轮次的本地错误仍按原协议通知设备。设备 `audio_unavailable` 会清除唤醒就绪标志，避免本地模型加载成功被误当作设备采集可用。
- 固件允许 `turn:0` 的终止 error 状态，仍拒绝零轮次的唤醒、聆听、识别、思考、合成、播放和未知状态；后续正常轮次可继续开启。
- 新增真实本地 WebSocket 回归，覆盖零轮次和活动轮次的设备错误无回显、原始错误脱敏保留、本地工作取消、首轮前本地错误兼容，以及活动轮次错误通知。完整 `go test -mod=readonly ./...`、`go vet -mod=readonly ./...`、`go test -mod=readonly -race -count=1 ./internal/voice` 均通过。固件主机会话回归使用实际 `voice.c` 配合 ASan/UBSan 验证零轮次校验和后续恢复；语音优先级／WebSocket 优先级及既有音频主机测试通过。ESP-IDF 6.0.2 的 `idf.py build` 编译通过。
- 此协议修复阶段尚未确认原始音频初始化失败原因，也未烧录设备、替换正在运行的应用或调用付费云 API。后续串口定位和经用户授权的实板更新结果见上方“I²S DMA 内存不足”记录。

## 智能音箱式唤醒提示音（2026-11-16）

- 唤醒交互改为“唤醒词 → 短提示音 → 提问”：语音唤醒、触屏唤醒、“手动唤醒”按钮以及仅识别到唤醒词后的重新聆听，都会先播放约 0.58 秒的语音提示“我在”（用当前 Fish reference_id 音色经 `agent/scripts/wake-cue-gen` 合成一次，裁剪静音、峰值归一化到 -3 dBFS，以 16 kHz 单声道 WAV 内置于 `agent/internal/voice/assets/wake_cue.wav`；唤醒时不调用云端），处于 `wake` 阶段；提示音实际播完（设备 `played` 或电脑输出排空）后才进入 `listening` 并开始录音。提示音结束前的麦克风音频、原先最多 2 秒的唤醒词后缀不再进入识别，不再支持连说“唤醒词 + 问题”。
- 提示音结束后再跳过约 150ms 回声尾音，然后沿用 5 秒开口超时、0.8 秒停顿断句和 30 秒上限。提示音使用独立的 wire turn，迟到的确认不会影响后续回复；取消、F8 和文字输入可在提示音期间打断。F8 按住说话和文字输入不播放提示音。提示音由电脑端生成并通过现有播放协议下发，**无需更新固件**，只需更新桌面应用 / CLI。设备播放提示音时屏幕会短暂显示播报状态再切换到聆听。
- 软件验证：`go test ./...`、`go vet ./...`、`go test -race -count=1 ./internal/voice` 通过；回归覆盖设备与独立模式提示音、提示音期间的音频丢弃、回声尾音跳过、仅唤醒词后再次提示、提示音期间取消及迟到确认隔离。没有烧录设备、替换正在运行的桌面应用或调用付费云端 API；提示音音量、实机回声尾长和唤醒体验仍需实机试听。

## 无 ESP32 独立模式（2026-11-13）

- 桌面在“设置 → 连接 → 运行模式”选择“独立模式”并保存；CLI 使用 `--standalone`，自动启用语音，无需额外 `--voice`。两者均直接打开系统默认麦克风与扬声器，不依赖 ESP32、设备发现、设备 `/ws` 或 `/voice` 连接。性能指标与 Magpie 额度仍在电脑本地更新。
- 独立模式自动采用电脑输出：有效 JSON 播放值为 `console`（界面“Agent”），即使已保存的是 `agent`（界面“监控台”），也不覆盖已保存的设备模式偏好或 CLI JSON 文件。电脑音频打开失败明确报错，不回退到设备。
- 本机录音需系统权限：macOS 在“隐私与安全性 → 麦克风”允许 Aemeath，CLI 允许所用终端宿主；Windows 开启“麦克风访问”和“允许桌面应用访问麦克风”。原生音频要求 cgo；关闭 cgo 的构建不提供本机音频。没有默认麦克风、拒绝授权和设备占用等情况仍需按系统实际错误处理。
- 本机播放没有 AEC；播放期间暂停自动唤醒，结束后恢复，不能用此路径声称支持播放期间的唤醒词语音打断。桌面 F8、手动唤醒/取消/文字和 CLI `record_start` / `wake` / `cancel` / `text` 仍可打断。离线唤醒与 VAD 在本地运行，ASR、配置的 LLM 和 MiMo/Fish TTS 仍需云服务与对应配置。
- CLI 回归新增参数与冲突、未连接设备的显式错误和本地指标/额度快照、手动录音及文字命令。`go test -mod=readonly ./cmd/aemeath-agent`、`go test -mod=readonly -race ./cmd/aemeath-agent` 与 `go vet -mod=readonly ./cmd/aemeath-agent` 通过。macOS CLI 的 darwin+cgo 构建内嵌 `__TEXT,__info_plist` 麦克风用途声明；已构建二进制并检查该节，Foundation `NSBundle mainBundle` 读取同一嵌入声明的本地验证通过，全程未打开麦克风。本节没有声称真人麦克风、系统权限弹窗、Windows 硬件、真实扬声器音质或付费云 API 已完成验收。
- 建议验收：断开 ESP32 后分别启动桌面与 CLI 独立模式，确认无发现/连接重试；允许/拒绝麦克风权限各测一次；用系统默认输入录音并经 ASR/LLM/TTS 从默认扬声器播报；播放中用手动操作打断，确认自动唤醒仅在播完恢复；停止服务、切换模式或 Ctrl+C 后确认录音和扬声器释放。切回设备模式后检查原设备地址与播放偏好仍保留。

## 播放回声消除与打断接续（2026-11-11）

- 固件接入固定版本 ESP-SR 2.6.1 的 FD_HIGH_PERF AEC，使用 NORMAL 残余回声抑制。参考来自已经播放完成的 TX DMA，含软件音量、欠载静音及功放状态；RX DMA 按采集时间与固定帧偏移配对。每帧检查 RX 间隔与参考时间，错配或丢帧终止该连接，不回退上传原始麦克风音频。参考历史、采集队列和 AEC 帧转换缓冲有界，较大的缓冲放在 PSRAM。
- 自动倾听、ASR、思考、合成或播放期间再次命中唤醒词，会取消旧任务并开启新轮倾听；保留关键词末端之后最多 2 秒的后缀。显式取消仍回到待命，F8 按住录音仍绕过 KWS/VAD。
- 取消与本地控制使用独立队列；正常播放文本与 PCM 按到达序号归并，避免 playback_end 越过音频。设备发送手动 wake 时保持与麦克风帧的顺序；本地取消、清空或静音会丢弃尚未发送的旧麦克风帧和已被覆盖的 wake。播放启动异步检查 DMA 清空后才开启功放并发放 credits，等待期间继续处理取消。采集配置/连接变更使用显式 revision，丢弃进行中的旧输出，重建 AEC 后清理 RX 队列并跳过恢复前暂存帧。
- 软件验证：完整 `go test -mod=readonly ./...`、`go vet -mod=readonly ./...`，voice/wake/desktop 的 `go test -race -count=1`，以及 ESP-IDF 6.0.2 固件构建通过。新增逐样本回归覆盖活动阶段打断、连续“唤醒词 + 新问题”、旧轮结果隔离；纯 C ASan/UBSan 验证 20ms/32ms 分帧、参考时间与序号回绕、FIFO 边界和既有播放协议。
- 复现主机测试：`python3 firmware/tools/audio_tests/run.py`、`python3 firmware/tools/voice_priority_tests/run.py`。测试编译实际音频/会话/传输源码，用外围设备与 AEC 桩验证时序、队列及所有权；它们不运行真实 Xtensa AEC 推理，也不测量实际回声衰减。后者需要先通过 IDF 解析组件以提供 cJSON 源码。
- 本次没有烧录设备、替换正在运行的桌面应用或调用付费云端 API。需要同时更新固件和 Agent；真实 ADC/DAC 延迟、双人声保留、不同音量与距离的唤醒成功率、AEC CPU/内部 RAM 余量仍需实板验收。

## 自定义唤醒词验证（2026-11-10）

- 新增 `wakePhrase` 配置：留空兼容默认“你好小爱”，支持 2–12 个模型词表可表示的中文汉字。使用本地拼音字典生成带声调的声母／韵母，保存前拒绝不支持的字符和读音；多音字使用字典默认读音，未实现词组消歧。
- `go test ./...` 全量通过。新增回归覆盖默认迁移、中文／音素校验、前缀剔除、仅自定义唤醒词时继续聆听、设置编辑／保存／重载、配置变更重连及桌面提示采用已保存配置。
- 使用本机已安装的真实 sherpa-onnx 模型运行 `TestNativeSmoke` 和 `TestNativeWakePhrase`：自定义“小智同学”的本地 Tingting 合成样本被命中（1 秒前置静音，对齐 1.000–2.040 秒），既有无关语音样本未触发。自定义 detector 加载成功，且没有改写模型目录中的关键词资源。样本不是用户真人或实机麦克风录音，不代表唤醒率／误唤醒率统计。
- 自定义词直接传给识别器，使用现有模型；桌面显示已保存的词。固件待命文案改为通用“语音唤醒，或轻触唤醒”，避免固定提示旧词；LVGL 主机重建及两项 CTest（界面行为、并发字体位图）通过。没有修改用户当前配置，没有替换应用或烧录开发板；使用新功能需要更新运行中的桌面应用。

## 聆听起始丢字修复（2026-11-10）

- 静态排查确认：固件每次进入 `listening` 会播放 200ms 提示音，并在等待 DMA、提示音播放及 150ms 回声尾部期间把麦克风 PCM 置零。正常路径静音窗口约半秒，能直接抹掉唤醒后连说的问题开头；传输序号仍连续，电脑端不会报 `mic_gap`。
- 移除聆听提示音、对应的麦克风静音门控及已无调用的提示音实现，保留角色动画与聆听波形作为就绪反馈。主动静音和正常回复播放继续沿用原流程。修复覆盖首次聆听和仅唤醒词后的重听。
- ESP-IDF 固件构建、Go `go test -mod=readonly ./internal/voice` 及 C 播放协议 ASan/UBSan 回归通过。用户授权后，首次烧录在约 80% 时 USB 断连，随后目标串口无法枚举；电脑重启后恢复同一 ESP32-S3（MAC E8:F6:0A:A5:B7:54）。使用 115200 参数重新构建并烧录，5,757,968 字节 app 完整写入并通过哈希校验，命令退出码 0；未擦除或写入 NVS。
- 烧录后启动日志确认运行新构建（ELF SHA256 前缀 `5f80b7b69`），PSRAM 自检通过，原 Wi-Fi 和 6% 背光设置恢复，获得原 IP `192.168.31.207`，`/voice` 注册成功，HTTP 根页面返回 200。日志见 [烧录后串口记录](../firmware/build/audio-capture-fix-post-flash.log)。尚未用真人麦克风或真实云端 ASR 验证丢字改善；电脑端无需更新。
- 建议实机对照：唤醒词后无停顿连说问题、触摸唤醒后立即说话、仅唤醒词后继续提问，各重复数次比较句首是否完整。句内超过 800ms 的 VAD 非语音仍会触发既有结束条件；若缺的是句尾或句中，需要结合具体缺字位置进一步定位，不能以本次修复替代验证。

## 已通过

| 项目 | 结果和覆盖范围 |
| --- | --- |
| Go 全量测试 | `go test -mod=readonly ./...` 通过，包括原监控/额度、桌面、语音、MiMo、Eino、唤醒资源包 |
| Go 静态检查 | `go vet -mod=readonly ./...` 通过 |
| 语音并发与取消 | `go test -race` 覆盖 desktop、voice、brain、voiceapi、wake、CLI、桌面 UI；取消旧轮次、清空历史、重连、配置重启和工具取消不误断监控连接 |
| MiMo 契约 | 本地 HTTP 假服务验证 ASR input_audio、zh、指定模型、voiceclone assistant 文本、audio.voice WAV data URL、WAV 回包、限流、错误脱敏、取消和大小边界；没有调用真实付费 API |
| Eino Agent | 本地 OpenAI 兼容假服务验证真实 Eino ReAct 工具调用、工具结果、无工具模式、10 轮历史上限、步数与工具并发上限、错误和取消 |
| 跨端协议 | Go WS 假设备验证握手、epoch/turn/seq、额度限流、实际 playing/played 回执、迟到结果与断线；额外修复初始 idle 的显式 turn=0、UTF-8 字幕和设备 4096 字节消息上限 |
| C 播放协议 | Clang ASan/UBSan 下验证无效帧、序号、轮次、sample count、32 帧流控窗口以及连续 20 个模拟轮次完整排空 |
| 资源安装 | 默认用户缓存的模型、Silero VAD、参考 WAV 全部通过固定 SHA-256 校验，安装命令与离线 `-verify` 可用 |
| 原生模型 | 真实模型运行 32 秒静音，不唤醒且正确轮换有界缓存；真实参考 WAV 被 Silero 判为语音 |
| 正负关键词 | macOS 本地 Tingting 合成“你好小爱，帮我看看电脑现在的温度。”与无关句“今天的天气很好，请帮我看看电脑温度。”；真实模型命中前者“你好小爱”（对齐 1.040–1.800 秒，含 1 秒前置静音），未命中后者。样本不是实机麦克风录音，也不代表准确率统计 |
| 无 cgo | wake/voice/brain/voiceapi 测试通过；离线唤醒明确不可用，不替换成伪唤醒或云端待机识别 |
| Windows 兼容 | amd64 与 arm64 的无 cgo 监控/CLI 交叉编译通过；Windows 原生语音安装包与硬件运行未验证 |
| 动画资源 | 9 动作、57 帧均保留，2,334,720 字节 I8 资源逐字节可重复生成；透明边缘检查通过；中文字体 7,541 字符可重复生成 |
| LVGL 真实渲染 | 主机实际 LVGL 软件渲染通过像素断言，能看到中文与人物；测挥手覆盖快到来的聆听状态、失败逐帧后停最后帧、听/说波形、静音重连重置、控件、UTF-8、3 秒恢复、手动切页优先、tick 溢出 |
| 固件构建 | `idf.py build` 通过，产出 [aemeath.bin](../firmware/build/aemeath.bin)，尺寸 0x57da90（5,757,584）字节，6 MiB 应用分区还剩约 8% |
| macOS 分发 | ARM64 应用与 [DMG](../agent/build/voice-fix/darwin-arm64/aemeath-0.1.0-arm64.dmg) 构建通过；sherpa/ONNX dylib 已随包分发，相对 rpath、依赖路径与 `codesign --verify --deep --strict` 校验通过 |

上述“20 轮”是 C 协议模拟，不是 20 轮真实云端+开发板对话。

## 实板烧录补充（2026-11-09）

- 目标经 esptool 确认：ESP32-S3 revision v0.2，16 MB Flash、8 MB PSRAM；USB JTAG 串口 `/dev/cu.usbmodem11101`。
- 执行 `idf.py -p /dev/cu.usbmodem11101 build flash`，仅写入 bootloader、分区表和 app，未整片擦除，也未写入 NVS。最终 app 5,355,520 字节写入并通过 esptool 哈希校验。
- 第一次实板启动发现网络发送队列约 50 KiB 被 FreeRTOS 默认队列分配到紧缺内部 RAM，触发 `ESP_ERR_NO_MEM` 和重启。已改为显式 PSRAM 分配并匹配释放；可选语音服务的注册失败不再中止监控/配网 HTTP 服务。修复版已重新烧录。随后现有电脑端自动连接，暴露出 41,728 字节 PCM 暂存队列挤占 I²S DMA 内存；已同样将非 DMA 暂存队列迁入 PSRAM，真正的 DMA 缓冲/描述符继续使用内部 RAM。代码强制要求非 IRAM-safe 的 I²S ISR（cache 不可用时屏蔽中断），不允许误启用 cache-off 中断配置。最终启动记录见 [串口日志](../firmware/build/post-flash-final.log)，ELF SHA256 前缀 `a522ce1c8`。
- 最终固件已正常启动：PSRAM 自检通过，原 Wi-Fi 与 6% 背光设置保留，获得 IP `192.168.31.207`；监控代理自动重连并恢复 3 个额度账户。HTTP 根页面返回 200。
- 已见 ES8311/ES7210、MIC1/2 和 16k 双向 I²S 成功初始化；初始化后内部 RAM 空闲 43,367 字节，PSRAM 空闲 2,030,508 字节。短时启动观察未见 panic/循环重启；这仍不是连续 20 轮或音质验收。

## 合成偶发失败与中文绘制修复（2026-11-09）

- 用户截图显示识别和 Agent 回复已完成，失败发生于读取 MiMo 响应。旧代码统一 60 秒 HTTP 总超时，并把响应中断、超时等都显示成 `could not read response`；外层 worker 115 秒还会提前截断较慢合成。截图本身不能判定那一次是超时还是上游断流。
- 现按 ASR 60 秒、Agent 90 秒、TTS 180 秒分别计时；总预算覆盖各阶段，取消仍立即传播。错误区分响应头/响应体、超时/截断/连接中断，并带 HTTP 状态、接收字节数，保留失败阶段耗时；错误及日志均不包含密钥、正文或音频。
- 已生成的回复可点“重试回复语音”单独重试 TTS，不重跑 Agent/设备工具；再次失败后文字仍保留。不会自动重发传输结果不确定的付费 POST。
- 全量 Go 测试与 vet 通过；voice/voiceapi 竞态测试覆盖响应体超时、调用方取消、截断、隐私错误、单次关闭和不重放请求。MiMo 适配器另有 9 个聚焦用例，独立执行 `go test -mod=readonly -count=5 ./internal/voiceapi`、`go test -mod=readonly -race ./internal/voiceapi` 与 vet 均通过；慢响应测试实际完成延后返回的 WAV→PCM 传输。桌面 UI 回归验证重试命令只发送现有文字；真实会话测试确认 TTS 拥有完整 180 秒预算且失败耗时保留。
- 当前 LVGL 9.6 的压缩字形 RLE 游标位于全局上下文，双绘制线程会竞争。中文改为未压缩 4bpp 位图，并启用 `CONFIG_LV_FONT_FMT_TXT_LARGE=y` 防止超过 1 MiB 后字形索引截断；保持双绘制线程与现有刷新率。字形 bitmap 增加 335,222 字节；相同字幕/状态不再重复重设标签、分配内存与布局，长字幕按原文比较以兼容省略号处理。固件重新编译和烧录通过哈希校验。
- 单绘制线程及双 pthread 绘制线程下的界面回归均通过；额外 4 线程连续 120,000 次字形解码与串行基准 hash 一致，覆盖超过 1 MiB 的高索引字符，重复 100 次相同长字幕不触发标签写入；[CTest 记录](../firmware/build-avatar-host/font-stability/Testing/Temporary/LastTest.log)可查。
- 修复版 ARM64 桌面应用已构建、验证动态库/签名并替换当前安装版本；用户设置与密钥保留，不随安装包打包。原应用备份在忽略的构建输出目录。
- 实板直接发送本地参考音频完成 3 轮，每轮 32,000 采样（100 帧、约 2 秒），均获得实际 `playing` 和完整 `played` 回执，持续时间约 2.16–2.20 秒；字幕在音频和角色动画运行时持续更新。最终三轮[输出记录](../agent/build/voice-smoke/result.log)和[设备日志](../firmware/build/voice-fix-final.log)可查，验证程序退出码为 0，之后桌面监控代理已重新连接。最终音频初始化后内部 RAM 空闲 42,439 字节、PSRAM 空闲 1,628,620 字节，观察未见 panic 或循环重启。验证后 HTTP 仍返回 200。工具最初因默认参考路径为空未执行播放，已修正；也已按 IDF 直接关闭 socket 的语义处理关闭阶段 EOF。此次没有调用付费云端 API，也没有拍摄 LCD 来判定所有“波动”现象是否消失。

## 仅唤醒词被报为空语音的修复（2026-11-09）

用户截图中的精确错误 `no speech recognized` 来自识别文字剔除“你好小爱”之后为空的分支，尚未调用 Agent 或 TTS。现将仅识别到唤醒词作为正常的继续聆听结果：保持同一轮次，重置 VAD/录音缓冲，提示“我在听，请继续说出问题”；最多重开一次录音。5 秒静音或再次只有唤醒词时正常回到待命，保留取消操作，不自动循环付费识别。

本地 WebSocket/ASR 假服务覆盖继续提问并完成播放、继续静音、重复唤醒词、取消以及初次纯静音共五种场景；验证二次录音不含旧唤醒样本、只有有效问题才调用一次 Agent/TTS，提示进入普通 Notice 而非 Error。语音与桌面 UI 竞态测试、Go 全量测试和 vet 通过。修复仅需更新桌面应用，设备已有 state.text 能显示相同中文提示；未调用真实云端验证识别准确率。

## 缩短默认克隆参考（2026-11-09）

- 本机已配置的参考路径保持不变，音频从 10.624 秒剪到 4.20 秒；选在 4.02–4.76 秒的停顿中，保留约 180ms 尾部静音。完整原版已保存在同目录 `reference-original.wav`。
- 短版 48kHz/24bit 单声道 WAV 为 604,844 字节，SHA256 `de29dbfe3950a2020aeaafb88ad0cb9e9a6c606a983ff546386cd44ced6232bb`。发送前转成 16kHz PCM16 WAV 为 134,444 字节、Base64 179,260 字节，相比旧版 453,352 字节减少约 60.5%。
- 本机三次离线实际参考处理为 37、37、38ms，旧版为 96、97、95ms。此测量只含本地解码/重采样/编码，不代表云端推理同比例加速；没有调用付费云端做时延或音色相似度对照。
- 资源安装器校验源文件后按完整 PCM 帧截取，校验短版后原子替换，保留原始文件；已缓存的旧版可无网络升级。新应用合成/参考试听入口也会对匹配旧校验值的默认样本执行本地升级，自定义音频及显式选取的完整原版保持可用。
- 已用模拟含奇数字节元数据的 WAV 验证截断位置、旧缓存迁移与幂等、完整备份保留、下载损坏拒绝、备份损坏不覆盖、应用不触碰自定义参考。资源与语音竞态测试、完整 Go 测试和 vet 通过；本机资源完整校验通过。

## LLM 分段合成与播放（2026-11-09）

- 接入真实 Eino/OpenAI SSE，纯聊天一次流式请求；工具模式用真实 ReAct 选择/执行工具，再以无工具流式请求产生最终正文。伪服务器证明工具只执行一次，tool args/reasoning/中间旁白不进朗读回调，取消、Clear、回调失败、错误流不提交不完整历史、不重放工具。
- 中文句子切分保留 UTF-8、字节顺序、引号、金额和版本小数。首段短、后续句组较大；总输入上限 16KiB，每轮最多 8 次 TTS，最终余文不丢。
- 同一逻辑对话内采用独立递增的 v1 wire turn 播放每一段，收到该段实际 `played` 才推进。原板固件无需变更。假设备测试证明首段音频在 LLM 尚未 EOF 时已经开始发送，迟到旧段 ACK 不影响后段，段间取消、下一轮取消隔离、最后一段播完才收到 generation_done 均正确收尾。
- 独立流水线故障回归在竞态检测下重复 20 次，覆盖部分 LLM 流失败不重试、TTS 失败/父取消/下游停止均取消并等待生产者、总 PCM 恰好 120 秒及超限拒绝、8 段保留全文。完整 Go 测试、brain/voice/桌面 UI 竞态检查及 vet 通过。
- 显式硬件检查 `AEMEATH_HW_ADDRESS=192.168.31.207 go test -mod=readonly ./internal/voice -run "^TestHardwareSegmentedPlayback$" -count=1 -v` 通过，耗时 9.60 秒：同一逻辑轮次实际排空两段，每段 67,200 样本（4.20 秒）。第二段文字故意等待第一段排空后才释放，验证跨段等待。云端三环节均为本地假服务，设备返回真实 playing/played；182ms 首段渲染只代表本地假服务通路，不代表真实 LLM/MiMo 云端时延，未产生付费 API 调用。
- 桌面以实际首次 playing 回执显示首段耗时，并展示已合成/播放完成段数；当前仍用短版参考和 MiMo voiceclone。每段需要独立 MiMo 请求，首段等待可减少，但总耗时/费用未必同比下降；没有进行真实云端速度或音色衔接对照。

## Fish 双向流式与 reference_id（2026-11-09）

- 新增可选 Fish TTS：默认 `s2.1-pro` / `balanced`，原 MiMo 仍为默认。桌面和 CLI 支持 Key、模型、`reference_id`、参考逐字文本和延迟设置。ID 非空时不读取本地音频，已用不存在的参考路径验证优先级；纯文字 Fish 试听不依赖 MiMo Key。密码输入/无明文可访问性、保存重载、草稿保持、旧配置默认和环境变量优先级已通过测试。沿用现有本地 JSON 存储机制，没有新增密钥迁移。
- Fish MessagePack WebSocket 本地 TLS 模拟服务覆盖真实双向传输：首个音频早于文字 EOF/finish，start/text/flush/stop、auth/model、奇数字节 PCM 拼接、显式成功 finish、提前 EOF/错误拒绝、取消/回调失败的连接关闭与生产者退出，以及总样本/文字/消息/畸形长度限制。竞态重复 10 次通过；独立审查发现并修复 writer 完成通知晚于取消的错误根因竞态，1000 次文字超限请求保持精确根因、没有重放。
- Go 与 ESP32 增加可协商的未知长度播放；首音到达才发 `samples:0,streaming:true`，末尾发精确总量，单一 wire turn。回归验证 320/320/1 帧、暂停不结束、实际 playing/played、缓冲压缩后 seq>0 的首音识别、有界 2s PCM+16 条消息背压，以及取消/静音/清空/新轮次仍能及时处理和清除旧音频。缺失固件能力在云请求前拒绝。
- 固件协议 Clang ASan/UBSan、完整 ESP-IDF 构建、Go 全套测试/vet 和语音/桌面/CLI 竞态检查通过。新固件 5,757,984 字节已烧录并校验，保留 NVS。实板本地假 Fish 通过正常排空与中途取消：正常一条流播放总计 67,200 样本，故意在第一秒音频后等待 1.5 秒以制造欠载，未提前完成；取消时实际已开始播放且没有错误报告已播完。原固定长度 MiMo 分段两段播放仍通过。实板日志为 `agent/build/fish-hardware.log`。
- 首轮实板测试完成后主动取消上下文时，发送错误被泛化为网络失败；现已保留父上下文取消原因，重新验证正常流 5.57s、取消 1.04s、旧分段 9.60s 均通过。假云端的首渲染 178/179ms 不代表真实 Fish 云端延迟。没有实际 Fish API Key/reference_id，因此未声称真实音色合成成功、音色相似度或线上延迟已验证；没有产生付费云调用。

## 电脑锁屏 Function Call（2026-11-10）

- 新增无参数 `lock_computer`，接通桌面和 CLI Host，注册到真实 Eino 工具链；屏幕亮度工具描述明确指向 ESP32。锁屏请求成功/系统拒绝均以结构化 ok 结果送回最终模型，取消及参数错误保持错误返回，不输出原始系统诊断。
- macOS 当前主机没有 CGSession，login.framework 的 SACLockScreenImmediate 可从 dyld shared cache 动态解析（仅探测未调用），按 Chromium 记录的 OSStatus 签名检查请求结果；旧 CGSession 存在时使用静态 argv。Windows 检查 user32 LockWorkStation 返回值。无 cgo 的 macOS 在没有 CGSession 时明确返回 unavailable；其他平台返回 unsupported。
- 原生 macOS 竞态、锁屏包 CGO0 测试和 Windows/Linux 锁屏包测试二进制交叉编译通过。脑层 CGO0 测试也通过；完整 macOS 无 cgo CLI 检查遇到原有 collect/newSensors 缺少实现，不影响已通过的原生语音版构建。模拟系统请求覆盖取消前不执行、失败结果、清理、时间预算和固定命令；Eino SSE 端到端测试验证锁屏只调用一次、成功/失败结果完整进入模型上下文且最终正文才朗读。桌面与 CLI 的已取消调用不需要设备连接。完整 Go 测试/vet 通过。
- 没有实际锁定用户电脑，没有调用真实 LLM 或云端 TTS；Windows/Linux 仅交叉编译，未声称运行时验证成功。

## 视觉结果

[角色状态总览](../firmware/build-avatar-host/avatar-lvgl-preview.png) 来自主机实际 LVGL 快照（每个状态原生 480×480），不是物理 LCD 拍摄。生成方法见 [主机验证说明](../firmware/tools/avatar_tests/README.md)。

## 尚需真实环境验收

用户随后授权烧录，已向实板写入并通过哈希校验；没有使用真实 MiMo/LLM Key 发起识别或声音复刻请求，因此以下项目仍没有宣称通过：

- 实板 ES7210 双麦克风信号、I²S 时钟/声道、ES8311 音量及功放噪声；
- LCD 刷新+Wi-Fi+音频同时运行时的 DMA heap、PSRAM、CPU、丢帧、欠载与连续 20 轮稳定性；
- 不同说话人、距离、背景噪声下的唤醒准确率、误唤醒率和关键词后首字完整性；
- 实际账号对 `mimo-v2.5-asr`、`mimo-v2.5-tts-voiceclone` 的权限、额度、限速、复刻相似度与端到端时延；
- 实物触摸、屏幕临时唤醒与手动操作优先级；
- macOS Intel/Universal 实际运行、Windows cgo 语音版编译/签名/安装、Windows 真机运行。

代码与构建产物已经具备这些验收的入口。使用步骤与限制见 [使用说明](voice-agent.md)。

## 复现命令

```bash
# Go（在 agent 目录）
go test -mod=readonly ./...
go vet -mod=readonly ./...
go test -mod=readonly -race ./internal/desktop ./internal/voice ./internal/brain ./internal/voiceapi ./internal/wake ./cmd/aemeath-agent ./cmd/aemeath-desktop
CGO_ENABLED=0 go test -mod=readonly ./internal/wake ./internal/voice ./internal/brain ./internal/voiceapi
go run ./cmd/aemeath-voice-assets -verify
AEMEATH_WAKE_SMOKE=1 go test ./internal/wake -run TestNativeSmoke -v

# 可选：提供自有 16k/其他支持格式 WAV，完全离线测试关键词正负样本
AEMEATH_WAKE_POSITIVE_WAV=/absolute/target.wav AEMEATH_WAKE_NEGATIVE_WAV=/absolute/unrelated.wav \
  go test ./internal/wake -run TestNativeWakePhrase -v

# C 播放协议（在 firmware 目录）
clang -Wall -Wextra -Werror -fsanitize=address,undefined -I main/voice \
  tests/voice_protocol_test.c main/voice/protocol.c -o build/voice_protocol_tests
./build/voice_protocol_tests

# IDF 固件（已加载 IDF 6.0.2）
idf.py build
```

角色图片仍需 `CONFIG_LV_BIN_DECODER_RAM_LOAD=y`。中文字体现使用未压缩 4bpp 位图，避免 LVGL 9.6 的全局 RLE 解码上下文被两个绘制线程同时改写；不再依赖 `CONFIG_LV_USE_FONT_COMPRESSED=y` 来显示中文。旧的压缩开关可以保留以兼容其他字体，但不要将语音中文字体重新生成成压缩格式。仅 C 编译成功并不能证明图像、文字和并行绘制正确。

仍有上游 Kconfig bool 默认值提示和 LVGL 旧 API 弃用警告，但不会阻断当前构建。macOS 链接器会提示重复的相对 rpath 已忽略，最终校验确认没有开发机模块缓存依赖。
