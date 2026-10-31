package web_test

// 图片输入（7.7.4）：发送表单 multipart → handleTurn 校验 → turn.start/steer
// 的 images 参数（base64 + mime，≤4 张、单张 ≤8MB、PNG/JPEG/WebP/GIF）。

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/adag/ada-go/internal/gateway/web"
	"testing"
)

// pngBytes 是一张 1x1 PNG 的最小合法字节（浏览器/服务端只看头部与长度）。
var pngBytes = []byte{
	0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 13, 'I', 'H', 'D', 'R',
}

func multipartBody(t *testing.T, text string, files map[string][]byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("text", text); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("idempotencyKey", "abcdefgh"); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		fw, err := writer.CreateFormFile("images", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

func TestTurnSendWithImage(t *testing.T) {
	core := newFakeCore().on("turn.start", `{"runId":"run_1","duplicate":false,"seq":1}`)
	srv, _ := newServer(t, core)

	body, contentType := multipartBody(t, "看这张图", map[string][]byte{"a.png": pngBytes})
	req := httptest.NewRequest(http.MethodPost, "/threads/th_1/send", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("turn.start = %d (body=%s)", rec.Code, rec.Body.String())
	}
	call, ok := core.lastCall("turn.start")
	if !ok {
		t.Fatal("没有调用 turn.start")
	}
	imagesJSON, merr := json.Marshal(call.Params["images"])
	if merr != nil {
		t.Fatal(merr)
	}
	var images []struct {
		Type string `json:"type"`
		Mime string `json:"mime"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(imagesJSON, &images); err != nil || len(images) != 1 {
		t.Fatalf("turn.start 缺少 images 参数: %s (%+v)", imagesJSON, call.Params)
	}
	if images[0].Type != "image" || images[0].Mime != "image/png" ||
		!strings.HasPrefix(images[0].Data, "iVBOR") {
		t.Fatalf("图片部件形状错误: %+v", images)
	}
	if call.Params["text"] != "看这张图" {
		t.Fatalf("text = %v", call.Params["text"])
	}
}

func TestTurnSendImageValidation(t *testing.T) {
	// 非图片文件被内容嗅探拒绝。
	core := newFakeCore().on("turn.start", `{"runId":"run_1","duplicate":false,"seq":1}`)
	srv, _ := newServer(t, core)

	body, contentType := multipartBody(t, "看这张图", map[string][]byte{"a.txt": []byte("plain text, not an image")})
	req := httptest.NewRequest(http.MethodPost, "/threads/th_1/send", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非图片附件 = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "iVBOR") {
		t.Fatal("被拒绝的请求不应进入 turn.start")
	}

	// 纯图片无文本：允许（text 与 images 至少其一非空）。
	core2 := newFakeCore().on("turn.start", `{"runId":"run_2","duplicate":false,"seq":1}`)
	srv2, _ := newServer(t, core2)
	body2, contentType2 := multipartBody(t, "", map[string][]byte{"b.png": pngBytes})
	req2 := httptest.NewRequest(http.MethodPost, "/threads/th_2/send", body2)
	req2.Header.Set("Content-Type", contentType2)
	rec2 := httptest.NewRecorder()
	srv2.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusSeeOther {
		t.Fatalf("纯图片发送 = %d (body=%s)", rec2.Code, rec2.Body.String())
	}
	call2, _ := core2.lastCall("turn.start")
	if images2, merr := json.Marshal(call2.Params["images"]); merr != nil ||
		!strings.Contains(string(images2), "image/png") {
		t.Fatalf("纯图片发送缺少 images: %+v (%v)", call2.Params, merr)
	}
}

// 用户消息里的图片按 data URL 渲染，并且是「可点开查看」的按钮：
// 固定尺寸裁切会让宽截图看起来和输入框预览像两张图，所以只限最大宽高。
func TestThreadPageRendersClickableUserImage(t *testing.T) {
	entries := `{"entries":[{
	  "id":"e1","threadId":"th_1","seq":1,"kind":"message","role":"user",
	  "payload":{"role":"user","parts":[
	    {"type":"text","text":"看这张图"},
	    {"type":"image","mime":"image/png","data":"iVBORw0KGgo="}
	  ]},
	  "createdAt":"2026-10-28T10:00:01Z"}],"hasMore":false}`
	core := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entries)
	srv, _ := newServer(t, core)

	body := serve(t, srv, "GET", "/thread/th_1", nil).Body.String()
	for _, want := range []string{
		`<button class="ada-msg__image" type="button" data-image-viewer`,
		`src="data:image/png;base64,iVBORw0KGgo="`,
		"看这张图",
	} {
		wantContains(t, body, want)
	}
}

// 无 JS 的浏览器也能用原生 file input 提交 multipart：模板里有 images 字段。
func TestComposerTemplateHasImageInput(t *testing.T) {
	core := newFakeCore().
		on("thread.list", emptyThreadList).
		on("thread.get", `{"thread":{"id":"th_1","workspace":"","title":"图片会话","createdAt":"2026-10-28T10:00:00Z","updatedAt":"2026-10-28T10:00:00Z","archived":false},"items":[],"seq":1,"run":{"runId":"run_x","status":"terminal","terminal":"completed"},"interactions":[],"hasMore":false}`).
		on("thread.entries", `{"entries":[],"hasMore":false}`).
		on("model.list", `{"models":[],"current":{"providerId":"fake","modelId":"fake-1"}}`)
	srv, _ := newServer(t, core)

	body := serve(t, srv, "GET", "/threads/th_1", nil).Body.String()
	for _, want := range []string{
		`enctype="multipart/form-data"`,
		`type="file" name="images" multiple`,
		`data-image-thumbs`,
	} {
		wantContains(t, body, want)
	}
	// 缩略图容器必须在输入框之前：新建任务页与会话页的位置要一致。
	if strings.Index(body, "data-image-thumbs") > strings.Index(body, `name="text"`) {
		t.Fatal("会话页的图片缩略图不在输入框上方")
	}
}

// 新建任务页也支持图片：multipart 表单 → thread.create + turn.start 带 images。
func TestCreateThreadWithImage(t *testing.T) {
	ws := t.TempDir()
	core := newFakeCore().
		on("thread.create", `{"thread":{"id":"th_img"}}`).
		on("turn.start", `{"runId":"run_img","duplicate":false,"seq":0}`)
	srv, _ := newServerWithWorkspaces(t, core, []string{ws})

	body, contentType := multipartBody(t, "", map[string][]byte{"shot.png": pngBytes})
	req := httptest.NewRequest(http.MethodPost, "/threads", body)
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(&http.Cookie{Name: web.WorkspaceCookieName, Value: "0"})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /threads = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/threads/th_img" {
		t.Fatalf("Location = %q", loc)
	}
	start, ok := core.lastCall("turn.start")
	if !ok {
		t.Fatal("没有调用 turn.start")
	}
	imagesJSON, merr := json.Marshal(start.Params["images"])
	if merr != nil || !strings.Contains(string(imagesJSON), "image/png") {
		t.Fatalf("turn.start 缺少 images: %+v (%v)", start.Params, merr)
	}

	// 新建任务页模板：图片输入与缩略图容器存在。
	core2 := newFakeCore().on("thread.list", emptyThreadList)
	srv2, _ := newServerWithWorkspaces(t, core2, []string{ws})
	home := serve(t, srv2, "GET", "/", nil).Body.String()
	for _, want := range []string{
		`id="ada-image-input-new"`,
		`data-image-thumbs`,
		`for="ada-image-input-new"`,
		`chat-images.js`, // 新建任务页也要加载图片绑定脚本
	} {
		wantContains(t, home, want)
	}
	// 与会话页同一口径：缩略图在输入框上方。
	if strings.Index(home, "data-image-thumbs") > strings.Index(home, `name="text"`) {
		t.Fatal("新建任务页的图片缩略图不在输入框上方")
	}
}
