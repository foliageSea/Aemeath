# Voice implementation contract v1

This document fixes the interfaces used by parallel implementation. Existing `/ws` telemetry remains independent; Go dials the ESP32 `/voice` endpoint.

## Wire protocol

- WebSocket text JSON, `v:1`, `type` discriminator. One owner per device; new owners receive `error` busy and close.
- Device hello: `{v:1,type:"hello",epoch:42,sample_rate:16000,channels:1,encoding:"pcm_s16le",frame_samples:320,capabilities:["capture","playback","avatar"]}`. Epoch is a random nonzero uint32 per connection.
- Host responds `{v:1,type:"config",epoch:42,enabled:true,volume:70}`. Device captures ONLY after config enabled.
- Binary frame header is 20 bytes: ASCII `AEAU` [0:4], version uint8=1 [4], kind uint8 [5] (1 microphone,2 playback), flags uint16 LE=0 [6:8], epoch uint32 LE [8:12], turn uint32 LE [12:16], seq uint32 LE [16:20], then PCM16LE mono. Mic always has turn=0 and continuously increasing seq per connection; playback seq restarts at0 each turn. 320 samples/640 bytes per normal frame; final playback frame may be shorter, must be even and <=640 bytes.
- Host state `{v:1,type:"state",epoch,turn,state:"idle|wake|listening|recognizing|thinking|synthesizing|speaking|error",text?:"..."}`; `wake` causes immediate avatar entry/wave. Host sends `transcript` and `response` text messages with epoch/turn/text.
- Wire `turn` identifies a capture/playback exchange and advances monotonically per connection. Segmented speech allocates a fresh wire turn for every clip (or an inter-clip wait) while desktop `State.Turn` counts logical user requests. Never reuse a device-closed wire turn. Credits/playing/played match only the current wire turn; a device cancel for any wire turn in the active logical request cancels the whole request. The next logical request starts beyond all prior wire turns.
- Host playback_start `{v:1,type:"playback_start",epoch,turn,samples:N,volume:70}`.
- Optional device capability `stream_playback` enables unknown totals: start includes explicit `samples:0,streaming:true`; all binary PCM retains the same wire turn and increasing sequence. A streaming end MUST include actual `samples:N` in 1..1920000 equal to received samples. Temporary underflow does not end the stream; only explicit end plus actual DMA drain produces played. Full frames remain 320 samples; the short final frame seals input. Streaming idle timeout is 30s versus fixed playback 5s. Fish selection requires this capability before any cloud work. Host PCM queue is at most 2s plus bounded result messages; credits/commands stay serviced while upstream audio waits. Device discards old playback, sets expected seq0, and responds `{v:1,type:"credit",epoch,turn,frames:32}`. Each consumed queued PCM frame replenishes one credit; host must never send without credits.
- Device sends `{v:1,type:"playing",epoch,turn}` when first audio is actually rendered, and `{v:1,type:"played",epoch,turn}` only after end marker and all queued audio rendered.
- Host playback_end `{v:1,type:"playback_end",epoch,turn}`; device compares total declared samples to received/played count.
- Cancel in either direction `{v:1,type:"cancel",epoch,turn}` aborts current round and releases audio resources. Touch wake `{v:1,type:"wake",epoch}`; touch clear `{v:1,type:"clear",epoch}`; touch mute `{v:1,type:"mute",epoch,muted:true}`.
- Error `{v:1,type:"error",epoch,turn,code:"...",message:"..."}`. Bad binary length, seq, epoch, queue overflow => terminate affected round, reject stale frames. Bounded RX and TX queues and send timeout; no audio/cloud work on HTTP/LVGL callback tasks.
- Ping/pong/close handling and disconnect cleanup required. Mic sequence gap resets KWS/VAD and cancels recording. Only matching epoch/turn controls playback.

## Go interfaces (owned by voice-runtime teammate)

Package `aemeath/agent/internal/voice`.

```go
type Config struct {
 Enabled bool `json:"enabled"`
 LLMBaseURL string `json:"llmBaseURL"`
 LLMAPIKey string `json:"llmAPIKey"`
 LLMModel string `json:"llmModel"`
 LLMTools bool `json:"llmTools"`
 ContextWindowMode string `json:"contextWindowMode"` // auto (default) | manual; invalid values fail normalization
 ContextWindow int `json:"contextWindow"` // auto fallback / manual limit; 0 => 32768; allowed 8192..2097152
 HAMCPEnabled bool `json:"haMCPEnabled"`
 HAMCPURL string `json:"haMCPURL"`
 HAAccessToken string `json:"haAccessToken"`
 MiMoBaseURL string `json:"mimoBaseURL"`
 MiMoAPIKey string `json:"mimoAPIKey"`
 TTSProvider string `json:"ttsProvider"` // mimo (default) | fish
 FishBaseURL string `json:"fishBaseURL"`
 FishAPIKey string `json:"fishAPIKey"`
 FishModel string `json:"fishModel"`
 FishReferenceID string `json:"fishReferenceID"`
 FishReferenceText string `json:"fishReferenceText"`
 FishLatency string `json:"fishLatency"`
 ReferenceAudio string `json:"referenceAudio"`
 WakeModelDir string `json:"wakeModelDir"`
 WakePhrase string `json:"wakePhrase"` // empty defaults to 你好小爱; trimmed, 2–12 supported Han characters
 WakeThreshold float64 `json:"wakeThreshold"`
 Volume int `json:"volume"`
 Persona string `json:"persona"`
}
type State struct {
 Context brain.ContextStats `json:"context"`
 Connected bool `json:"connected"`
 WakeReady bool `json:"wakeReady"`
 Phase string `json:"phase"`
 Error string `json:"error"`
 Notice string `json:"notice,omitempty"`
 Transcript string `json:"transcript"`
 Response string `json:"response"`
 Turn uint32 `json:"turn"`
 ASRMillis int64 `json:"asrMillis"`
 AgentMillis int64 `json:"agentMillis"`
 TTSMillis int64 `json:"ttsMillis"`
 FirstAudioMillis int64 `json:"firstAudioMillis"`
 GeneratedSegments int `json:"generatedSegments"`
 PlayedSegments int `json:"playedSegments"`
}
type Command struct { Kind string; Text string } // wake,cancel,clear,preview,reference
type Host interface {
 Metrics() string
 Quota() string
 SetPage(context.Context,string) error
 SetScreen(context.Context,bool,int) error
 LockComputer(context.Context) error
}
func NormalizeConfig(Config) (Config,error) // does not require keys/paths while Disabled; defaults only; no network
func Run(ctx context.Context, address string, cfg Config, host Host, commands <-chan Command, emit func(State)) error
```

Run owns ONE connection; caller retries with bounded backoff and cancels/restarts on config/device changes. Emits snapshots containing no keys. Commands are nonblocking from desktop service; preview synthesizes Text (default short greeting), reference plays configured sample without cloud, clear drops history, wake manually starts a round. Empty/missing wake model gives explicit error; manual wake/reference/preview should remain available to diagnose, but never pretend offline wake is ready.

Desktop conversation memory persists a summary and retained completed user/final-assistant pairs in `voice_history.json` next to settings, with owner-only permissions and atomic replacement (v2 writes, v1 reads remain supported). Tool traces and credentials are not stored. The shared conversation history and summary persist across idle periods, device reconnects and app restarts without time-based expiration. Automatic compression and budget trimming remain active; manual clear deletes memory (even before the lazy model engine is first created). CLI shares memory across reconnects without writing disk. On window creation the desktop restores retained text exchanges regardless of age, marks them as historical text, and omits unavailable execution timings/tool details; the current live snapshot is deduplicated. Host-assigned `State.Session` plus `Turn` identify live exchanges so reconnects cannot overwrite earlier turns with recycled turn numbers.

Snapshots expose `FirstAudioMillis` (submit-to-first actual playing ACK), `GeneratedSegments`, and `PlayedSegments`; the first-audio timer is written once per logical request, and segment counters distinguish generated PCM from drained playback.

Snapshots may carry `Notice` (`json:"notice,omitempty"`) for ordinary listening guidance, separate from `Error`. A wake-only ASR result can rearm capture once within the same turn; silence or another wake-only result then returns to idle without Agent/TTS calls. State messages carry the same guidance in `text`.

The `lock_computer` Function Call has an empty argument object and calls Host.LockComputer for the machine running the Agent. Structured `{ok,message}` results reflect OS request submission, not visual verification of a completed lock screen. Cancelled/invalid calls do not invoke the OS; normal system refusal is returned as ok:false so the final assistant can explain failure. macOS/Windows use native session-lock operations; unsupported platforms report failure without display-sleep substitutes or arbitrary command text.

The `web_search` Function Call accepts a required `query` (1–256 Unicode characters) and optional `limit` (default 3, range 1–5). It POSTs to the fixed Tavily Search API using a 12-second cancellable request, Bearer authentication, basic depth, and no answer/raw-content output and returns `{ok,query,results:[{title,url,snippet}],message?}`. The private `tavilyAPIKey` voice setting flows to the Agent; CLI `TAVILY_API_KEY` overrides JSON. Missing credentials, unauthorized responses, rate limits, and quota exhaustion return actionable ok:false reports without exposing keys or upstream bodies; canceled contexts propagate cancellation. There is no alternate-provider fallback, and missing Tavily credentials do not block unrelated tools. Only public HTTP(S) source links are accepted. Results remain untrusted tool data; the assistant references source titles/numbers rather than speaking URLs. The bounded search report is retained in ToolCall.Result for clickable source cards; tool events do not enter the speech stream.

The `get_current_time` Function Call accepts optional `timezone` (IANA or supported city alias; omitted/Local selects the host timezone). Every call obtains a fresh UTC observation over a fixed timeapi.io HTTPS endpoint with a four-second request budget, then projects it using bundled IANA timezone data. `{ok,info,message?}` exposes dateTime/utc/unix/timezone/utcOffset/weekday/date/time/isDST/source/provider/notice. Source is `network` or explicit `system_fallback`; failures of the external service fall back to the local clock with a notice, while parent cancellation/deadline propagates without fallback. ToolCall.Result retains the bounded report for time/source cards. The tool does not set the clock or create scheduled tasks.

### Home Assistant MCP

The desktop Agent connects directly to the official [Home Assistant MCP Server](https://www.home-assistant.io/integrations/mcp_server/) using Streamable HTTP and `github.com/modelcontextprotocol/go-sdk` v1.8.0. The desktop must reach HA; no ESP32 firmware change is required. Voice JSON defaults are `haMCPEnabled:false`, `haMCPURL:"http://homeassistant.local:8123/api/mcp/assist"`, and `haAccessToken:""`. CLI `HA_MCP_ENABLED` (`true`/`false`), `HA_MCP_URL`, and `HA_ACCESS_TOKEN` override the corresponding JSON fields. HA tools are available to conversations only when both HA MCP and Agent tools (`llmTools`) are enabled.

The default `/api/mcp/assist` Device control endpoint uses HA Assist exposed entities. Users may instead supply the full `/api/mcp` endpoint or a custom tool-set URL; effective tools, entity access, and permissions then follow the HA server and token, with no claim that the Assist exposure list still restricts them. Authentication in this version is a manually supplied HA user long-lived access token sent only in the `Authorization: Bearer <token>` header; browser OAuth is not implemented. Persist these settings in the existing private settings file, mask the token with an eye toggle, and never send the token to the LLM or include it in URLs, tool results, state snapshots, or logs.

The voice settings add a 智能家居 group with 启用 HA MCP, 服务地址, 长期访问令牌, and a test-connection/refresh-tool-list action. Setup order is: enable HA MCP and select exposed entities, copy the Device control URL, create a token in HA Profile, fill the desktop settings, test/refresh, save, and ask to open the living-room light or query its temperature. Testing discovers tools without executing device actions.

Discover HA tools dynamically, prefix their model-facing names with `ha_`, and refresh `tools/list` on every conversation turn so additions and removals take effect. HA discovery failure/offline status must not block the seven existing tools (`get_metrics`, `get_quota`, `set_page`, `set_screen`, `lock_computer`, `web_search`, `get_current_time`). A later turn or explicit test can reconnect. Discovery has an eight-second total budget and a tool call a twenty-second budget; parent cancellation or a shorter context deadline takes precedence. The client must not automatically replay a `tools/call` action, including after connection failures.

Reuse existing voice-conversation tool events and cards for HA titles, execution status, elapsed time, arguments, and results; tool events stay out of the spoken response stream. HA tool descriptions and results are external, untrusted data. This integration is covered by mock protocol tests only in this round: no real HA address/token was supplied, no real device was called, and actual HA connection, permissions, and device behavior remain unverified.

`internal/voiceapi` owns MiMo, WAV and resampling:
```go
type Client struct {}
func New(baseURL,key string) *Client
func (c *Client) Recognize(ctx context.Context, pcm []int16) (string,error)
func (c *Client) Synthesize(ctx context.Context,text,referencePath string) ([]int16,error) // always16k mono
func ReferencePCM(path string) ([]int16,error)
func EncodeWAV(pcm []int16,rate int) []byte
```

`internal/brain` owns Eino:
```go
type Config struct {BaseURL,APIKey,Model,Persona string; Tools bool; ContextWindow int; AutoContextWindow bool; ModelsDev *ModelCatalog; ModelsDevProvider,ModelsDevModel string}
type Host interface {Metrics() string; Quota() string; SetPage(context.Context,string)error; SetScreen(context.Context,bool,int)error; LockComputer(context.Context)error}
type Engine struct{}
func New(ctx context.Context,cfg Config,host Host)(*Engine,error)
func (e *Engine) Reply(ctx context.Context,text string)(string,error)
func (e *Engine) Clear()
```
Engine uses a configurable context window (0 defaults to 32768; configured fallback/manual values allow 8192..2097152 Tokens), bounded tool steps, context cancellation, and plain chat when Tools=false. Direct brain callers default to manual behavior because `AutoContextWindow` defaults to false; production voice passes `AutoContextWindow: c.ContextWindowMode != "manual"`, including unnormalized empty modes. Automatic mode uses only an explicitly enabled models.dev catalog, matching the exact service base URL and model ID or an explicit provider/model mapping. It never queries the conversation provider's model metadata endpoints. Catalog capacities allow 4096..2097152 Tokens and are public directory values, not independently verified service limits. Missing/disabled/unmatched catalog data immediately uses the configured fallback; asynchronous downloads never delay a reply. Downloads use the fixed public endpoint without credentials, conversation data or query parameters, cache for 24 hours, and preserve stale data on failure. Optional catalog input limits cap both the normal input budget and transport hard limit. There is no model-name guessing or capacity inference from usage. Saving a mode/value change reconnects the voice session. `brain.NormalizeContextWindow(int) (int,error)` validates the value and is called by voice configuration normalization. At 70% of estimated input budget it summarizes in the background; at 85% it compresses synchronously before the next model request. Compression aims to retain the latest four complete exchanges; hard budget enforcement can drop further complete exchanges. Oversized tool results fail safely without replaying actions.

`Engine.ContextStats() ContextStats` and `Memory.ContextStats() ContextStats` return value snapshots. Fields are `EstimatedTokens`, `InputBudget`, `ContextWindow`, `ReservedTokens`, `PromptTokens`, `CompletionTokens`, `TotalTokens`, `LocalPromptTokens`, `LocalCompletionTokens`, `LocalTotalTokens`, `SummaryTokens`, `RetainedTurns`, `Compressions`, `DroppedTurns`, `ContextInputLimit` (int), `UsagePercent` (float64), `UsageAvailable`, `Compressing` (bool), `LastCompression` (empty / `compressed` / `fallback` / `failed`), `ContextWindowSource` (string, JSON `contextWindowSource`: `modelsdev` / `manual` / `fallback`), `ContextWindowReference` (string, optional matched catalog provider/model ID), and `ContextWindowModel` (string, JSON `contextWindowModel,omitempty`, exact request model ID); all other JSON names are camelCase. Estimated context occupancy is based on serialized requests, not exact provider tokenization. Prompt/completion/total usage accumulates only provider-reported values during the current application session, including tool selection, final streaming, and summary calls; missing provider usage is unavailable, not zero consumption, and some providers never support it. Provider-reported usage is not persisted or displayed in the desktop UI. The `Local*Tokens` counters estimate actual serialized requests (including history, tool definitions and results) and received outputs (including visible reasoning and tool calls), covering tool selection, final replies and summaries; these are not exact tokenization or provider billing totals. Local input/output counters are persisted in version 2 `local_usage` (`input_tokens` / `output_tokens`), with total derived from their sum; missing saved usage restores as zero without discarding history or summary. Startup restores local usage immediately, without waiting for a voice event or request. Accepted input and completed output accounting are saved immediately, without waiting for a completed exchange. Local usage survives idle periods, reconnects, restarts, compression and budget trimming; only manual clear resets it and removes the saved history and usage record.

Desktop `Service.Snapshot()` reads `Memory.ContextStatsForConfig()` dynamically, outside the service state lock. It estimates restored history immediately from current settings and cached catalog data before an Engine exists; old connection statistics cannot mask changed settings. The native desktop refreshes snapshots once per second with `win.Update(native.refresh)`, so background summary completion and budget-driven trimming appear without a new voice event; idle time does not expire memory. Voice worker events also attach engine statistics to `State.Context`. The UI displays estimated occupancy/budget, summary state and cumulative local input/output/total estimates labeled Tokens（本地估算） separately. Capacity sources render as models.dev / 手动设置 / 未获取，使用备用值; the exact request model ID, matched catalog entry and optional input limit are available in a tooltip so long IDs do not crowd compact layouts. New configuration and stats require no device protocol or firmware change.

`internal/wake` owns official sherpa-onnx Go/cgo bindings and model setup:
```go
type Config struct {ModelDir string; Threshold float64; Phrase string}
type Detector struct{}
func NormalizePhrase(phrase string)(string,error) // trim/default; 2–12 supported Han characters
func New(Config)(*Detector,error)
func (d *Detector) Feed(pcm []int16)(bool,error) // streaming16k, true on configured phrase
func (d *Detector) Reset()
func (d *Detector) Close()
type VAD struct{}
func NewVAD(modelDir string)(*VAD,error)
func (v *VAD) Speech(pcm []int16)(bool,error)
func (v *VAD) Reset()
func (v *VAD) Close()
```
Keyword timing may be exposed as an extra method for suffix capture; coordinate additions. No-cgo builds must compile, with clear unavailable error rather than fake detections.

## Firmware UI interface (animation teammate)

`ui/voice_page.h` exposes `voice_page_create(lv_obj_t *parent)`, `voice_page_update(const char *state,const char *text,uint32_t turn)`, `voice_page_level(float level)`, `voice_page_tick(void)`. Update is thread-safe snapshot only; actual LVGL mutations occur in tick under existing LVGL task/lock. `dashboard_show_page` accepts enum VOICE=2; add `dashboard_voice_begin()` and `dashboard_voice_finish()` for enter/restore, and `dashboard_voice_active()` query. UI calls transport functions `voice_request_wake()`, `voice_request_cancel()`, `voice_request_clear()`, `voice_request_mute(bool)` declared in `voice/voice.h` (firmware teammate owns).

Firmware transport owns `voice/voice.h` and `voice_init()` startup (after dashboard exists). It forwards state/transcript/response to UI, invokes dashboard begin on wake/listening and finish on idle/cancel/disconnect. Speaking UI begins on first rendered sample, not host state. Listening uses the avatar and waveform as its ready indication, without a chime or microphone gate. Firmware continuously applies ESP-SR full-duplex AEC using the actual completed TX DMA PCM as reference, paired with timestamped RX DMA blocks through a fixed shared-clock frame offset. Native AEC frames are reblocked into 320-sample protocol frames; unsynchronized or dropped capture is an error, with no raw-audio fallback. Priority controls run before playback render handling; non-cancel text remains ordered relative to binary PCM. Playback startup flushes DMA asynchronously and issues initial credits only after PA enable succeeds. A valid priority cancel seals its wire turn even if its queued start has not run. During an active Agent turn, a keyword hit cancels the old turn and starts a fresh listening turn with at most two seconds of keyword-end suffix audio. Explicit cancel returns to idle; hold-to-record bypasses KWS/VAD. Screen temporary override may need display.c work; animation teammate owns ui/display.c+h. Preserve user manual navigation over automatic restore.
