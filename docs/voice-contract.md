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
 WakeThreshold float64 `json:"wakeThreshold"`
 Volume int `json:"volume"`
 Persona string `json:"persona"`
}
type State struct {
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

Snapshots expose `FirstAudioMillis` (submit-to-first actual playing ACK), `GeneratedSegments`, and `PlayedSegments`; the first-audio timer is written once per logical request, and segment counters distinguish generated PCM from drained playback.

Snapshots may carry `Notice` (`json:"notice,omitempty"`) for ordinary listening guidance, separate from `Error`. A wake-only ASR result can rearm capture once within the same turn; silence or another wake-only result then returns to idle without Agent/TTS calls. State messages carry the same guidance in `text`.

The `lock_computer` Function Call has an empty argument object and calls Host.LockComputer for the machine running the Agent. Structured `{ok,message}` results reflect OS request submission, not visual verification of a completed lock screen. Cancelled/invalid calls do not invoke the OS; normal system refusal is returned as ok:false so the final assistant can explain failure. macOS/Windows use native session-lock operations; unsupported platforms report failure without display-sleep substitutes or arbitrary command text.

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
type Config struct {BaseURL,APIKey,Model,Persona string; Tools bool}
type Host interface {Metrics() string; Quota() string; SetPage(context.Context,string)error; SetScreen(context.Context,bool,int)error; LockComputer(context.Context)error}
type Engine struct{}
func New(ctx context.Context,cfg Config,host Host)(*Engine,error)
func (e *Engine) Reply(ctx context.Context,text string)(string,error)
func (e *Engine) Clear()
```
Engine keeps <=10 complete conversation turns, bounded tool steps, context cancellation, plain chat when Tools=false.

`internal/wake` owns official sherpa-onnx Go/cgo bindings and model setup:
```go
type Config struct {ModelDir string; Threshold float64}
type Detector struct{}
func New(Config)(*Detector,error)
func (d *Detector) Feed(pcm []int16)(bool,error) // streaming16k, true iff你好小爱
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

Firmware transport owns `voice/voice.h` and `voice_init()` startup (after dashboard exists). It forwards state/transcript/response to UI, invokes dashboard begin on wake/listening and finish on idle/cancel/disconnect. Speaking UI begins on first rendered sample, not host state. Screen temporary override may need display.c work; animation teammate owns ui/display.c+h. Preserve user manual navigation over automatic restore.
