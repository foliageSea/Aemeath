# 爱弥斯语音助手使用说明

## 运行结构

```text
ESP32 双麦克风 → ES7210 → 16k PCM → /voice WebSocket → 电脑本地 KWS + VAD
                                                        ↓ 唤醒后问题
                                    MiMo ASR → Eino / OpenAI兼容 LLM
                                                        ↓ 回复文字
ESP32 扬声器 ← ES8311 ← 16k PCM ← MiMo 分句 WAV / Fish 双向音频流
      └── LVGL 爱弥斯 Q 版形象随会话状态变化
```

电脑仍主动连接设备；原 `/ws` 性能与额度通道独立运行。唤醒词为**你好，小爱**，标点不参与语音关键词。采用电脑离线识别，不调用云端 ASR 识别待机音频；只有唤醒后的一轮问题发往 MiMo。声音播放期间暂停唤醒检测，避免回声自唤醒，首版不支持免唤醒连续聊天或语音打断，触屏和桌面端都可取消。

## 1. 安装资源

在仓库根目录执行：

```bash
cd agent
go run ./cmd/aemeath-voice-assets
go run ./cmd/aemeath-voice-assets -verify
```

安装命令显式联网下载固定版本的中文关键词模型、Silero VAD 和参考 WAV；每个文件校验编译进程序的 SHA-256，拒绝不符的内容，不修改桌面设置。日常启动和 `-verify` 不下载。受损目录不会被静默替换，可以用 `-dir /新的绝对路径` 重新安装；随后在桌面语音页设置模型目录 `<dir>/models`、参考音频 `<dir>/reference.wav`。

默认目录是 `os.UserCacheDir()/Aemeath/voice`：macOS 通常为 `~/Library/Caches/Aemeath/voice`，Windows 通常为 `%LOCALAPPDATA%/Aemeath/voice`。留空的配置路径会使用默认值。

离线关键词使用 `sherpa-onnx-kws-zipformer-wenetspeech-3.3M-2024-01-01`，官方 sherpa-onnx Go/native 版本 2.0.1，仅配置“你好小爱”。依赖原生 cgo；无 cgo 的监控版会明确显示离线唤醒不可用，不会偷偷改用云端待机识别。

## 2. 更新固件

```bash
. ~/.espressif/v6.0.2/esp-idf/export.sh
cd firmware
idf.py build
# 确认串口确实对应这块开发板，再执行烧录：
idf.py -p /dev/cu.usbmodem11101 flash monitor
```

新的语音通道需要新固件；旧固件不具备音频能力，不能只更新桌面端。代码会协商版本与 PCM 格式，握手失败时显示语音连接错误。首次 Wi-Fi 配网方式及长按 BOOT 清除 Wi-Fi 的行为不变。

板载 I²C 使用 GPIO47/48；I²S MCLK5、BCLK16、LRCLK7、DOUT6、DIN15，功放由 TCA9554 EXIO3 控制。ES7210 两路麦克风以 16 kHz/16-bit 标准双槽采集后下混，ES8311 在同一时钟格式输出。不能把右槽误作 AEC 参考；四通道 TDM 与回声抵消不在首版中。

## 3. 桌面配置

在 MyGO 桌面应用左侧打开“语音助手”：

1. 开启语音助手，填写 OpenAI 兼容 API 的 Base URL、模型与 API Key。Base URL 通常以 `/v1` 结尾；接本地无鉴权兼容服务时可以留空 LLM Key。
2. 若启用工具，所选模型必须支持 tool calling；不支持时关闭工具模式使用纯对话。
3. 填写 MiMo API Key。默认 MiMo Base URL 为 `https://api.xiaomimimo.com/v1`；它与文字 LLM 可以使用完全不同的服务商。
4. 资源安装在默认位置时，无需改动模型、参考音频路径。默认音量为 70，0 是有意静音。灵敏度对应关键词阈值，越低越容易触发，也可能增加误唤醒；推荐先保持 0.25 并实测。
5. 点击保存。先“播放参考音频”检查设备扬声器，再“试听语音”检查 MiMo 复刻权限，最后测试唤醒和完整对话。

设置先保存才生效，刷新不会覆盖未保存表单。API Key 只保存在电脑的现有私有设置文件中，权限 `0600`（Windows 实际访问控制仍由用户目录 ACL 决定）；界面遮罩，错误日志不输出 Key。不要把包含 Key 的设置文件提交到 Git 或分享。

未安装 KWS/VAD 时显示“离线唤醒未就绪”；仍可手动唤醒和试听用于排障。缺少 VAD 的手动录音采用明确提示的 6 秒固定窗口，不代表自动唤醒已经就绪。

## 4. 命令行

参考配置为 [voice.example.json](../agent/voice.example.json)。创建自己的私有副本，填写模型；密钥推荐从环境传入。`voice.local.json` 已加入忽略规则。

```bash
cd agent
# 复制示例并编辑你自己的 voice.local.json，避免提交真实 Key。
export AEMEATH_LLM_API_KEY="你的文字模型 Key"
export AEMEATH_LLM_MODEL="你的模型名"
export MIMO_API_KEY="你的 MiMo Key"
CGO_ENABLED=1 go run ./cmd/aemeath-agent -voice-config voice.local.json -addr aemeath.local:80
```

也可仅用 `-voice` 启用并从环境提供模型参数；无 `-voice` 和 `-voice-config` 时保留原监控行为。支持环境变量：

| 环境变量 | 含义 |
| --- | --- |
| `AEMEATH_LLM_BASE_URL` | 文字模型兼容 API 根地址 |
| `AEMEATH_LLM_API_KEY` | 文字模型密钥，覆盖 JSON |
| `AEMEATH_LLM_MODEL` | 文字模型名 |
| `MIMO_BASE_URL` | MiMo 根地址 |
| `MIMO_API_KEY` | MiMo 密钥，覆盖 JSON |

终端中输入 `wake` 手动唤醒、`cancel` 取消、`clear` 清空上下文、`preview 你好，我是爱弥斯。` 合成试听、`reference` 播放参考音频。Ctrl+C 退出并取消请求。桌面版与 CLI 不要同时占用同一台设备；音频通道只允许一个拥有者。

## 5. 对话与动画

- 说“你好，小爱”后角色页立即挥手；随后播放聆听 `waiting`、识别/推理 `review`、合成/播报 `running`，错误 `failed`，完成 `idle`。
- 可连续说“你好，小爱，帮我看看 CPU”；2 秒预缓冲按关键词末端截取，识别文字中的句首唤醒词会被剔除。如果识别到的只有唤醒词，会正常提示“我在听，请继续说出问题”并重新聆听一次；5 秒内没有问题则回到待命，不作为故障。重复唤醒词最多再识别一次，不会无限重试或调用 Agent/TTS。模型对齐点并非精确音素边界，应在实际说话速度下验证是否截掉首字。
- 约 800 ms 尾部静音提交问题，5 秒无问题退出，最长一轮录音 30 秒。识别、Agent、合成分别最多等待 60、90、180 秒，取消随时生效；播放音频最多 120 秒。合成单独计时，不再被较早阶段耗时或旧的 60 秒 HTTP 总超时截断。
- LLM 最终正文采用流式接收，首个完整短句立即开始 MiMo 合成；后续按较大的句组顺序合成，生成与播放重叠。每轮最多 8 段，整轮合成共用从首段开始计时的 180 秒预算，总音频仍最多 120 秒。需要兼容 API 支持 SSE 流式聊天；流失败不会自动重放工具或整轮请求。
- 音频真正开始输出后才显示说话，播放能量驱动声波；不能把网络收到音频误作扬声器已经播放完成。桌面额外显示首段播放耗时和合成/排空段数：首段耗时从提交识别音频起计，结束于设备首次实际 `playing` 回执，不包含录音等待。
- 结束后角色页停留 3 秒再恢复原页面。临时亮屏不覆盖持久亮度设置；手动切页或屏幕操作优先于自动恢复。
- 只保留最近 10 轮完整文字上下文，清空或重启会话后不保留历史录音。

Agent 工具包括 `get_metrics`、`get_quota`、`set_page`、`set_screen` 和 `lock_computer`；不包含任意 shell 或文件执行。启用“允许查询监控与控制设备”后，可以说“你好小爱，帮我锁屏”或“锁定这台电脑”。`lock_computer` 无参数，作用于运行 Agent 的电脑当前登录会话；`set_screen` 仍控制 ESP32 监控台屏幕。锁屏工具仅在用户明确要求电脑锁屏时使用，返回结构化的请求提交成功/失败结果，不把请求提交当作已验证最终锁屏状态。macOS 使用系统原生锁屏接口（旧系统可用 CGSession），Windows 使用 LockWorkStation；其他平台明确返回不支持。开启工具时先用独立的短工具选择请求完成 ReAct 工具调用，再以禁止工具的流式请求生成最终正文；选择过程、推理内容和工具参数不会朗读。这会比无工具模式多一次选择请求；无工具模式直接使用单次 SSE。切页协议没有设备页面 ACK，工具会说明指令已发送，而非声称已确认切页。

## 6. MiMo 和参考音频

ASR 保持为 `mimo-v2.5-asr`。TTS 可在桌面设置选择 MiMo 或 Fish；默认仍是 MiMo，模型为 `mimo-v2.5-tts-voiceclone`。两者使用 `/chat/completions`，不是 `/audio/speech` 或 `/audio/transcriptions`。克隆合成将目标文字放 assistant 消息，参考 WAV 转 Base64 data URL 放 `audio.voice`。不上传网页地址，不需要虚构“训练音色 ID”。

**此 voiceclone 模型目前不是低延迟逐块生成。**每个句组仍使用 `stream:false` 接收完整 WAV，随后转换成 16 kHz PCM 发给设备；首段无需等待全文或后续句组生成完成。分段会增加合成请求数、重复发送参考音频的量和可能的计费开销，后续句组因此合并得更大，并限制最多 8 段。试听/重试已生成文字的入口仍为单段合成，不重新运行 LLM。等待时角色展示合成动画；不对首包延迟作实时承诺。若请求超时或响应中断，界面显示具体阶段、错误类别和实际耗时。已经获得文字回复时，可点“重试回复语音”只重新合成这条文字；不会重复运行 Agent 工具，也不会自动重发可能已经计费的请求。

默认素材来自用户提供的[爱弥斯 Wiki](https://wiki.kurobbs.com/mc/item/1457744312692867072?wkFrom=catalog) 中的[中文“自我介绍”](https://web-static.kurobbs.com/wiki_audio/1770098269166323983.wav)，原始录音为 10.624 秒、48 kHz、24-bit、单声道。默认参考现截到 4.20 秒，结束点位于 4.02–4.76 秒的停顿中；安装器保留完整原版为 `reference-original.wav`，短版仍使用 `reference.wav`。新版应用会在使用默认参考前本地升级与旧校验值完全匹配的长版，不下载文件、不更改其他自定义音频。使用时重采样为 16 kHz PCM16 WAV；每次参考 Base64 约 179 KB，比原版 453 KB 减少约 60%。可换成自己的清晰、单人 WAV；支持 PCM8/16/24/32 和 float32 WAV，MP3 请先自行转成 WAV。素材和角色权利归其权利人，分发、商业用途和声音复刻需自行确认授权，不能把公开可读取等同于版权授权。

官方接口说明：[MiMo ASR](https://mimo.mi.com/static/docs/quick-start/usage-guide/audio/Speech-Recognition.md)、[MiMo 语音合成](https://mimo.mi.com/docs/zh-CN/quick-start/usage-guide/audio/speech-synthesis-v2.5)、[Eino ReAct](https://www.cloudwego.cn/docs/eino/core_modules/flow_integration_components/react_agent_manual/)。

## 7. Fish 双向流式与 reference_id

在桌面“语音助手”中将合成提供方切换为 **Fish**，填写 **Fish API Key** 和 **Fish reference_id**，保存后可用“试听语音”验证。默认根地址为 `https://api.fish.audio`、模型 `s2.1-pro`、延迟模式 `balanced`；可选 `normal`、`balanced`、`low`。API Key 使用密码输入框，保存方式与现有 MiMo/LLM 设置一致。

`reference_id` 优先于本地参考路径和参考文字。填写 ID 后不会读取/上传本地参考音频；未填写时，必须提供参考 WAV 和逐字对应的转写文字，二者在同一开始消息中发送。不自动创建、上传持久音色模型，也不会在 Fish 失败后自动切换 MiMo 或重放文本。

CLI 配置新增字段示例，其余 LLM/ASR 配置沿用原值：

```json
{
  "ttsProvider": "fish",
  "fishBaseURL": "https://api.fish.audio",
  "fishAPIKey": "填入你的 Fish API Key",
  "fishModel": "s2.1-pro",
  "fishReferenceID": "填入你的 reference_id",
  "fishLatency": "balanced"
}
```

环境变量覆盖支持 `AEMEATH_TTS_PROVIDER`、`FISH_API_KEY`、`FISH_BASE_URL`、`FISH_MODEL`、`FISH_REFERENCE_ID`、`FISH_REFERENCE_TEXT`、`FISH_LATENCY`。语音问题识别仍使用 MiMo ASR；Fish 的纯文字试听不要求 MiMo 密钥。

Fish 使用 `/v1/tts/live` WebSocket + MessagePack，同一会话逐段发送 LLM 文字并同步接收 16kHz/16bit/单声道 PCM。设备在首批 PCM 到达时开始未知长度播放；云端成功结束后才声明总采样数，实际 DMA 排空才确认播完。电脑缓冲为 2 秒 PCM 加有限消息队列，声卡信用不足会向 Fish 施加背压；取消同时终止 LLM、Fish 和待播音频。整轮总音频上限 120 秒，生成预算从首段文字开始为 180 秒，流式空闲等待上限 30 秒。需要广告 `stream_playback` 能力的新固件；旧固件会在云端请求前明确报错。

官方协议：[Fish WebSocket TTS](https://docs.fish.audio/api-reference/endpoint/websocket/tts-live)、[Fish 能力说明](https://docs.fish.audio/overview/capabilities)。真实账号权限、音色存在性及首音速度须在填写自己的 Key/ID 后验证；本地测试不调用付费云端。

## 8. 构建与诊断

macOS 语音版使用 [build-dmg.sh](../agent/scripts/build-dmg.sh)，自动暂存对应架构的 sherpa/ONNX Runtime 动态库，在签名前设置相对查找路径；可用 [verify-macos-voice.sh](../agent/scripts/verify-macos-voice.sh) 校验发布包。

```bash
bash agent/scripts/build-dmg.sh darwin/arm64 -o build/voice-validation
bash agent/scripts/verify-macos-voice.sh agent/build/voice-validation/darwin-arm64/Aemeath.app
```

Windows 监控版继续使用 [build-windows.sh](../agent/scripts/build-windows.sh)，不需要 cgo；语音版使用 [build-windows-voice.sh](../agent/scripts/build-windows-voice.sh)，额外要求目标架构的 MinGW-w64/llvm-mingw C 编译器与 NSIS，脚本会打包 DLL。构建成功与 Windows 实际硬件运行是两种不同验证。

```bash
CC=x86_64-w64-mingw32-gcc bash agent/scripts/build-windows-voice.sh windows/amd64
```

排障顺序：设备 `/ws` 连通 → `/voice` 握手 → 参考音频播放 → MiMo TTS 试听 → 模型就绪 → 唤醒 → ASR → Agent。声音小先检查桌面音量与设备静音；语音错误不会停止性能/额度采样。资源损坏先执行 `-verify`，再用新目录安装。

**局域网信任边界：**首版设备通道是明文 WebSocket，没有设备配对鉴权。仅在可信私有 LAN 使用，不要端口映射到公网；同网段其他程序可能占用语音通道或请求采集。需要跨网、多人环境或商用时，应先增加设备配对/令牌及加密传输。

## 验证范围

自动测试使用本地 HTTP/WS 模拟服务，不消耗 MiMo/LLM 额度；它们验证请求结构、Agent 工具循环、取消、流控和恢复，不证明真实云端账号权限、音色相似度或硬件声学效果。详细实测记录见 [voice-validation.md](voice-validation.md)。
