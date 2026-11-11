// 预览/便签 HTML 的运行时边界（迭代 2-①）。
//
// 画布节点用 sandbox="allow-same-origin" 的 srcdoc iframe 渲染（无 allow-scripts，
// 脚本本就不可执行），但外链资源（<img src="http://…">、<link rel=stylesheet>、
// @font-face url(...)）在此之前会真实加载 —— 跨域资源一旦加载成功，
// foreignObject 光栅化的 canvas 被污染，整张截图失败且难定位。
//
// 处理：入库（Store.Add/Update/装载旧快照）时统一注入 CSP —— 被阻断的资源
// 「画不出来」而不是「画出脏块」，截图链路恢复确定性；内联 <style> 与
// style 属性不受影响，设计包生成的预览（纯内联 CSS）渲染不变。
// 不做字符串手术剥 <script>/改写 src：script 已被 sandbox 挡住，外链已被
// CSP 挡住，正则改写 HTML 易碎且会产生二次转义问题。
package canvas

import "regexp"

// previewCSPMeta 是注入每个预览文档的 CSP：
//   - default-src 'none'：外链图片/样式/字体/连接全部阻断
//   - style-src 'unsafe-inline'：内联 <style> 与 style 属性可用（预览的全部样式来源）
//   - img-src data: / font-src data:：允许内联 base64 资源，外链一律不放行
const previewCSPMeta = `<meta http-equiv="Content-Security-Policy" ` +
	`content="default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:">`

var (
	cspPresentRe = regexp.MustCompile(`(?i)http-equiv\s*=\s*["']?content-security-policy`)
	headOpenRe   = regexp.MustCompile(`(?i)<head[^>]*>`)
	htmlOpenRe   = regexp.MustCompile(`(?i)<html[^>]*>`)
)

// SanitizePreviewHTML 给预览文档注入 CSP，幂等（已声明 CSP 的文档原样返回）。
// 注入位置：有 <head> 跟在其后；无 <head> 有 <html> 则补一个 <head> 插入；
// 纯片段（两者皆无）直接前置。设计包生成的文档全部带 <head>。
func SanitizePreviewHTML(html string) string {
	if html == "" || cspPresentRe.MatchString(html) {
		return html
	}
	if loc := headOpenRe.FindStringIndex(html); loc != nil {
		return html[:loc[1]] + "\n" + previewCSPMeta + html[loc[1]:]
	}
	if loc := htmlOpenRe.FindStringIndex(html); loc != nil {
		return html[:loc[1]] + "\n<head>" + previewCSPMeta + "</head>" + html[loc[1]:]
	}
	return previewCSPMeta + "\n" + html
}
