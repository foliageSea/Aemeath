// Real macOS window-drag probe. Usage: pet-drag <application pid>
import AppKit
import ApplicationServices
let args=CommandLine.arguments
if args.count != 2 || !AXIsProcessTrusted() { fputs("Application pid and Accessibility permission required.\n",stderr);exit(2) }
let pid=Int(args[1])!
func windows()->[[String:Any]]{CGWindowListCopyWindowInfo(.optionOnScreenOnly,kCGNullWindowID) as! [[String:Any]]}
guard let w=windows().first(where:{($0[kCGWindowOwnerPID as String] as? Int)==pid && ($0[kCGWindowName as String] as? String)=="爱弥斯桌宠"}),let raw=w[kCGWindowBounds as String] as? [String:Any],let initial=CGRect(dictionaryRepresentation:raw as CFDictionary),let id=w[kCGWindowNumber as String] as? UInt32 else{fputs("Visible pet not found.\n",stderr);exit(2)}
func bounds()->CGRect{let items=CGWindowListCopyWindowInfo(.optionIncludingWindow,id) as! [[String:Any]];return CGRect(dictionaryRepresentation:items[0][kCGWindowBounds as String] as! CFDictionary)!}
let previousMouse=CGEvent(source:nil)!.location
let previousApp=NSWorkspace.shared.frontmostApplication?.processIdentifier
let origin=CGPoint(x:initial.midX,y:initial.midY)
func mouse(_ kind:CGEventType,_ x:Double,_ y:Double){CGEvent(mouseEventSource:nil,mouseType:kind,mouseCursorPosition:CGPoint(x:x,y:y),mouseButton:.left)!.post(tap:.cghidEventTap)}
var step=0
let count=90,delta=2.0
var errors=[Double]()
mouse(.mouseMoved,origin.x,origin.y)
_ = Timer.scheduledTimer(withTimeInterval:0.1,repeats:false){_ in
 mouse(.leftMouseDown,origin.x,origin.y)
 _ = Timer.scheduledTimer(withTimeInterval:1.0/60.0,repeats:true){timer in
  if step>0 {let r=bounds();errors.append(abs(r.minX-initial.minX-delta*Double(step)))}
  if step==count {
   timer.invalidate();mouse(.leftMouseUp,origin.x+delta*Double(step),origin.y)
   _ = Timer.scheduledTimer(withTimeInterval:0.15,repeats:false){_ in
    let finalError=abs(bounds().minX-initial.minX-delta*Double(count))
    let maxError=errors.max() ?? .infinity,meanError=errors.reduce(0,+)/Double(errors.count)
    let focusRetained=NSWorkspace.shared.frontmostApplication?.processIdentifier==previousApp
    print("samples=\(count) maxLagDIP=\(maxError) meanLagDIP=\(meanError) finalErrorDIP=\(finalError) focusRetained=\(focusRetained)")
    CGWarpMouseCursorPosition(previousMouse)
    exit(maxError<=24 && meanError<=6 && finalError<=2 && focusRetained ? 0 : 1)
   }
  } else {step+=1;mouse(.leftMouseDragged,origin.x+delta*Double(step),origin.y)}
 }
}
RunLoop.main.run()
