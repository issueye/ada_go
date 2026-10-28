package plugins

// Summary 是一个插件的状态摘要，供 plugin.list、页面与审计展示（8.2 PluginSummary）。
//
// 刻意不含密钥、宿主绝对路径与脚本细节：这些出现在界面上就是泄露（09 §9.4）。
type Summary struct {
	ID          string   `json:"id"`
	Name        string   `json:"name,omitempty"`
	Version     string   `json:"version"`
	Trust       string   `json:"trust"`
	Enabled     bool     `json:"enabled"`
	Status      string   `json:"status"` // ready | degraded | disabled | failed
	Fingerprint string   `json:"fingerprint,omitempty"`
	LastError   string   `json:"lastError,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	Hooks       []string `json:"hooks,omitempty"`
	Slots       []string `json:"slots,omitempty"`
	// Skills 是插件包内随包技能的名字（6.7：只给名字，不给 description/正文）。
	Skills []string `json:"skills,omitempty"`
}
