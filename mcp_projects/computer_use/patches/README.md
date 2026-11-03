# 历史补丁（已合并进 mygo 上游）

mygo-transparent-native-ui.patch 让 Windows 上的原生 UI（WindowOptions.Content）
透出桌面：窗口带 WS_EX_NOREDIRECTIONBITMAP，由 DirectComposition 合成内容 alpha。

**这个改动已经合并进 mygo 上游**（见 internal/windows/window.go 中
o.Flyout != nil || o.Transparent && o.Surface 的分支），当前依赖的
mygo v0.7.4-36 及以后不需要打这个补丁；文件仅作旧检出（v0.6.x 及更早）的参考。

应用方式（旧检出）：

    cd E:\codes\go_projects\mygo
    git apply E:\codes\go_projects\computer_use\patches\mygo-transparent-native-ui.patch
