// Package globalconfig 是插件机器级配置的通用存储（capability: global.config）。
//
// 每个插件一个 JSON 文件，命名空间按插件 id 隔离：
//
//	$ADAGO_HOME/config/<plugin-id>.json   或   ~/.adago/config/<plugin-id>.json
//
// 文件是带版本的 JSON 信封：
//
//	{ "version": 1, "values": { "providers": { ... }, "cron": [ ... ] } }
//
// 边界（09 §9.5、05 §6.2.1）：目录 0700、文件 0600、单值 ≤256KB、总量 ≤4MB、
// 原子替换、跨进程文件锁；脚本拿不到路径，只能经 ctx.config 读写自己的命名空间。
package globalconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/gofrs/flock"

	"github.com/adag/ada-go/internal/agent_core/plugins"
)

const (
	// FileVersion 是配置文件信封版本。
	FileVersion = 1
	// MaxKeyBytes 是单个 key 的长度上限。
	MaxKeyBytes = 128
	// MaxValueBytes 是单个 value 的 JSON 字节上限。
	MaxValueBytes = 256 << 10
	// MaxTotalBytes 是单个插件配置文件的总上限。
	MaxTotalBytes = 4 << 20
	// MaxEntries 是单个插件配置的条目数上限。
	MaxEntries = 1024
	// FileName 是配置目录名。
	DirName = "config"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)

type file struct {
	Version int                        `json:"version"`
	Values  map[string]json.RawMessage `json:"values"`
}

// Entry 是 list 返回的一项。
type Entry struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// Store 是一个插件命名空间的配置存储。
type Store struct {
	path string
}

// Root 返回配置根目录：$ADAGO_HOME/config 或 ~/.adago/config。
func Root() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("ADAGO_HOME")); dir != "" {
		return filepath.Join(dir, DirName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", errors.New("globalconfig: 无法定位用户主目录")
	}
	return filepath.Join(home, ".adago", DirName), nil
}

// Path 返回某插件的配置文件路径；插件 id 必须合法，防止路径穿越。
func Path(pluginID string) (string, error) {
	if !plugins.ValidID(pluginID) {
		return "", fmt.Errorf("globalconfig: 插件 id %q 非法", pluginID)
	}
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, pluginID+".json"), nil
}

// OpenWorkspace 打开工作区级配置命名空间：<workspace>/.adago/config/<pluginID>.json。
// 与机器级命名空间用同一文件格式与限额，只是根目录不同。
func OpenWorkspace(workspaceDir, pluginID string) (*Store, error) {
	if !plugins.ValidID(pluginID) {
		return nil, fmt.Errorf("globalconfig: 插件 id %q 非法", pluginID)
	}
	if strings.TrimSpace(workspaceDir) == "" {
		return nil, errors.New("globalconfig: 工作区目录为空")
	}
	abs, err := filepath.Abs(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("globalconfig: 解析工作区失败: %w", err)
	}
	return &Store{path: filepath.Join(abs, ".adago", DirName, pluginID+".json")}, nil
}

// OpenAt 在指定根目录下打开某插件命名空间（显式 --data-dir 的隔离运行用）。
func OpenAt(rootDir, pluginID string) (*Store, error) {
	if !plugins.ValidID(pluginID) {
		return nil, fmt.Errorf("globalconfig: 插件 id %q 非法", pluginID)
	}
	if strings.TrimSpace(rootDir) == "" {
		return nil, errors.New("globalconfig: 配置根目录为空")
	}
	return &Store{path: filepath.Join(rootDir, pluginID+".json")}, nil
}

// Open 打开一个插件命名空间；目录在首次写入时创建。
func Open(pluginID string) (*Store, error) {
	path, err := Path(pluginID)
	if err != nil {
		return nil, err
	}
	return &Store{path: path}, nil
}

// Path 返回配置文件路径（诊断用）。`nfunc (s *Store) Path() string { return s.path }`n`n// Path 返回配置文件路径（诊断用）。
func (s *Store) Path() string { return s.path }

// Get 读取一个 key；不存在返回 found=false，第二返回值为 nil。
func (s *Store) Get(key string) (value json.RawMessage, found bool, err error) {
	if err := validateKey(key); err != nil {
		return nil, false, err
	}
	f, err := s.load()
	if err != nil {
		return nil, false, err
	}
	raw, ok := f.Values[key]
	if !ok {
		return nil, false, nil
	}
	return append(json.RawMessage(nil), raw...), true, nil
}

// Set 写入一个 key（值必须是合法 JSON）。
func (s *Store) Set(key string, value json.RawMessage) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if err := validateValue(value); err != nil {
		return err
	}
	return s.mutate(func(f *file) error {
		f.Values[key] = append(json.RawMessage(nil), value...)
		return nil
	})
}

// Delete 删除一个 key；第二返回值表示是否真的存在。
func (s *Store) Delete(key string) (bool, error) {
	if err := validateKey(key); err != nil {
		return false, err
	}
	f, err := s.load()
	if err != nil {
		return false, err
	}
	if _, ok := f.Values[key]; !ok {
		return false, nil
	}
	err = s.mutate(func(f *file) error {
		delete(f.Values, key)
		return nil
	})
	return err == nil, err
}

// List 返回按 key 排序的条目；prefix 为空表示全部。
func (s *Store) List(prefix string) ([]Entry, error) {
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(f.Values))
	for key, value := range f.Values {
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			continue
		}
		out = append(out, Entry{Key: key, Value: append(json.RawMessage(nil), value...)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *Store) load() (*file, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &file{Version: FileVersion, Values: map[string]json.RawMessage{}}, nil
		}
		return nil, fmt.Errorf("globalconfig: 读取 %s 失败: %w", s.path, err)
	}
	if len(raw) > MaxTotalBytes+64<<10 {
		return nil, fmt.Errorf("globalconfig: %s 超过 %d 字节上限", s.path, MaxTotalBytes)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("globalconfig: %s 不是合法配置: %w", s.path, err)
	}
	if f.Values == nil {
		f.Values = map[string]json.RawMessage{}
	}
	if f.Version == 0 {
		f.Version = FileVersion
	}
	return &f, nil
}

func (s *Store) mutate(fn func(*file) error) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("globalconfig: 创建目录失败: %w", err)
	}
	lock := flock.New(s.path + ".lock")
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("globalconfig: 获取配置锁失败: %w", err)
	}
	defer func() { _ = lock.Unlock() }()
	f, err := s.load()
	if err != nil {
		return err
	}
	if err := fn(f); err != nil {
		return err
	}
	if err := validateFile(f); err != nil {
		return err
	}
	return saveAtomic(s.path, f)
}

func validateFile(f *file) error {
	if f == nil {
		return errors.New("globalconfig: 配置为空")
	}
	if len(f.Values) > MaxEntries {
		return fmt.Errorf("globalconfig: 条目数超过 %d", MaxEntries)
	}
	total := 0
	for key, value := range f.Values {
		if err := validateKey(key); err != nil {
			return err
		}
		if err := validateValue(value); err != nil {
			return err
		}
		total += len(key) + len(value)
	}
	if total > MaxTotalBytes {
		return fmt.Errorf("globalconfig: 配置总量超过 %d 字节", MaxTotalBytes)
	}
	return nil
}

func validateKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("globalconfig: key 不能为空")
	}
	if len(key) > MaxKeyBytes {
		return fmt.Errorf("globalconfig: key 超过 %d 字节", MaxKeyBytes)
	}
	if !keyPattern.MatchString(key) || strings.Contains(key, "..") {
		return fmt.Errorf("globalconfig: key %q 非法（匹配 ^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$，不能含 ..）", key)
	}
	return nil
}

func validateValue(value json.RawMessage) error {
	if len(value) == 0 {
		return errors.New("globalconfig: value 不能为空")
	}
	if len(value) > MaxValueBytes {
		return fmt.Errorf("globalconfig: value 超过 %d 字节", MaxValueBytes)
	}
	if !json.Valid(value) {
		return errors.New("globalconfig: value 不是合法 JSON")
	}
	return nil
}

func saveAtomic(path string, f *file) error {
	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("globalconfig: 序列化失败: %w", err)
	}
	body = append(body, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("globalconfig: 创建目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("globalconfig: 创建临时文件失败: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("globalconfig: 设置权限失败: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("globalconfig: 写入失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("globalconfig: 关闭临时文件失败: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("globalconfig: 替换 %s 失败: %w", path, err)
	}
	_ = os.Chmod(path, 0o600)
	return nil
}
