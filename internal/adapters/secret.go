package adapters

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// Secrets 是 ports.SecretBroker 的生产实现：按名字取密钥 + 日志脱敏（09 §9.4）。
//
// 取值顺序：环境变量 > secrets.toml（附录 B 的优先级口径）。密钥永不进入提示词、
// 工具结果、日志、前端 props，也不进入 process.env 或插件子进程环境（09 §9.4）；
// 因此这里刻意不提供「批量导出」接口，只能按已声明的名字逐个取。
type Secrets struct {
	mu sync.RWMutex
	// values 是 name → secret；只驻留内存。
	values map[string]string
	// declared 是 pluginID → 允许申请的名字集合（装配期由清单写入）。
	declared map[string]map[string]bool
	// envPrefix 是环境变量前缀；ADAG_SECRET_OPENAI_API_KEY → openai_api_key。
	envPrefix string
}

var _ ports.SecretBroker = (*Secrets)(nil)

// SecretEnvPrefix 是密钥环境变量前缀（保留前缀，插件清单不得声明 ADAG_*）。
const SecretEnvPrefix = "ADAG_SECRET_"

// NewSecrets 创建未加载任何密钥的 broker。
func NewSecrets() *Secrets {
	return &Secrets{
		values:    map[string]string{},
		declared:  map[string]map[string]bool{},
		envPrefix: SecretEnvPrefix,
	}
}

// LoadFile 读取 secrets.toml（形如 `openai_api_key = "sk-..."`）。
//
// 文件不存在不算错误（离线首启动）；权限过宽必须拒绝加载：密钥文件是 0600（附录 B）。
func (s *Secrets) LoadFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return domain.WrapError(domain.NewError(domain.CodePluginDenied, "无法读取密钥文件"), err)
	}
	// Windows 的 NTFS ACL 不映射到 Unix 权限位（os.Chmod 只影响只读位），
	// 因此只在 Unix 语义有效的平台上按 0600 判定；Windows 侧靠 ACL（文档 09 §9.4 的部署前提）。
	if needsUnixPermCheck() && info.Mode().Perm()&0o077 != 0 {
		return domain.NewError(domain.CodePluginDenied,
			"密钥文件权限过宽: "+filepath.Base(path)+" mode="+info.Mode().Perm().String()+"，必须是 0600")
	}
	var raw map[string]any
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return domain.WrapError(domain.NewError(domain.CodePluginDenied, "解析密钥文件失败"), err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, v := range raw {
		str, ok := v.(string)
		if !ok || strings.TrimSpace(str) == "" {
			continue
		}
		s.values[normalizeSecretName(name)] = str
	}
	return nil
}

// Lookup 按名字取密钥；环境变量优先于文件。found=false 表示未配置。
//
// 注意：这里不校验「谁在申请」，那是 Declared 的职责——宿主 API 两个都要过。
func (s *Secrets) Lookup(_ context.Context, name string) (string, bool, error) {
	key := normalizeSecretName(name)
	if env := os.Getenv(s.envPrefix + strings.ToUpper(key)); env != "" {
		return env, true, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[key]
	return v, ok, nil
}

// Declared 报告该插件清单是否声明了这个密钥名（越名即拒，12 §6.13）。
func (s *Secrets) Declared(_ context.Context, pluginID, name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set, ok := s.declared[pluginID]
	if !ok {
		return false
	}
	return set[normalizeSecretName(name)]
}

// SetDeclared 在装配期登记某插件可申请的密钥名（来自 manifest.backend.secrets）。
func (s *Secrets) SetDeclared(pluginID string, names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.declared[pluginID]
	if set == nil {
		set = map[string]bool{}
		s.declared[pluginID] = set
	}
	for _, n := range names {
		set[normalizeSecretName(n)] = true
	}
}

// Names 返回已加载的密钥名（不含值）：doctor 与设置页用「已配置 + 尾号」呈现（07 §7.7.6）。
func (s *Secrets) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.values))
	for k := range s.values {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Redact 把文本里出现的任何已知密钥值替换成 ***：日志与审计的最后一道兜底（09 §9.4）。
func (s *Secrets) Redact(_ context.Context, text string) string {
	if text == "" {
		return text
	}
	s.mu.RLock()
	vals := make([]string, 0, len(s.values))
	for _, v := range s.values {
		if len(v) >= 4 { // 太短的值会误伤正文，不参与脱敏。
			vals = append(vals, v)
		}
	}
	s.mu.RUnlock()
	for _, v := range vals {
		if strings.Contains(text, v) {
			text = strings.ReplaceAll(text, v, "***")
		}
	}
	return text
}

func normalizeSecretName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// 编译期确保接口签名没有漂移。
var _ = func(s *Secrets) ports.SecretBroker { return s }
