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

电脑仍主动连接设备；原 `/ws` 性能与额度通道独立运行。默认唤醒词为**你好小爱**，可在“设置 → 语音 → 离线唤醒”的“唤醒词”中修改。采用电脑离线识别，不调用云端 ASR 识别待机音频；只有唤醒后的一轮问题发往 MiMo。对话期间持续进行离线唤醒检测：录音、识别、思考、合成或播放时再次说出已保存的唤醒词，会取消当前对话、停止请求和播放并回到待命，不直接开启下一轮；待命时再说唤醒词才开始新对话。触屏和桌面端也可取消；静音或离线唤醒未就绪时使用按钮取消。未启用免唤醒连续聊天。

## 1. 安装资源

桌面应用中打开“设置 → 语音 → 离线唤醒”，在“离线唤醒模型”卡片点击“安装唤醒模型”即可下载安装中文唤醒与 Silero VAD 模型（约 33 MB），无需终端。界面会显示下载进度，支持取消和失败后重试；已安装的模型仅做本地校验，不重复下载。模型目录默认收在“模型目录与高级选项”中，安装使用当前选定的目录。已有文件校验失败时不会覆盖，请检查文件或更换一个尚不存在的目录。

安装不会自动保存其他设置。若安装目录正用于当前已启用的语音连接，安装成功后会重连并加载模型；修改了目录时，保存设置后生效。唤醒模型不包含参考音频；需要默认参考 WAV 或完整命令行安装时，在仓库根目录执行：

```bash
cd agent
go run ./cmd/aemeath-voice-assets
go run ./cmd/aemeath-voice-assets -verify
```

安装命令显式联网下载固定版本的中文关键词模型、Silero VAD 和参考 WAV；每个文件校验编译进程序的 SHA-256，拒绝不符的内容，不修改桌面设置。日常启动和 `-verify` 不下载。受损目录不会被静默替换，可以用 `-dir /新的绝对路径` 重新安装；随后在桌面语音页设置模型目录 `<dir>/models`、参考音频 `<dir>/reference.wav`。

默认目录是 `os.UserCacheDir()/Aemeath/voice`：macOS 通常为 `~/Library/Caches/Aemeath/voice`，Windows 通常为 `%LOCALAPPDATA%/Aemeath/voice`。留空的配置路径会使用默认值。

离线关键词使用 `sherpa-onnx-kws-zipformer-wenetspeech-3.3M-2024-01-01`，官方 sherpa-onnx Go/native 版本 2.0.1，按已保存的唤醒词自动生成拼音关键词，无需手写拼音或重新下载模型。唤醒词会去掉首尾空白；留空使用“你好小爱”，自定义词支持 2–12 个模型支持的中文汉字，不支持标点、数字、拉丁字母或词内空格。多音字采用拼音库的默认读音，可能与预期不同，建议换用读音明确的词并实际试唤醒。依赖原生 cgo；无 cgo 的监控版会明确显示离线唤醒不可用，不会偷偷改用云端待机识别。

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

在 MyGO 桌面应用打开“设置 → 语音”（也可从“语音助手”页点击“语音设置”）。左侧分为“基础设置、对话服务、智能家居、语音识别、语音合成、离线唤醒、声音试听”七组，右侧显示对应表单；切换分组保留草稿和各组滚动位置，顶部“保存设置”统一保存所有分组的修改：

1. 在“基础设置”开启语音助手、设置角色偏好，再在“对话服务”填写 OpenAI 兼容 API 的 Base URL、模型与 API Key。Base URL 通常以 `/v1` 结尾；接本地无鉴权兼容服务时可以留空 LLM Key。
2. 在“对话服务”默认开启“自动读取模型上下文容量”，并填写“备用上下文容量（Tokens）”。JSON 的 `contextWindowMode` 默认 `auto`，仅支持 `auto` / `manual`；关闭开关使用手动模式，数值标签改为“手动上下文容量（Tokens）”。`contextWindow` 在自动模式中是备用值，在手动模式中是上限，0 或省略默认 32768（32K），手填范围 8192–2097152。保存设置后会重新连接并应用模式和数值。若启用工具，所选模型必须支持 tool calling；不支持时关闭工具模式使用纯对话。
3. 在“语音识别”填写 MiMo API Key；此组的地址和密钥也供 MiMo 语音合成使用。在“语音合成”选择 MiMo/Fish 并配置音色、参考音频。默认 MiMo Base URL 为 `https://api.xiaomimimo.com/v1`；它与文字 LLM 可以使用完全不同的服务商。
4. 在“离线唤醒”填写“唤醒词”，留空使用默认“你好小爱”。资源安装在默认位置时，无需改动模型、参考音频路径。默认音量为 70，0 是有意静音。灵敏度对应关键词阈值，越低越容易触发，也可能增加误唤醒；推荐先保持 0.25 并实测。
5. 点击顶部“保存设置”，然后在“声音试听”调整音量。先“播放参考音频”检查设备扬声器，再“试听语音”检查合成服务，最后测试唤醒和完整对话。

设置先保存才生效，刷新不会覆盖未保存表单。修改唤醒词后点击顶部“保存设置”，现有配置更新机制会停止当前连接尝试并重新连接设备，语音会话随之重新启动并加载新词，无需退出重开桌面应用；当前对话可能中断。语音助手页的唤醒提示使用已保存的词，未保存的草稿不会改变提示。API Key 只保存在电脑的现有私有设置文件中，权限 `0600`（Windows 实际访问控制仍由用户目录 ACL 决定）；界面遮罩，错误日志不输出 Key。不要把包含 Key 的设置文件提交到 Git 或分享。

未安装 KWS/VAD 时显示“离线唤醒未就绪”；仍可手动唤醒和试听用于排障。缺少 VAD 的手动录音采用明确提示的 6 秒固定窗口，不代表自动唤醒已经就绪。

### 按住快捷键说话

Aemeath 启动后注册 **F8 全局快捷键**：无论当前打开哪个页面、正在使用其他应用，还是已关闭窗口驻留托盘，都可以按住 **F8**，对着 **ESP32 设备麦克风**说话；松开 F8 后，录音通过现有 MiMo ASR 识别并直接发送给 Agent，随后由设备播报回复。部分 Mac 键盘需按住 **Fn + F8** 才会发送 F8。若 F8 已被占用，自动改用 **Ctrl + Shift + F8**（Mac 上是 Control，不是 Command）；“语音助手”页会显示实际生效的快捷键，下文的 F8 操作对应这个已显示的组合。应用退出后释放快捷键。若两个组合均注册失败，“语音助手”页会显示错误；关闭冲突应用后重新启动 Aemeath。

- 需要启用语音助手，且设备语音通道已连接。无需先说唤醒词，也不要求离线唤醒模型就绪。
- 按住期间停顿不会提前结束录音，单次最长 30 秒；到达上限会自动结束并识别。
- 切换页面、切到其他应用或关闭窗口驻留托盘时，录音会持续到松开 F8。Aemeath 窗口在前台时可按 **Esc** 或点击“取消对话”丢弃尚未提交的录音；退出应用也会丢弃录音。Esc 不注册为全局快捷键。
- 按下 F8 会打断当前对话或播报并开始新一轮录音。设备麦克风静音时无法开始录音。
- 原有“手动唤醒”按钮仍按自动断句方式工作；缺少 VAD 时该按钮采用 6 秒固定窗口，F8 按住录音由松键控制结束。

### 智能家居：Home Assistant MCP

桌面 Agent 从本机通过 Streamable HTTP 连接 Home Assistant（HA）官方 MCP Server；运行桌面的电脑必须能访问 HA，接入 HA 无需更改 ESP32 固件。HA MCP 默认关闭，需要同时在“对话服务”启用“启用 Agent 工具”，对话模型支持 tool calling 后才能在语音对话中调用。

1. 在 Home Assistant 中启用 **Model Context Protocol Server** 集成，并选择允许 Assist 暴露的实体，具体步骤见 [HA 官方 MCP Server 文档](https://www.home-assistant.io/integrations/mcp_server/)。
2. 复制集成提供的 **Device control** 地址，默认是 `http://homeassistant.local:8123/api/mcp/assist`；将主机名替换为桌面能访问的 HA 地址。
3. 在 HA 的个人资料（Profile）中创建长期访问令牌（Long-Lived Access Token），复制到桌面“设置 → 语音 → 智能家居”的“长期访问令牌”。首版手动填写令牌，通过 `Authorization: Bearer <token>` 请求头认证，没有浏览器 OAuth 登录流程。
4. 在“智能家居”开启“启用 HA MCP”，填写“服务地址”和令牌。令牌默认遮罩，可用眼睛按钮显示/隐藏；点击“测试连接与刷新工具清单”检查连接和当前可用工具，然后点击顶部“保存设置”。
5. 保存并确认 Agent 工具已开启后，可以说“你好小爱，打开客厅灯”或“你好小爱，客厅温度是多少？”。可操作设备取决于 HA 实际返回的工具和权限。

默认 Device control 地址使用 HA 的 Assist 暴露实体范围。也允许填写完整 `/api/mcp` 地址或自定义工具集 URL；这些地址的可访问工具、实体和权限以 HA 服务端实际配置及令牌权限为准，不能假定仍受 Assist 暴露列表限制。

工具从 HA 动态发现，在模型中使用 `ha_` 名称前缀。每轮对话刷新工具清单，新增或移除的工具会随之更新；HA 离线或发现失败不会阻断现有的七个 Agent 工具，下一轮对话或再次点击测试可重连。发现预算为 8 秒，单次工具调用预算为 20 秒；取消对话或更短的父请求期限会提前终止。客户端不会自动重放 `tools/call` 动作。HA 工具沿用现有语音对话的执行过程和结果卡片，显示标题、状态、耗时、参数及结果。

HA 配置与其他语音设置一起持久化到本机私有设置文件。令牌只用于桌面到 HA 的认证，不发送给 LLM，不放在 URL、工具结果或日志中；不要分享含令牌的配置副本。

## 4. 命令行

参考配置为 [voice.example.json](../agent/voice.example.json)，示例使用 `"contextWindowMode": "auto"` 和 `"contextWindow": 32768`；后者是自动读取失败时的备用值。标准接口不一定提供容量，自动失败不阻断对话；改成 `"manual"` 可始终使用手填上限。创建自己的私有副本，填写模型；密钥推荐从环境传入。`voice.local.json` 已加入忽略规则。

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
| `TAVILY_API_KEY` | Tavily 网页搜索密钥，覆盖 JSON 的 `tavilyAPIKey` |
| `HA_MCP_ENABLED` | `true` / `false`，覆盖 JSON 的 `haMCPEnabled`（默认 `false`） |
| `HA_MCP_URL` | HA MCP Streamable HTTP 地址，覆盖 JSON 的 `haMCPURL`；默认 `http://homeassistant.local:8123/api/mcp/assist` |
| `HA_ACCESS_TOKEN` | HA 用户长期访问令牌，覆盖 JSON 的 `haAccessToken`（默认空） |

终端中输入 `wake` 手动唤醒、`cancel` 取消、`clear` 清空上下文、`preview 你好，我是爱弥斯。` 合成试听、`reference` 播放参考音频。Ctrl+C 退出并取消请求。桌面版与 CLI 不要同时占用同一台设备；音频通道只允许一个拥有者。

## 5. 对话与动画

内置爱弥斯设定包含人物背景、性格、称呼与朗读习惯，资料依据和改编示例见[角色设定说明](aemeath-persona.md)。角色设定留空使用内置版本；已保存的非空设定继续优先使用，升级后可清空并保存以采用新版。

- 说“你好，小爱”后角色页立即挥手；随后播放聆听 `waiting`、识别/推理 `review`、合成/播报 `running`，错误 `failed`，完成 `idle`。
- 可连续说“你好，小爱，帮我看看 CPU”；2 秒预缓冲按关键词末端截取，识别文字中的句首唤醒词会被剔除。如果识别到的只有唤醒词，会正常提示“我在听，请继续说出问题”并重新聆听一次；5 秒内没有问题则回到待命，不作为故障。对话期间离线再次命中唤醒词时直接取消，不会为该取消指令调用云端 ASR 或 Agent/TTS；只有未被离线检测拦截、但 ASR 结果仍仅含唤醒词时，才使用上述最多一次的继续聆听逻辑。模型对齐点并非精确音素边界，应在实际说话速度下验证是否截掉首字。
- 约 800 ms 尾部静音提交问题，5 秒无问题退出，最长一轮录音 30 秒。识别、Agent、合成分别最多等待 60、90、180 秒，取消随时生效；播放音频最多 120 秒。合成单独计时，不再被较早阶段耗时或旧的 60 秒 HTTP 总超时截断。
- LLM 最终正文采用流式接收，首个完整短句立即开始 MiMo 合成；后续按较大的句组顺序合成，生成与播放重叠。每轮最多 8 段，整轮合成共用从首段开始计时的 180 秒预算，总音频仍最多 120 秒。需要兼容 API 支持 SSE 流式聊天；流失败不会自动重放工具或整轮请求。
- 音频真正开始输出后才显示说话，播放能量驱动声波；不能把网络收到音频误作扬声器已经播放完成。桌面“语音助手”在每轮对话的“执行过程”中显示工具名称、执行中/完成/失败/取消状态与执行耗时，可展开查看参数和结果摘要；工具事件不会进入回复正文或语音合成。对应轮次同时保留识别、思考、合成累计耗时及首段播放耗时，底部操作栏显示当前轮已合成与已播放段数及进度。生成与播放并行时，总段数仍可能增加，进度以当前已合成段数为基准；播放完成计数以设备实际回执为准。首段耗时从提交识别音频起计，结束于设备首次实际 `playing` 回执，不包含录音等待。
- 结束后角色页停留 3 秒再恢复原页面。临时亮屏不覆盖持久亮度设置；手动切页或屏幕操作优先于自动恢复。
- 上下文按 Token 预算管理，自动模式仅查询显式启用的 models.dev 公共目录，未启用、未匹配或无可用缓存时立即使用备用容量，不阻断对话。不向对话服务查询上下文容量，不调用 `/models/{id}` 或 `/models` 元数据接口。目录容量允许 4096–2097152 Tokens，不代表已验证的真实可用上限；不按模型名猜值、不用 usage 反推容量。自动模式备用值和手动上限默认 32768（32K），可在“设置 → 语音 → 对话服务”配置。输入预算从模型容量中扣除了回复、安全余量与工具增长等预留开销；目录如有独立 `limit.input`，输入预算和工具增长硬限制还会受其约束。占用为按序列化请求估算的值，不是服务商精确分词或计费数据。达到输入预算的 70% 后台提炼摘要，85% 在下次请求前同步压缩；摘要后优先保留最近 4 轮完整问答，硬预算不足时可进一步裁剪完整问答，摘要失败时可回退裁剪旧对话。当前单轮工具结果过大时安全报错，不自动重放工具。
- 桌面操作区默认以紧凑状态栏显示上下文估算占用比例、进度条、容量和重要摘要状态；未初始化时显示“等待初始化”，不显示虚假的零占用。点击“详情”展开 Tokens / 输入预算、容量来源、摘要 Tokens、保留/压缩/丢弃轮数和“累计用量”；容量用 K 简写，精确 Tokens 数与请求模型 ID 可在容量悬浮提示中查看。容量来源显示“models.dev / 手动设置 / 使用备用值”。占用达到 85% 或摘要压缩失败时突出警示，窄窗口允许状态换行。桌面每秒从共享记忆读取统计，后台摘要无需新对话事件也能更新，刷新保留展开状态。累计用量为当前应用会话中服务商已上报的输入/输出/总 Tokens，可能含工具决策、最终回复和摘要调用；部分兼容服务商不支持上报，未上报时显示“服务商尚未上报”，未上报部分无法统计，因此并非完整账单，重启应用后重新累计。
- 完成的文字问答、摘要及压缩/丢弃次数会以私有权限保存在设置旁的 `voice_history.json` 文件中，设备重连或应用重启后恢复，闲置 4 小时过期；“清空对话”删除记忆与摘要。已配置模型时，桌面启动后立即根据当前角色设定、内置工具和恢复的历史重算上下文估算占用，无需先发起对话或联网；使用已启用的目录缓存或备用容量估算，不等待服务商初始化或容量查询。累计用量不随历史恢复，仍按本次应用运行重新累计。历史录音不保留。

### models.dev 公共目录

在“设置 → 语音 → 对话服务”保持自动读取开启，再开启“使用 models.dev 查询上下文容量”。默认关闭，手动容量模式不使用或下载目录。JSON 对应字段：

```json
{
  "contextWindowMode": "auto",
  "modelsDevEnabled": true,
  "modelsDevProvider": "openai",
  "modelsDevModel": "gpt-4o-2024-05-13"
}
```

- 两个映射字段留空时，按目录服务商的 API 根地址和完整模型 ID 精确匹配；原生 SDK 服务商不一定记录 API 地址，内置识别 OpenAI 官方根地址，其他未匹配服务可手动填写服务商 ID。
- 中转地址、部署别名需指定目录服务商 ID，模型 ID 留空则使用实际请求模型 ID；填写目录模型 ID 可显式映射别名。映射只用于本地容量查询，不改变对话请求的模型或服务地址。不做跨服务商名称搜索、模糊匹配、去前缀或去版本号；歧义或未知条目继续使用备用值。
- 固定下载 `https://models.dev/api.json`，只读取 `limit.context` 与可选的 `limit.input`，不把输出上限当作上下文容量。目录数据只表示该条目公开的限制，中转站或私有部署可能提供更小的容量，必要时改用手动上限。
- 后台下载最多 5 秒、最多 32 MiB，不跟随重定向，不携带服务商认证或模型查询参数。模型匹配在本地完成，不发送密钥、对话、请求模型 ID 或自定义服务地址。
- 桌面在设置旁保存 `models_dev_cache.json`；CLI 在系统用户缓存目录 `Aemeath/models_dev_cache.json` 保存。缓存只保留公共服务地址、模型 ID 和容量字段，原子写入、仅当前用户可读写；不与对话记忆混存。
- 优先读取缓存，24 小时后可后台更新；桌面每 5 分钟检查是否需要刷新，下载失败至少间隔 5 分钟重试并继续使用旧缓存。CLI 在连接及需要目录的对话时检查刷新。首次无缓存时不等待目录，直接使用备用容量，目录就绪后最迟在下一轮对话采用，期间不会改变正在进行的请求预算。
- 来源明确显示为“models.dev”，悬浮可查看目录条目与独立输入上限。关闭目录开关后不再发起目录更新，已缓存文件保留；手动模式始终使用手填容量。

### Agent 工具

内置 Agent 工具包括 `get_metrics`、`get_quota`、`set_page`、`set_screen`、`lock_computer`、`web_search` 和 `get_current_time`；不包含任意 shell 或文件执行。在“设置 → 语音 → 对话服务”启用“启用 Agent 工具”并保存后，可以说“你好小爱，帮我锁屏”或“锁定这台电脑”。`lock_computer` 无参数，作用于运行 Agent 的电脑当前登录会话；`set_screen` 仍控制 ESP32 监控台屏幕。锁屏工具仅在用户明确要求电脑锁屏时使用，返回结构化的请求提交成功/失败结果，不把请求提交当作已验证最终锁屏状态。macOS 使用系统原生锁屏接口（旧系统可用 CGSession），Windows 使用 LockWorkStation；其他平台明确返回不支持。开启工具时先用独立的短工具选择请求完成 ReAct 工具调用，再以禁止工具的流式请求生成最终正文；选择过程、推理内容和工具参数不会朗读。这会比无工具模式多一次选择请求；无工具模式直接使用单次 SSE。切页协议没有设备页面 ACK，工具会说明指令已发送，而非声称已确认切页。

网页搜索可直接说“你好小爱，帮我上网搜索 ESP32-S3 的官方文档”。`web_search` 接受 `query`（最多 256 个字符）和可选 `limit`（默认 3，最多 5）；使用 Tavily Search API，需在“设置 → 语音 → 对话服务”填写“Tavily API 密钥”并保存；CLI 可在 JSON 配置 `tavilyAPIKey` 或设置 `TAVILY_API_KEY`（环境变量优先）。单次最多等待 12 秒，固定使用 basic 搜索深度，不请求 Tavily 生成答案或返回网页原文。模型得到网页标题、摘要和 URL，界面工具卡片可展开查看并打开来源，回答可用 [1] 等编号对应来源。搜索只读取摘要，不抓取网页全文；外部资料不作为指令执行，失败或无结果会明确返回，取消对话会取消搜索。搜索词会发送给 Tavily；请求使用 Bearer 鉴权，密钥不放在 URL 或结果中，不自动切换其他搜索源。未配置密钥只影响网页搜索，时间查询和设备工具仍可用；鉴权失败、请求过频和额度不足会分别提示。Tavily 的额度与计费以账户方案为准。

时间感知可说“你好小爱，现在北京时间几点？”或“纽约现在是星期几？”。`get_current_time` 接受可选的 `timezone`（IANA 时区，如 `Asia/Shanghai`、`Asia/Tokyo`、`America/New_York`，省略使用电脑本机时区；也支持北京时间等常用别名）。每次查询从固定的 timeapi.io HTTPS 服务读取 UTC 时刻，再按指定时区转换，返回日期、中文星期、UTC 偏移、夏令时状态和 Unix 时间。网络请求最多等待 4 秒，失败时明确回退电脑系统时间；取消对话会取消查询，不会把取消当作正常回退。工具卡片显示查询当时的时间与来源，历史记录不会随时钟刷新，也不会调整电脑时钟或创建定时任务。无需额外 API 密钥。

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

HA MCP 本轮仅使用 mock 协议测试；未提供真实 HA 地址和令牌，未调用真实设备，未验证真实 HA 实例连接、用户权限或设备控制效果。

自动测试使用本地 HTTP/WS 模拟服务，不消耗 MiMo/LLM 额度；它们验证请求结构、Agent 工具循环、取消、流控和恢复，不证明真实云端账号权限、音色相似度或硬件声学效果。详细实测记录见 [voice-validation.md](voice-validation.md)。
