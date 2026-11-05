package config

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"browseruse/internal/version"
)

// AppName is the application name, which also names its data directory.
// BROWSER_USE_APP_NAME isolates a dev/test instance from the user's browser.
var AppName = envOr("BROWSER_USE_APP_NAME", "browser-use")

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// Config are the user settings of browser-use. The zero value is filled by
// Default.
type Config struct {
	Server   ServerConfig   `json:"server"`
	Agent    AgentConfig    `json:"agent"`
	Control  ControlConfig  `json:"control"`
	Script   ScriptConfig   `json:"script"`
	Session  SessionConfig  `json:"session"`
	Security SecurityConfig `json:"security"`
	UI       UIConfig       `json:"ui"`
}

// SecurityConfig configures local-file and page boundaries.
type SecurityConfig struct {
	// UploadDirs limits files upload into a page to these roots (absolute,
	// ~ and %VAR% are expanded). Empty disables the check for compatibility.
	UploadDirs []string `json:"upload_dirs"`
}

// SessionConfig configures tab/window restore.
type SessionConfig struct {
	Restore bool `json:"restore"`
	Save    bool `json:"save"`
	MaxTabs int  `json:"max_tabs"`
}

// ScriptConfig configures the moejs script engine.
type ScriptConfig struct {
	Enabled          bool `json:"enabled"`
	MaxSourceBytes   int  `json:"max_source_bytes"`
	TimeoutMS        int  `json:"timeout_ms"`
	MaxTimeoutMS     int  `json:"max_timeout_ms"`
	MemoryLimitMB    int  `json:"memory_limit_mb"`
	MaxOutputBytes   int  `json:"max_output_bytes"`
	MaxActions       int  `json:"max_actions"`
	MaxResultBytes   int  `json:"max_result_bytes"`
	ReleaseOnFinish  bool `json:"release_on_finish"`
	KeepSources      bool `json:"keep_sources"`
	KeepSourcesDays  int  `json:"keep_sources_days"`
	AllowDynamicCode bool `json:"allow_dynamic_code"`
}

type ServerConfig struct {
	Port int    `json:"port"`
	Bind string `json:"bind"`
}

type AgentConfig struct {
	MaxTabs          int    `json:"max_tabs"`
	DefaultTimeoutMS int    `json:"default_timeout_ms"`
	SnapshotMaxChars int    `json:"snapshot_max_chars"`
	FollowAgent      bool   `json:"follow_agent"`
	AllowEval        bool   `json:"allow_eval"`
	DefaultURL       string `json:"default_url"`
	DownloadsSubdir  string `json:"downloads_subdir"`
}

type ControlConfig struct {
	Mask          bool `json:"mask"`
	IdleReleaseMS int  `json:"idle_release_ms"`
	Dim           int  `json:"dim"`
}

type UIConfig struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Default returns the built-in settings.
func Default() Config {
	return Config{
		Server: ServerConfig{Bind: "127.0.0.1"},
		Agent: AgentConfig{
			MaxTabs:          8,
			DefaultTimeoutMS: 30000,
			SnapshotMaxChars: 12000,
			FollowAgent:      true,
			AllowEval:        true,
			DefaultURL:       "about:blank",
			DownloadsSubdir:  "downloads",
		},
		Control: ControlConfig{Mask: true, IdleReleaseMS: 120000, Dim: 20},
		Script: ScriptConfig{
			Enabled: true, MaxSourceBytes: 262144, TimeoutMS: 120000, MaxTimeoutMS: 300000,
			MemoryLimitMB: 64, MaxOutputBytes: 32768, MaxActions: 200, MaxResultBytes: 65536,
			ReleaseOnFinish: true, KeepSources: true, KeepSourcesDays: 7,
		},
		Session: SessionConfig{Restore: true, Save: true, MaxTabs: 20},
		Security: SecurityConfig{
			UploadDirs: defaultUploadDirs(),
		},
		UI: UIConfig{Width: 1280, Height: 860},
	}
}

func defaultUploadDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, "Desktop"),
		filepath.Join(home, "Documents"),
		filepath.Join(home, "Downloads"),
	}
}

// Load reads config.json over the defaults. A missing file keeps the defaults;
// a broken file is logged and also keeps the defaults — a half-parsed config
// would silently run the app on partial settings.
func Load() Config {
	base := Default()
	dir, err := Dir()
	if err != nil {
		return base
	}
	return loadFrom(dir, base)
}

// loadFrom applies <dir>/config.json over base. Split from Load so tests can
// feed a temp directory instead of the user's real config.
func loadFrom(dir string, base Config) Config {
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return base
	}
	cfg := base
	if err := json.Unmarshal(b, &cfg); err != nil {
		log.Printf("browser-use: config.json 解析失败，使用默认配置: %v", err)
		return base
	}
	normalize(&cfg)
	return cfg
}

// normalize 把 <=0 的数值键补回默认值，避免 config.json 写 0 让标签页上限、
// 脚本预算等悄悄失效。
func normalize(cfg *Config) {
	if cfg.Agent.MaxTabs <= 0 {
		cfg.Agent.MaxTabs = 8
	}
	if cfg.Agent.DefaultTimeoutMS <= 0 {
		cfg.Agent.DefaultTimeoutMS = 30000
	}
	if cfg.Agent.SnapshotMaxChars <= 0 {
		cfg.Agent.SnapshotMaxChars = 12000
	}
	if cfg.Control.Dim <= 0 {
		cfg.Control.Dim = 20
	}
	if cfg.Script.MaxSourceBytes <= 0 {
		cfg.Script.MaxSourceBytes = 262144
	}
	if cfg.Script.TimeoutMS <= 0 {
		cfg.Script.TimeoutMS = 120000
	}
	if cfg.Script.MaxTimeoutMS <= 0 {
		cfg.Script.MaxTimeoutMS = 300000
	}
	if cfg.Script.MemoryLimitMB <= 0 {
		cfg.Script.MemoryLimitMB = 64
	}
	if cfg.Script.MaxOutputBytes <= 0 {
		cfg.Script.MaxOutputBytes = 32768
	}
	if cfg.Script.MaxActions <= 0 {
		cfg.Script.MaxActions = 200
	}
	if cfg.Script.MaxResultBytes <= 0 {
		cfg.Script.MaxResultBytes = 65536
	}
	if cfg.Script.KeepSourcesDays <= 0 {
		cfg.Script.KeepSourcesDays = 7
	}
	if cfg.Session.MaxTabs <= 0 {
		cfg.Session.MaxTabs = 20
	}
	if cfg.UI.Width <= 0 {
		cfg.UI.Width = 1280
	}
	if cfg.UI.Height <= 0 {
		cfg.UI.Height = 860
	}
}

// Dir returns the per-user data directory of browser-use.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, AppName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// RuntimePath returns the file the running app writes its port and token to.
func RuntimePath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runtime.json"), nil
}

// RuntimeInfo is what the CLI and the MCP shim read to reach a running app.
type RuntimeInfo struct {
	App       string    `json:"app"`
	Version   string    `json:"version"`
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	Token     string    `json:"token"`
	StartedAt time.Time `json:"started_at"`
	Profile   string    `json:"profile"`
}

func (r RuntimeInfo) URL() string {
	return "http://127.0.0.1:" + itoa(r.Port)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// LoadRuntime reads the runtime file, if an instance wrote one.
func LoadRuntime() (*RuntimeInfo, error) {
	path, err := RuntimePath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r RuntimeInfo
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// SaveRuntime writes the runtime file for the CLI.
func SaveRuntime(r *RuntimeInfo) error {
	path, err := RuntimePath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RemoveRuntime deletes the runtime file of this instance.
func RemoveRuntime() {
	if path, err := RuntimePath(); err == nil {
		_ = os.Remove(path)
	}
}

// NewRuntimeInfo fills the fields of a runtime file.
func NewRuntimeInfo(port int, token string) *RuntimeInfo {
	return &RuntimeInfo{
		App:       AppName,
		Version:   version.Version,
		PID:       os.Getpid(),
		Port:      port,
		Token:     token,
		StartedAt: time.Now(),
		Profile:   "agent",
	}
}
