// Native macOS keyboard-focus probe; requires Accessibility permission.
// Usage: swift scripts/pet-focus.swift petX petY bubbleX bubbleY
import AppKit
import ApplicationServices
let args = CommandLine.arguments.dropFirst().compactMap(Double.init)
guard args.count == 4, AXIsProcessTrusted() else { fputs("Four screen coordinates and Accessibility permission required.\n", stderr); exit(2) }
let previous = NSWorkspace.shared.frontmostApplication
let originalMouse = CGEvent(source:nil)!.location
let app = NSApplication.shared
app.setActivationPolicy(.regular)
let window = NSWindow(contentRect:NSRect(x:80,y:500,width:380,height:130),styleMask:[.titled],backing:.buffered,defer:false)
window.title="Aemeath pet focus probe (temporary)"
let field = NSTextField(frame:NSRect(x:20,y:40,width:340,height:30))
window.contentView!.addSubview(field)
window.makeKeyAndOrderFront(nil)
window.makeFirstResponder(field)
app.activate(ignoringOtherApps:true)
func click(_ x:Double,_ y:Double){let p=CGPoint(x:x,y:y);for t in [CGEventType.mouseMoved,.leftMouseDown,.leftMouseUp]{CGEvent(mouseEventSource:nil,mouseType:t,mouseCursorPosition:p,mouseButton:.left)!.post(tap:.cghidEventTap)}}
func type(_ character:UInt16){for down in [true,false]{let e=CGEvent(keyboardEventSource:nil,virtualKey:0,keyDown:down)!;var c=character;e.keyboardSetUnicodeString(stringLength:1,unicodeString:&c);e.post(tap:.cghidEventTap)}}
var passed=true
_ = Timer.scheduledTimer(withTimeInterval:0.1,repeats:false){_ in type(120)}
_ = Timer.scheduledTimer(withTimeInterval:0.2,repeats:false){_ in print("baseline active=\(app.isActive) key=\(window.isKeyWindow) text=\(field.stringValue)")}
_ = Timer.scheduledTimer(withTimeInterval:0.3,repeats:false){_ in click(args[0],args[1])}
_ = Timer.scheduledTimer(withTimeInterval:0.6,repeats:false){_ in type(97)}
_ = Timer.scheduledTimer(withTimeInterval:0.9,repeats:false){_ in let ok=app.isActive && field.stringValue=="xa";passed = passed && ok;print("pet-click keyboard-retained=\(ok) active=\(app.isActive) key=\(window.isKeyWindow) value=\(field.stringValue) front=\(NSWorkspace.shared.frontmostApplication?.localizedName ?? "unknown")");click(args[2],args[3])}
_ = Timer.scheduledTimer(withTimeInterval:1.2,repeats:false){_ in type(98)}
_ = Timer.scheduledTimer(withTimeInterval:1.5,repeats:false){_ in let ok=app.isActive && field.stringValue=="xab";passed = passed && ok;print("bubble-click keyboard-retained=\(ok) active=\(app.isActive) key=\(window.isKeyWindow) value=\(field.stringValue) front=\(NSWorkspace.shared.frontmostApplication?.localizedName ?? "unknown")");CGWarpMouseCursorPosition(originalMouse);window.orderOut(nil);previous?.activate(options:[]);exit(passed ? 0 : 1)}
app.run()
exit(passed ? 0 : 1)
