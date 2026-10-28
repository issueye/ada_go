// 本文件实现 zip 安装（6.9 安装校验）：adag plugin install --zip。
//
//   - zip 文件 ≤ 20MB、条目 ≤ 500、解压总量 ≤ 50MB；超限明确报错而不是截断。
//   - 条目名拒绝绝对路径、盘符、UNC 与 .. 段；反斜杠先归一成 /（zip 规范分隔符）
//     再做同样的穿越检查（PowerShell 等工具会写出反斜杠分隔的条目名）。
//   - 清单必须在 zip 根目录或唯一顶层目录里（<top>/plugin.json）；其余布局拒绝。
//   - 先解压到 <workspace>/.adago/plugins/ 下的临时目录（同卷，保证原子移动），
//     清单校验通过且目标不存在后 os.Rename 到 <workspace>/.adago/plugins/<id>；
//     已存在同名 id 时报错，不覆盖（04 §5.7：安装只写插件目录）。
package discovery

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/adag/ada-go/internal/plugins"
)

// 安装上限（任务 M3 契约；docs 06 §6.9 的 50MB 解压上限）。
// 常量是文档化的默认值；实际判定走同名变量，测试可以用小阈值跑边界。
const (
	MaxZipBytes    = 20 << 20 // zip 文件大小上限
	MaxZipEntries  = 500      // zip 条目数上限
	MaxUnpackBytes = 50 << 20 // 解压总字节上限
)

var (
	maxZipBytes    = int64(MaxZipBytes)
	maxZipEntries  = MaxZipEntries
	maxUnpackBytes = int64(MaxUnpackBytes)
)

func shrinkZipLimits(zipBytes int64, entries int, unpackBytes int64) (restore func()) {
	oldZip, oldEntries, oldUnpack := maxZipBytes, maxZipEntries, maxUnpackBytes
	maxZipBytes, maxZipEntries, maxUnpackBytes = zipBytes, entries, unpackBytes
	return func() { maxZipBytes, maxZipEntries, maxUnpackBytes = oldZip, oldEntries, oldUnpack }
}

// InstallZip 把一个插件 zip 安装到 <workspaceDir>/.adago/plugins/<id>，返回插件 id。
//
// 校验全部通过才落位；任何一步失败都不留半个安装（临时目录清理，目标目录不动）。
func InstallZip(zipPath, workspaceDir string) (string, error) {
	if strings.TrimSpace(workspaceDir) == "" {
		return "", errors.New("discovery: 安装需要工作区目录")
	}
	absWS, err := filepath.Abs(workspaceDir)
	if err != nil {
		return "", fmt.Errorf("discovery: 解析工作区目录失败: %w", err)
	}
	fi, err := os.Stat(zipPath)
	if err != nil {
		return "", fmt.Errorf("discovery: 读取 zip 失败: %w", err)
	}
	if fi.Size() > maxZipBytes {
		return "", fmt.Errorf("discovery: zip 大小 %d 字节超过上限 %d", fi.Size(), maxZipBytes)
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("discovery: 打开 zip 失败: %w", err)
	}
	defer zr.Close()
	if len(zr.File) > maxZipEntries {
		return "", fmt.Errorf("discovery: zip 有 %d 个条目，超过上限 %d", len(zr.File), maxZipEntries)
	}
	if err := validateEntryNames(zr.File); err != nil {
		return "", err
	}
	prefix, err := manifestPrefix(zr.File)
	if err != nil {
		return "", err
	}

	pluginsRoot := WorkspaceRoot(absWS)
	if err := os.MkdirAll(pluginsRoot, 0o755); err != nil {
		return "", fmt.Errorf("discovery: 创建插件目录 %s 失败: %w", pluginsRoot, err)
	}
	tmpDir, err := os.MkdirTemp(pluginsRoot, ".install-")
	if err != nil {
		return "", fmt.Errorf("discovery: 创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := extract(zr.File, prefix, tmpDir); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(tmpDir, "plugin.json"))
	if err != nil {
		return "", fmt.Errorf("discovery: 解压后缺少 plugin.json: %w", err)
	}
	m, err := plugins.LoadManifest(raw)
	if err != nil {
		return "", fmt.Errorf("discovery: 插件清单非法: %w", err)
	}
	// 目标目录名就是插件 id：用 Validate(id) 复核「id 等于目录名」（6.2）。
	if err := m.Validate(m.ID); err != nil {
		return "", fmt.Errorf("discovery: 插件清单校验失败: %w", err)
	}
	target := filepath.Join(pluginsRoot, m.ID)
	if _, err := os.Lstat(target); err == nil {
		return "", fmt.Errorf("discovery: 插件 %s 已存在于 %s（不覆盖；先移除旧目录或改用新版本号重新打包）", m.ID, target)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("discovery: 检查目标目录失败: %w", err)
	}
	if err := os.Rename(tmpDir, target); err != nil {
		return "", fmt.Errorf("discovery: 落位插件目录失败: %w", err)
	}
	return m.ID, nil
}

// validateEntryNames 拒绝绝对路径、盘符、UNC 与 .. 段。
// 反斜杠先归一成 /（zip 规范分隔符）：PowerShell 等工具会写出反斜杠分隔的
// 条目名，归一后做同样的穿越检查，不放过任何一段 ".."。
func validateEntryNames(files []*zip.File) error {
	for _, f := range files {
		name := normalizeZipName(f.Name)
		if name == "" {
			return errors.New("discovery: zip 里有空条目名")
		}
		if isAbsoluteZipName(name) {
			return fmt.Errorf("discovery: zip 条目 %q 是绝对路径，拒绝安装", name)
		}
		if hasEscapeSegment(name) {
			return fmt.Errorf("discovery: zip 条目 %q 包含 .. 段，拒绝安装", name)
		}
	}
	return nil
}

// normalizeZipName 把 zip 条目名里的反斜杠归一成 zip 规范的 / 分隔符。
func normalizeZipName(name string) string {
	return strings.ReplaceAll(name, "\\", "/")
}

// manifestPrefix 返回需要剥掉的顶层前缀（"" 表示 zip 根布局）。
// 根目录有 plugin.json 优先；否则要求唯一顶层目录且里面有 plugin.json。
func manifestPrefix(files []*zip.File) (string, error) {
	hasRoot := false
	tops := map[string]bool{}
	for _, f := range files {
		name := strings.TrimPrefix(normalizeZipName(f.Name), "./")
		if name == "plugin.json" {
			hasRoot = true
			continue
		}
		top, _, found := strings.Cut(name, "/")
		if found && top != "" {
			tops[top] = true
		}
	}
	if hasRoot {
		return "", nil
	}
	if len(tops) == 0 {
		return "", errors.New("discovery: zip 里没有 plugin.json（要求根目录或唯一顶层目录里有它）")
	}
	if len(tops) > 1 {
		names := make([]string, 0, len(tops))
		for t := range tops {
			names = append(names, t)
		}
		return "", fmt.Errorf("discovery: zip 有多个顶层目录（%s）且根目录没有 plugin.json，布局非法", strings.Join(names, ", "))
	}
	for top := range tops {
		if findEntry(files, top+"/plugin.json") == "" {
			return "", fmt.Errorf("discovery: 唯一顶层目录 %s 里没有 plugin.json", top)
		}
		return top + "/", nil
	}
	return "", errors.New("discovery: zip 里没有 plugin.json")
}

func findEntry(files []*zip.File, name string) string {
	for _, f := range files {
		if strings.TrimPrefix(normalizeZipName(f.Name), "./") == name {
			return f.Name
		}
	}
	return ""
}

// extract 把条目解压到 dst（剥掉 prefix）；总解压字节超限即失败。
func extract(files []*zip.File, prefix, dst string) error {
	var total int64
	for _, f := range files {
		name := strings.TrimPrefix(normalizeZipName(f.Name), "./")
		if prefix != "" {
			if !strings.HasPrefix(name, prefix) {
				continue // 顶层目录布局：忽略 zip 根上与插件无关的散落文件
			}
			name = strings.TrimPrefix(name, prefix)
		}
		if name == "" {
			continue
		}
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 || mode&os.ModeDevice != 0 || mode&os.ModeNamedPipe != 0 {
			return fmt.Errorf("discovery: zip 条目 %q 不是普通文件，拒绝安装", f.Name)
		}
		rel := cleanZipRel(name)
		if rel == "." || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("discovery: zip 条目 %q 解析后越界，拒绝安装", f.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if !strings.HasPrefix(target, filepath.Clean(dst)+string(filepath.Separator)) && target != filepath.Clean(dst) {
			return fmt.Errorf("discovery: zip 条目 %q 落在临时目录之外，拒绝安装", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("discovery: 创建目录 %s 失败: %w", rel, err)
			}
			continue
		}
		if dir := filepath.Dir(target); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("discovery: 创建目录失败: %w", err)
			}
		}
		size := int64(f.UncompressedSize64)
		if total+size > maxUnpackBytes {
			return fmt.Errorf("discovery: 解压总量超过上限 %d 字节", maxUnpackBytes)
		}
		n, err := copyZipEntry(f, target)
		if err != nil {
			return err
		}
		total += n
		if total > maxUnpackBytes {
			return fmt.Errorf("discovery: 解压总量超过上限 %d 字节", maxUnpackBytes)
		}
	}
	return nil
}

// copyZipEntry 解压单个文件；实际解压字节数超过声明值同样拒绝（防解压炸弹）。
func copyZipEntry(f *zip.File, target string) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, fmt.Errorf("discovery: 打开 zip 条目 %q 失败: %w", f.Name, err)
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode().Perm()&0o700|0o400)
	if err != nil {
		return 0, fmt.Errorf("discovery: 写入 %s 失败: %w", filepath.Base(target), err)
	}
	defer out.Close()
	n, err := io.Copy(out, rc)
	if err != nil {
		return n, fmt.Errorf("discovery: 解压 %q 失败: %w", f.Name, err)
	}
	if declared := int64(f.UncompressedSize64); declared > 0 && n > declared {
		return n, fmt.Errorf("discovery: zip 条目 %q 实际解压 %d 字节，超过声明的 %d", f.Name, n, declared)
	}
	return n, nil
}
