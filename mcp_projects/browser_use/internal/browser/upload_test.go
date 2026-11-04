package browser

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"browseruse/internal/config"
)

// TestAllowedUploadPath 钉住上传的写侧边界：只允许 security.upload_dirs 下的
// 文件（含子目录与符号链接解析后的真实路径），其余路径报 blocked_action。
func TestAllowedUploadPath(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "sub", "report.pdf")
	blocked := filepath.Join(outside, "secret.txt")
	for _, p := range []string{inside, blocked} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := &Manager{cfg: config.Config{Security: config.SecurityConfig{UploadDirs: []string{root}}}}
	if got, err := m.allowedUploadPath(inside); err != nil || got == "" {
		t.Fatalf("inside path: got %q err %v", got, err)
	}
	_, err := m.allowedUploadPath(blocked)
	var be *Error
	if !errors.As(err, &be) || be.Code != CodeBlockedAction {
		t.Fatalf("outside path err = %v, want blocked_action", err)
	}

	// An empty allowlist keeps the legacy "allow any readable path" behavior.
	m.cfg.Security.UploadDirs = nil
	if _, err := m.allowedUploadPath(blocked); err != nil {
		t.Fatalf("empty allowlist should allow: %v", err)
	}
}
