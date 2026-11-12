# 桌宠验收记录

环境：macOS / Apple Silicon，Go 1.27.3，MyGO 0.8.3。范围为 P0 资源与透明窗口、P1 离线互动与设置、P2 复用既有助手状态；P3 无硬件语音/聊天未实现。

## 自动验证

- 整个 agent 的 go test ./... 通过；监控、已有语音、会话锁及桌面 UI 回归未回退。
- go vet ./cmd/aemeath-desktop ./internal/pet ./scripts/pet-assets 通过。
- go test -race -cover ./cmd/aemeath-desktop ./internal/pet 通过，无数据竞争；桌面包覆盖率 80.6%，纯桌宠包 94.5%。
- 资源生成测试重新解码原 WebP、逐帧检查 alpha、unused 空格及边界，逐字节比较四个生成产物；go generate ./internal/pet 可复现。
- 控制器/headless UI 覆盖：单击/双击、拖动与取消、原生上下文菜单、旧菜单 turn/session 更新后不误取消、新菜单重开、气泡按钮、隐藏不派发、工作区负坐标、屏幕恢复、独立配置、穿透恢复与无托盘保护。
- 构建通过：macOS 正式二进制（mygo_noinspector）、macOS e2e、Windows amd64 交叉构建。Windows 的完整原生行为仍需实机验证，交叉构建不是桌面验收。

## 拖动性能修复（2026-11-12）

拖动从 view 重建中的 Pressed 轮询改为即时鼠标事件；macOS 直接观察输入层原始 mouseDragged，避免窗口随指针移动后局部坐标相同被 toolkit 过滤。位置变化走 setFrameOrigin，不再经 setFrame:display:YES；尺寸变化仍走正常缩放路径。缓存已应用的几何，拖动中不反复读原生 Bounds 或写配置，松手时保存一次。

使用 [真实拖动探针](../agent/scripts/pet-drag.swift)，同机同轨迹按 60Hz 发送 90 个真实 CGEvent、每步移动 2 DIP，并在下一采样点读取 WindowServer 窗口位置（这不是 FPS 或屏幕显示延迟测量）：

| 实现 | 最大位置滞后（DIP） | 平均位置滞后（DIP） | 松手误差（DIP） | 前台应用不变 |
| --- | ---: | ---: | ---: | --- |
| 修复前 | 20 | 9.27 | 0 | 是 |
| 修复后 | 2 | 0.02 | 0 | 是 |

新增测试覆盖不推进 UI 帧/动画时钟的连续移动、相同局部坐标、位置路径不触发 resize/Bounds 查询、最终释放点、拖动取消和尺寸调整。全量 go test ./...、桌宠 -race 测试、go vet 及原生端到端脚本均通过。以上是本机一次对照结果，不承诺所有设备得到相同数值。

## 原生验收方式

[端到端脚本](../agent/scripts/pet-smoke.mjs) 使用实际 MyGO 应用和窗口，包含角色/气泡截图、聆听/思考/合成/播报/错误、长回复、缩放、隐藏/恢复、穿透/恢复以及置顶切换。场景注入仅在 e2e build tag 且显式开启测试环境变量时生效；不调用真实语音接口，不联网访问收费服务，也不读取生产会话。

在 agent 目录使用独立数据目录运行，不设置 AEMEATH_TEST_COMPACT（普通窗口压缩验收不能改变宠物大小）：

~~~sh
go build -tags e2e -o build/pet-e2e ./cmd/aemeath-desktop
mkdir -p build/pet-validation/data
AEMEATH_TEST_DATA_DIR="$PWD/build/pet-validation/data" \
AEMEATH_TEST_PET_SCENARIOS=1 \
MYGO_AUTOMATION=/tmp/aemeath-pet-validation.sock ./build/pet-e2e
# 另一个终端，仍在 agent：
node scripts/pet-smoke.mjs
~~~

截图和报告写入 git-ignore 的 build/pet-validation。实际主窗口、角色窗口和单独气泡窗口共同运行，角色窗口是 192×208 DIP，Retina 截图为 384×416 像素。最终截图解码后检查：待机图 90,888 个 alpha=0 像素、68,856 个非零 alpha 像素，四角均全透明；挥手、跳跃与 150% 大小截图同样通过透明角落检查，不是黑色矩形背景。

注意：automation.input 直接向渲染器发送事件，不能用它声称 OS 不抢焦点或透明像素可穿透。原生系统验收另使用 [macOS 键盘探针](../agent/scripts/pet-focus.swift)：先向另一个应用的输入框写入基线字符，再用真实 CGEvent 点击角色及气泡，继续输入并检查字符仍进入原输入框；需要辅助功能权限。脚本编译成独立程序运行（不要用解释器代替原生事件循环）。

~~~sh
swiftc scripts/pet-focus.swift -o build/pet-validation/focus-probe
# 屏幕全局 DIP 坐标；先保证角色和气泡可见且未穿透
./build/pet-validation/focus-probe petX petY bubbleX bubbleY
~~~

### macOS 实际系统结果

最终实现使用创建时即为非激活的 NSPanel，加普通透明 pointer-only NSView 输入层转发原始鼠标事件；不替换 NSWindow / GPU surface 的 isa，不全局 swizzle。只设置 becomesKeyOnlyIfNeeded 仍不够，因为 MyGO 实际 surface 会请求 panel 成为 key window，真实系统探针已发现并覆盖这个问题。

最终实际系统探针 exit 0：

~~~text
baseline active=true key=true text=x
pet-click keyboard-retained=true active=true key=true value=xa front=focus-probe
bubble-click keyboard-retained=true active=true key=true value=xab front=focus-probe
~~~

气泡测试点击真实右上关闭按钮，再确认气泡不可见，避免仅吞掉事件造成假阳性。完整实际应用主窗口、角色、按钮气泡共同运行并退出已验证无 KVO 崩溃；原生层置顶关闭 / 开启 / 关闭对应 level 0 / 3 / 0。以上不代表 Windows 也已完成同样实机验收。

## 已发现并修复的回归

- MyGO 的 DoubleClicked 在第二次按下触发，但松开又报告 Clicked；增加跨 press/release 抑制，避免跳跃被挥手覆盖。
- 拖动在角色内部释放时不得再次触发挥手；取消拖动恢复原位置及原保存状态，不遗留延迟写盘。
- 原生上下文菜单选择时会重新调用 builder；固定菜单打开时的会话/轮次，防止取消到新请求；关闭菜单再开会重新绑定。
- macOS 运行时替换 NSWindow 类的方案在完整 UI 退出时触发 KVO 观察者异常，已删除该方案；仅用最小窗口循环不足以证明完整应用生命周期安全。

## 平台边界及人工矩阵

- 鼠标穿透当前是整窗开关；透明角落不是系统级逐像素穿透。紧凑窗口和独立气泡只降低遮挡面积，不改变这个事实。
- Windows 使用保留现有样式的 WS_EX_NOACTIVATE；MyGO 0.8.3 的 GPU surface 仍显式 SetFocus，不能把样式配置或交叉构建当作“不抢键盘”的实机保证。需要 Windows 物理跨应用点击/键盘测试及必要的平台修复。
- 其他平台明确返回不支持并不显示桌宠；不影响主应用。
- 多屏几何逻辑已有自动测试；没有声称完成实际拔插显示器、混合 DPI、所有 Spaces/全屏程序或 Dock 方位的穷举验收。

建议发布前实机矩阵：左/上副屏负坐标、主屏切换、不同缩放、跨屏拖动、拔屏、Dock 底/左/右与自动隐藏；最小/最大角色大小；置顶关闭允许其他窗口覆盖；穿透后托盘/主菜单恢复；应用完整退出和再次打开。不要把模拟工作区测试写成已完成物理拔屏测试。
