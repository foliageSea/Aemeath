//go:build darwin

package main

import (
	"fmt"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Select NSPanel at creation so WindowServer knows it never activates its app.
func petPlatformPanel() bool { return true }

// Position-only motion must not use MyGo SetBounds/SetPosition: both call
// setFrame:display:YES in v0.8.3, unnecessarily displaying the GPU surface on
// every mouse sample. Keep its existing backing store and only move its origin.
// This closure, like all nativePet state, is called only on the UI thread.
func petWindowMover(w *mygo.Window) func(mygo.Rectangle) {
	window := objc.ID(w.NativeHandle())
	return func(r mygo.Rectangle) {
		screens := objc.ID(objc.GetClass("NSScreen")).Send(objc.RegisterName("screens"))
		height := 0.0
		if objc.Send[uint64](screens, objc.RegisterName("count")) > 0 {
			primary := screens.Send(objc.RegisterName("objectAtIndex:"), uint64(0))
			height = objc.Send[petNativeRect](primary, objc.RegisterName("frame")).Size.Height
		}
		window.Send(objc.RegisterName("setFrameOrigin:"), petNativePoint{X: float64(r.X), Y: height - float64(r.Y+r.Height)})
	}
}

type petNativePoint struct{ X, Y float64 }
type petNativeSize struct{ Width, Height float64 }
type petNativeRect struct {
	Origin petNativePoint
	Size   petNativeSize
}

// All state and callbacks below belong to the UI thread. The transparent event
// view is an ordinary added subview, never an isa replacement or method swizzle
// of MyGo/AppKit objects: changing those breaks their KVO observer lifecycle.
var petPointerClass objc.Class
var petPointerTargets = map[objc.ID]objc.ID{}
var petPointerWindows = map[uintptr]objc.ID{}
var petPointerControllers = map[objc.ID]*nativePet{}

func connectPetPointer(w *mygo.Window, p *nativePet) bool {
	view := petPointerWindows[w.NativeHandle()]
	if view == 0 {
		return false
	}
	petPointerControllers[view] = p
	return true
}

func petRegisterPointerView() (objc.Class, error) {
	if petPointerClass != 0 {
		return petPointerClass, nil
	}
	methods := []objc.MethodDef{
		{Cmd: objc.RegisterName("isOpaque"), Fn: func(objc.ID, objc.SEL) bool { return false }},
		{Cmd: objc.RegisterName("mouseDownCanMoveWindow"), Fn: func(objc.ID, objc.SEL) bool { return false }},
		{Cmd: objc.RegisterName("acceptsFirstResponder"), Fn: func(objc.ID, objc.SEL) bool { return false }},
		{Cmd: objc.RegisterName("needsPanelToBecomeKey"), Fn: func(objc.ID, objc.SEL) bool { return false }},
		{Cmd: objc.RegisterName("acceptsFirstMouse:"), Fn: func(objc.ID, objc.SEL, objc.ID) bool { return true }},
	}
	// AppKit decides whether a nonactivating panel needs the keyboard from
	// the hit view before delivering mouseDown. MyGoSurfaceView reports YES
	// (it accepts first responder), despite this UI needing pointer input
	// only. Let a non-key event view receive the press and pass the original
	// NSEvent on to the unchanged GPU surface. Its coordinates stay in the
	// original window, and MyGo retains normal drag/click/context-menu logic.
	for _, name := range []string{
		"mouseDown:", "mouseUp:", "mouseDragged:", "mouseMoved:",
		"rightMouseDown:", "rightMouseUp:", "rightMouseDragged:",
		"otherMouseDown:", "otherMouseUp:", "otherMouseDragged:",
		"mouseEntered:", "mouseExited:", "scrollWheel:",
	} {
		methods = append(methods, objc.MethodDef{Cmd: objc.RegisterName(name), Fn: func(self objc.ID, cmd objc.SEL, event objc.ID) {
			// Window motion makes consecutive local pointer coordinates equal. The
			// toolkit filters those moves; observe the original NSEvent first so a
			// constant-speed drag cannot lose samples or wait for another UI frame.
			if p := petPointerControllers[self]; p != nil {
				ev := ui.InputEvent{Button: 0}
				switch name {
				case "mouseDown:":
					ev.Kind = ui.InputPointerDown
				case "mouseDragged:":
					ev.Kind = ui.InputPointerMove
				case "mouseUp:":
					ev.Kind = ui.InputPointerUp
				case "rightMouseDown:":
					ev.Kind, ev.Button = ui.InputPointerDown, 1
				}
				if ev.Kind == ui.InputPointerDown && ev.Button == 0 && objc.Send[uint64](event, objc.RegisterName("modifierFlags"))&(1<<18) != 0 {
					ev.Button = 1
				} // Control-click is a context menu.
				if ev.Kind != 0 {
					p.handlePointer(ev)
				}
			}
			if target := petPointerTargets[self]; target != 0 {
				target.Send(cmd, event)
			}
		}})
	}
	var err error
	petPointerClass, err = objc.RegisterClass("AemeathPetPointerView", objc.GetClass("NSView"), nil, nil, methods)
	return petPointerClass, err
}

func petFindSurface(view objc.ID, surface objc.Class) objc.ID {
	if view == 0 {
		return 0
	}
	if objc.Send[bool](view, objc.RegisterName("isKindOfClass:"), surface) {
		return view
	}
	children := view.Send(objc.RegisterName("subviews"))
	count := objc.Send[uint64](children, objc.RegisterName("count"))
	for i := uint64(0); i < count; i++ {
		if found := petFindSurface(children.Send(objc.RegisterName("objectAtIndex:"), i), surface); found != 0 {
			return found
		}
	}
	return 0
}

// configurePetWindow is for pointer-only pets and action bubbles. Create Hidden
// with Panel:petPlatformPanel(), configure, then ShowInactive, never Show/Focus.
// Do not put text fields or webviews in these windows: their keyboard input is
// intentionally unavailable. The added event view follows the content bounds.
//
// MyGo Panel:true initially implies AlwaysOnTop. Apply SetAlwaysOnTop(config)
// before showing: v0.8.3 applyLevel uses onTop, not Panel; false means normal.
func configurePetWindow(w *mygo.Window) error {
	if w == nil {
		return fmt.Errorf("pet window: nil window")
	}
	err := fmt.Errorf("pet window: UI event loop is not running")
	mygo.RunOnMain(func() {
		handle := w.NativeHandle()
		if handle == 0 {
			err = fmt.Errorf("pet window: native window is unavailable")
			return
		}
		window := objc.ID(handle)
		panel := objc.GetClass("NSPanel")
		if panel == 0 || !objc.Send[bool](window, objc.RegisterName("isKindOfClass:"), panel) {
			err = fmt.Errorf("pet window: macOS requires Panel:true at creation")
			return
		}
		window.Send(objc.RegisterName("setBecomesKeyOnlyIfNeeded:"), true)
		if petPointerWindows[handle] != 0 {
			err = nil
			return
		}
		content := window.Send(objc.RegisterName("contentView"))
		surfaceClass := objc.GetClass("MyGoSurfaceView")
		if surfaceClass == 0 {
			err = fmt.Errorf("pet window: MyGo native surface class is unavailable")
			return
		}
		target := petFindSurface(content, surfaceClass)
		if target == 0 {
			err = fmt.Errorf("pet window: native GPU surface is unavailable")
			return
		}
		class, classErr := petRegisterPointerView()
		if classErr != nil {
			err = fmt.Errorf("pet window: register pointer view: %w", classErr)
			return
		}
		bounds := objc.Send[petNativeRect](content, objc.RegisterName("bounds"))
		view := objc.ID(class).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("initWithFrame:"), bounds)
		if view == 0 {
			err = fmt.Errorf("pet window: create pointer view")
			return
		}
		view.Send(objc.RegisterName("setAutoresizingMask:"), uint64(2|16)) // width/height sizable
		petPointerTargets[view], petPointerWindows[handle] = target, view
		content.Send(objc.RegisterName("addSubview:"), view)
		view.Send(objc.RegisterName("release")) // retained by content, no custom dealloc
		w.OnClosed(func() {
			delete(petPointerTargets, view)
			delete(petPointerControllers, view)
			delete(petPointerWindows, handle)
		})
		err = nil
	})
	return err
}
