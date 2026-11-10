// Package envstore 是用户管理的环境变量存储（环境变量模块）。
//
// 两层文件，工作区覆盖全局：
//   - 全局：~/.adago/env.json（ADAGO_HOME 优先，与 plugins.json 同一约定）
//   - 工作区：<workspace>/.adago/env.json
//
// 文件是带版本的 JSON 信封；为了手改方便也接受扁平对象（键值都是字符串）：
//
//	{ "version": 1, "vars": { "GITHUB_TOKEN": "..." } }
//	{ "GITHUB_TOKEN": "..." }
//
// 这些变量由宿主注入插件启动的子进程（ctx.exec / 后台任务 / ctx.process.start），
// 同名覆盖宿主环境；它们不进入插件的 process.env（那里仍按清单 backend.env 白名单）。
// 值不写日志：本包的错误与警告只出现路径与键名。
package envstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// FileName 是环境变量文件名（全局与工作区同名）。
	FileName = "env.json"
	// FileVersion 是文件信封版本。
	FileVersion = 1
	// MaxKeyBytes 是单个变量名的长度上限。
	MaxKeyBytes = 128
	// MaxValueBytes 是单个变量值的长度上限。
	MaxValueBytes = 32 << 10
	// MaxFileBytes 是单个 env.json 的大小上限。
	MaxFileBytes = 256 << 10
	// WorkspaceDirName 是工作区本地数据目录。
	WorkspaceDirName = ".adago"
	// legacyWorkspaceDirName 是旧工作区目录（只读兼容，写入仍落到新目录）。
	legacyWorkspaceDirName = ".adag"
)

// ReservedPrefix 是宿主保留的环境变量前缀（与清单 backend.env 的规则一致）：
// 用户变量不允许使用，避免在子进程里伪造宿主开关。
const ReservedPrefix = "ADAG_"

// keyPattern 是合法的环境变量名：字母或下划线开头，后接字母/数字/下划线。
var keyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// file 是 env.json 的信封形状。
type file struct {
	Version int               `json:"version"`
	Vars    map[string]string `json:"vars"`
}

// GlobalDir 返回用户级数据目录：ADAGO_HOME 优先，否则 ~/.adago。
func GlobalDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("ADAGO_HOME")); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("envstore: 取用户主目录失败: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("envstore: 用户主目录为空")
	}
	return filepath.Join(home, ".adago"), nil
}

// GlobalPath 返回用户级 env.json 路径：ADAGO_HOME 优先，否则 ~/.adago/env.json。
func GlobalPath() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

// WorkspacePath 返回工作区 env.json 路径（工作区先转绝对路径）。
func WorkspacePath(workspace string) (string, error) {
	abs, err := workspaceAbs(workspace)
	if err != nil {
		return "", err
	}
	return filepath.Join(abs, WorkspaceDirName, FileName), nil
}

// legacyWorkspacePath 返回旧工作区 env.json 路径（只读兼容）。
func legacyWorkspacePath(workspace string) (string, error) {
	abs, err := workspaceAbs(workspace)
	if err != nil {
		return "", err
	}
	return filepath.Join(abs, legacyWorkspaceDirName, FileName), nil
}

func workspaceAbs(workspace string) (string, error) {
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		return "", errors.New("envstore: 需要工作区目录")
	}
	abs, err := filepath.Abs(ws)
	if err != nil {
		return "", fmt.Errorf("envstore: 解析工作区 %q 失败: %w", ws, err)
	}
	return abs, nil
}

// WorkspaceReadPath 选择工作区 env.json：新目录优先；新文件不存在时回落到旧
// .adag/env.json（返回 legacy=true）。两者都不存在时用新路径（读取为空）。
func WorkspaceReadPath(workspace string) (path string, legacy bool, err error) {
	newPath, err := WorkspacePath(workspace)
	if err != nil {
		return "", false, err
	}
	if _, statErr := os.Stat(newPath); statErr == nil {
		return newPath, false, nil
	}
	oldPath, err := legacyWorkspacePath(workspace)
	if err != nil {
		return "", false, err
	}
	if _, statErr := os.Stat(oldPath); statErr == nil {
		return oldPath, true, nil
	}
	return newPath, false, nil
}

// MigrateWorkspace 在写入前把旧 .adag/env.json 物化到新目录：换目录后第一次
// set/unset 不会丢掉旧变量，后续写入只落到 .adago/env.json。
func MigrateWorkspace(workspace string) error {
	newPath, err := WorkspacePath(workspace)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(newPath); statErr == nil {
		return nil
	}
	oldPath, err := legacyWorkspacePath(workspace)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(oldPath); statErr != nil {
		return nil
	}
	vars, _, err := LoadFile(oldPath)
	if err != nil {
		return err
	}
	return SaveFile(newPath, vars)
}

// ValidateKey 校验变量名。返回的错误可以安全展示（不含值）。
func ValidateKey(key string) error {
	if key == "" {
		return errors.New("变量名不能为空")
	}
	if len(key) > MaxKeyBytes {
		return fmt.Errorf("变量名超过 %d 字节", MaxKeyBytes)
	}
	if strings.ContainsAny(key, "=\x00") || strings.TrimSpace(key) != key {
		return fmt.Errorf("变量名 %q 非法（不能含 = 或 NUL，首尾不能有空白）", key)
	}
	if strings.HasPrefix(key, ReservedPrefix) {
		return fmt.Errorf("变量名 %q 使用宿主保留前缀 %q", key, ReservedPrefix)
	}
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("变量名 %q 非法（需匹配 ^[A-Za-z_][A-Za-z0-9_]*$）", key)
	}
	return nil
}

// ValidateValue 校验变量值（允许空串；不允许 NUL 且不超过上限）。
func ValidateValue(value string) error {
	if len(value) > MaxValueBytes {
		return fmt.Errorf("变量值超过 %d 字节", MaxValueBytes)
	}
	if strings.ContainsRune(value, 0) {
		return errors.New("变量值不能包含 NUL")
	}
	return nil
}

// ParseAssignment 解析命令行/表单里的 KEY=VALUE（只在第一个 = 处切分，值原样保留）。
func ParseAssignment(arg string) (string, string, error) {
	i := strings.IndexByte(arg, '=')
	if i <= 0 {
		return "", "", fmt.Errorf("%q 不是 KEY=VALUE 形式", arg)
	}
	key, value := arg[:i], arg[i+1:]
	if err := ValidateKey(key); err != nil {
		return "", "", err
	}
	if err := ValidateValue(value); err != nil {
		return "", "", err
	}
	return key, value, nil
}

// LoadFile 读取一份 env.json：文件不存在返回空 map。第二返回值是跳过项的警告
// （只含路径与键名，不含值）。
//
// 兼容两种形状：带 vars 的信封，以及「顶层键值都是字符串」的扁平对象。
func LoadFile(path string) (map[string]string, []string, error) {
	vars := map[string]string{}
	if strings.TrimSpace(path) == "" {
		return vars, nil, errors.New("envstore: 需要文件路径")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return vars, nil, nil
		}
		return vars, nil, fmt.Errorf("envstore: 读取 %s 失败: %w", path, err)
	}
	if len(raw) > MaxFileBytes {
		return vars, nil, fmt.Errorf("envstore: %s 超过 %d 字节上限", path, MaxFileBytes)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return vars, nil, fmt.Errorf("envstore: %s 不是合法 JSON: %w", path, err)
	}

	// 信封形状：vars 是对象。
	if _, ok := top["vars"]; ok {
		var body struct {
			Version int               `json:"version"`
			Vars    map[string]string `json:"vars"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return vars, nil, fmt.Errorf("envstore: %s 的 vars 必须是字符串键值对象: %w", path, err)
		}
		warns := versionWarning(path, body.Version)
		keys := sortedKeys(body.Vars)
		for _, key := range keys {
			if w, ok := accept(vars, key, body.Vars[key]); !ok {
				warns = append(warns, fmt.Sprintf("%s: %s", path, w))
			}
		}
		return vars, warns, nil
	}

	// 扁平形状：所有字符串值都是变量；其它类型跳过并警告。
	warns := []string{}
	for key, rawValue := range top {
		if key == "version" {
			continue
		}
		var value string
		if err := json.Unmarshal(rawValue, &value); err != nil {
			warns = append(warns, fmt.Sprintf("%s: 忽略非字符串值 %s", path, key))
			continue
		}
		if w, ok := accept(vars, key, value); !ok {
			warns = append(warns, fmt.Sprintf("%s: %s", path, w))
		}
	}
	return vars, warns, nil
}

// LoadForWorkspace 合并全局与工作区变量（工作区同名覆盖全局），并汇总警告。
// 单层读取失败只记警告：环境变量坏了不应该让装配失败。
func LoadForWorkspace(workspace string) (map[string]string, []string) {
	vars := map[string]string{}
	var warns []string
	type layer struct {
		label string
		path  string
	}
	var layers []layer
	if path, err := GlobalPath(); err != nil {
		warns = append(warns, "全局环境变量不可用: "+err.Error())
	} else {
		layers = append(layers, layer{label: "全局", path: path})
	}
	if ws := strings.TrimSpace(workspace); ws != "" {
		path, legacy, err := WorkspaceReadPath(ws)
		if err != nil {
			warns = append(warns, "工作区环境变量不可用: "+err.Error())
		} else {
			if legacy {
				warns = append(warns, "工作区环境变量读取自旧目录 .adag/env.json；写入会落到 .adago/env.json")
			}
			layers = append(layers, layer{label: "工作区", path: path})
		}
	}
	for _, layer := range layers {
		loaded, layerWarns, err := LoadFile(layer.path)
		if err != nil {
			warns = append(warns, layer.label+"环境变量读取失败: "+err.Error())
			continue
		}
		warns = append(warns, layerWarns...)
		for key, value := range loaded {
			vars[key] = value
		}
	}
	return vars, warns
}

// SaveFile 写一份 env.json：先校验全部键值，再原子替换（0600，目录 0700）。
func SaveFile(path string, vars map[string]string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("envstore: 需要文件路径")
	}
	clean := make(map[string]string, len(vars))
	for key, value := range vars {
		if err := ValidateKey(key); err != nil {
			return err
		}
		if err := ValidateValue(value); err != nil {
			return err
		}
		clean[key] = value
	}
	body, err := json.MarshalIndent(file{Version: FileVersion, Vars: clean}, "", "  ")
	if err != nil {
		return fmt.Errorf("envstore: 序列化失败: %w", err)
	}
	body = append(body, '\n')
	if len(body) > MaxFileBytes {
		return fmt.Errorf("envstore: 内容超过 %d 字节上限", MaxFileBytes)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("envstore: 创建目录 %s 失败: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, FileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("envstore: 创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("envstore: 设置权限失败: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("envstore: 写入失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("envstore: 关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("envstore: 替换 %s 失败: %w", path, err)
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

// Set 读取、设置一个变量并写回；返回写回后的完整集合。
func Set(path, key, value string) (map[string]string, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	if err := ValidateValue(value); err != nil {
		return nil, err
	}
	vars, _, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	vars[key] = value
	if err := SaveFile(path, vars); err != nil {
		return nil, err
	}
	return vars, nil
}

// Unset 删除一个变量并写回；第二个返回值表示是否真的删除了。
func Unset(path, key string) (map[string]string, bool, error) {
	if err := ValidateKey(key); err != nil {
		return nil, false, err
	}
	vars, _, err := LoadFile(path)
	if err != nil {
		return nil, false, err
	}
	if _, ok := vars[key]; !ok {
		return vars, false, nil
	}
	delete(vars, key)
	if err := SaveFile(path, vars); err != nil {
		return nil, false, err
	}
	return vars, true, nil
}

// accept 校验并登记一条变量；返回的警告不含值。
func accept(dst map[string]string, key, value string) (string, bool) {
	if err := ValidateKey(key); err != nil {
		return "忽略 " + err.Error(), false
	}
	if err := ValidateValue(value); err != nil {
		return "忽略变量 " + key + "：" + err.Error(), false
	}
	dst[key] = value
	return "", true
}

func versionWarning(path string, version int) []string {
	if version == FileVersion || version == 0 {
		return nil
	}
	return []string{fmt.Sprintf("%s: 未知版本 %d，按 %d 解析", path, version, FileVersion)}
}

func sortedKeys(vars map[string]string) []string {
	keys := make([]string, 0, len(vars))
	for key := range vars {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
