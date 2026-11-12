// Desktop integration
//
// Decode AtlasPNG once with image/png and use View.Rect as the source rectangle.
// The visible pet window is exactly View.Size (192x208 times Config.Scale); a
// bubble/menu belongs in a separate compact UI surface, not an enlarged mostly
// transparent pet rectangle. All frames are full cells; never trim their alpha.
//
// Use DefaultConfig/LoadConfig with a separate pet.json next to the desktop's
// settings file. SaveConfig never references or restarts the desktop service.
// Persist Engine.Config after dragging, preference changes, and graceful exit;
// do not save at 30Hz. Mouse-ignore is a process-local UI feature, intentionally
// not present in Config, and must start false on every launch.
//
// Construct NewEngine with the current time, supply every monitor work area via
// SetScreens, feed AssistantSnapshot from the existing service, and call Tick at
// 30Hz on the UI thread; Wave keeps 140ms frames, all other actions keep 150ms.
// Use one owner (or external synchronization) for Engine
// and Projector. SetScreens copies its input and supports negative monitor
// coordinates and disappearing monitors. An oversized pet is anchored to the
// nearest work area's top-left; the host can offer a smaller scale.
//
// AssistantSnapshot maps desktop.State.Voice.Phase/Connected/Turn/Session/Error,
// Voice.Response to Reply, and Settings.Voice.Enabled to Enabled. The pet does
// not depend on desktop/voice packages, initialize another assistant, use network
// resources, or send a cancel command itself. View.CanCancel is presentation
// state: the UI invokes its existing service cancellation method once per user
// action. Session+Turn identify a reply; the initial snapshot and a new session
// establish a baseline without replaying retained responses. Failed reacts for
// one 8-frame cycle (1.2s), then returns to normal interaction even while the
// service retains its error; repeated snapshots do not refresh this reaction.
// Bubbles last at most 6s per reply and contain at most 80 Unicode code points.
//
// Click waves for one animation cycle, DoubleClick jumps, BeginDrag/DragTo/
// EndDrag use an absolute top-left position, and SetPaused/SetHovered suspend
// autonomous activity without blocking clicks or assistant display. Time is
// injectable through event methods and Tick; backwards time cannot regress
// animation and a stalled UI's movement is capped at 250ms per tick.
package pet
