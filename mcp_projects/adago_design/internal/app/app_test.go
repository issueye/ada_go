package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"adagodesign/internal/config"
)

func stopTestServer(t *testing.T, s *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Errorf("stop server: %v", err)
	}
}

func TestStartServesHealthAndIndex(t *testing.T) {
	cfg := &config.Config{Port: 0, DataDir: t.TempDir(), Theme: "dark"}
	srv, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopTestServer(t, srv) })
	if srv.Store() == nil || srv.Tasks() == nil {
		t.Fatal("Start 应暴露 Store 与 TaskQueue")
	}

	res, err := http.Get(srv.BaseURL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var health struct {
		OK      bool   `json:"ok"`
		Server  string `json:"server"`
		DataDir string `json:"data_dir"`
	}
	if err := json.NewDecoder(res.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if !health.OK || health.Server != "adago-design" || health.DataDir != cfg.DataDir {
		t.Fatalf("健康检查异常: %+v", health)
	}

	res, err = http.Get(srv.BaseURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `data-theme="dark"`) {
		t.Fatal("index.html 应注入 -theme dark 缺省主题")
	}
}

func TestStartRejectsDuplicateDataDir(t *testing.T) {
	dir := t.TempDir()
	first, err := Start(context.Background(), &config.Config{Port: 0, DataDir: dir, Theme: "light"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopTestServer(t, first) })

	u, err := url.Parse(first.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("解析端口失败: %v", err)
	}
	second, err := Start(context.Background(), &config.Config{Port: port, DataDir: dir, Theme: "light"})
	if err == nil {
		stopTestServer(t, second)
		t.Fatal("同数据目录的第二实例应被拒绝")
	}
	var ie *InstanceExistsError
	if !errors.As(err, &ie) {
		t.Fatalf("错误类型 = %T, 期望 *InstanceExistsError: %v", err, err)
	}
	if ie.URL != first.BaseURL {
		t.Fatalf("已运行实例地址 = %q, 期望 %q", ie.URL, first.BaseURL)
	}
}
