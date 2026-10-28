package sqlite

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/adag/ada-go/internal/domain"
)

// MediaVerdict 是「state.db 所在存储介质」的判定结论（04 §5.7 部署约束）。
type MediaVerdict int

const (
	// MediaLocal 本地文件系统：WAL 的共享内存与文件锁可靠，允许启动。
	MediaLocal MediaVerdict = iota
	// MediaNetwork SMB/NFS/WebDAV/SSHFS 一类网络盘：必须拒绝启动。
	MediaNetwork
	// MediaUnknown 平台/挂载信息不足，判不出来：按失败方向同样拒绝启动（04 §5.7）。
	MediaUnknown
)

// String 输出人话（doctor 直接打印）。
func (v MediaVerdict) String() string {
	switch v {
	case MediaLocal:
		return "local"
	case MediaNetwork:
		return "network"
	default:
		return "unknown"
	}
}

// MediaInfo 是一次介质判定的结果。
type MediaInfo struct {
	// Path 是被判定的路径（可能是它的某个已存在祖先目录）。
	Path string
	// Verdict 是结论。
	Verdict MediaVerdict
	// FileSystem 是文件系统类型名（NTFS / nfs4 / CIFS…），可为空。
	FileSystem string
	// MountPoint 是匹配到的挂载点，可为空。
	MountPoint string
	// Device 是挂载设备/远端（//host/share 等），可为空。
	Device string
	// Reason 是人话说明（拒绝启动时进错误 detail）。
	Reason string
}

// MediaError 是「数据目录不在本地文件系统」或「判不出来」的拒绝启动错误（04 §5.7）。
//
// doctor 用 LocalMedia 直接拿判定结果报告；Core 启动路径把它当拒绝（不写库、不迁移）。
type MediaError struct{ Info MediaInfo }

func (e *MediaError) Error() string {
	return fmt.Sprintf("工作区数据目录不可安全使用（%s，判定=%s）：%s",
		e.Info.Path, e.Info.Verdict, e.Info.Reason)
}

// Domain 转业务错误：网络盘与「判不出来」都走 storage_corrupt（5004，不可重试）。
// 选它的理由：04 §5.7 要求这种情况「拒绝启动或由 doctor 报错」，而 5004 的下一步
// 正是 doctor（09 §10 把网络盘检测列为 doctor 检查项）；5003 storage_busy 是可重试码，
// 用它会让 Gateway 反复重启 Core。见 doc 的「不清楚的设计点」汇报条目。
func (e *MediaError) Domain() *domain.Error { return storageCorrupt(e.Error(), nil) }

// networkFileSystemNames 是「一眼判定为网络盘」的文件系统名（大小写不敏感、允许后缀匹配）。
// 来源：SMB/CIFS 客户端、NFS、WebDAV、SSHFS/SFTP、GlusterFS、Lustre、Ceph、9P、rclone、DFS 命名空间。
var networkFileSystemNames = []string{
	"cifs", "smb", "smbfs", "smb2", "ncpfs", "nfs", "nfs4", "nfsv3", "nfsv4",
	"webdav", "davfs", "dav", "rdfs",
	"sshfs", "sftp", "fuse.sshfs", "fuse.sftp", "fuse.rclone", "rclone",
	"glusterfs", "glusterfs-fuse", "fuse.glusterfs",
	"lustre", "lfsv3", "ceph", "cephfs", "fuse.ceph", "fuse.cephfs",
	"9p", "9pcs", "v9fs", "overlay9p", "virtiofs",
	"afs", "openafs", "ocfs2", "gfs", "gfs2", "vboxsf",
	"netdrive", "network", "remotedisk", "remotefs", "npfs",
}

// localFileSystemNames 是「一眼判定为本地盘」的文件系统名。
// 注意 virtiofs 与 overlay 这类「可能是共享/透传」的介质不放这里，交给网络表或 unknown。
var localFileSystemNames = []string{
	"ntfs", "exfat", "fat", "fat32", "msdos", "reiserfs", "jfs", "xfs", "btrfs",
	"ext2", "ext3", "ext4", "zfs", "apfs", "hfs", "hfsplus", "tmpfs", "devtmpfs",
	"ramfs", "squashfs", "ramdisk", "csvfs", "refs", "afs_local", "proc", "sysfs",
}

// classifyFileSystemName 把文件系统类型名判成 local/network/unknown（纯函数，可单测）。
func classifyFileSystemName(name string) MediaVerdict {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return MediaUnknown
	}
	base := n
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	for _, net := range networkFileSystemNames {
		if base == net || strings.HasSuffix(base, "."+net) || strings.HasPrefix(base, net+"-") {
			return MediaNetwork
		}
	}
	for _, loc := range localFileSystemNames {
		if base == loc {
			return MediaLocal
		}
	}
	return MediaUnknown
}

// MountEntry 是挂载表的一行（Linux /proc/mounts 或 BSD mount 输出）。
type MountEntry struct {
	Device     string
	MountPoint string
	FSType     string
}

// parseMountTable 解析「device mountpoint fstype 其余」形式的挂载表（纯函数，可单测）。
// 字段用空白分隔；挂载点里的八进制转义（\040 表空格）按 /proc/mounts 规则还原。
func parseMountTable(r io.Reader) []MountEntry {
	var out []MountEntry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		out = append(out, MountEntry{
			Device:     unescapeMount(f[0]),
			MountPoint: unescapeMount(f[1]),
			FSType:     unescapeMount(f[2]),
		})
	}
	return out
}

// unescapeMount 还原 /proc/mounts 的八进制转义（\040=空格、\011=tab、\012=\n、\134=\）。
func unescapeMount(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			o1, o2, o3 := s[i+1]-'0', s[i+2]-'0', s[i+3]-'0'
			if o1 <= 7 && o2 <= 7 && o3 <= 7 {
				b.WriteByte(o1*64 + o2*8 + o3)
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// mountForPath 选出挂载点最长匹配（纯函数，可单测）。找不到返回 ok=false。
func mountForPath(mounts []MountEntry, path string) (MountEntry, bool) {
	best := MountEntry{}
	bestLen := -1
	norm := normalizeForCompare(path)
	for _, m := range mounts {
		mn := normalizeForCompare(m.MountPoint)
		if mn == "" {
			continue
		}
		if mn == "/" || mn == "\\" {
			if len("/") > bestLen {
				best, bestLen = m, len("/")
			}
			continue
		}
		if norm == mn || strings.HasPrefix(norm, mn+"/") {
			if len(mn) > bestLen {
				best, bestLen = m, len(mn)
			}
		}
	}
	if bestLen < 0 {
		return MountEntry{}, false
	}
	return best, true
}

// normalizeForCompare 统一分隔符并去掉尾斜杠，便于挂载点前缀比较。
func normalizeForCompare(p string) string {
	s := filepath.ToSlash(filepath.Clean(p))
	if len(s) > 1 {
		s = strings.TrimRight(s, "/")
	}
	if runtime.GOOS == "windows" {
		s = strings.ToLower(s)
	}
	return s
}

// looksLikeUNC 判定 Windows UNC 路径（\\server\share\…）——它必然是网络盘。
func looksLikeUNC(path string) bool {
	p := filepath.ToSlash(strings.TrimSpace(path))
	return strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "///")
}

// LocalMedia 判定给定路径是否在本地文件系统上（04 §5.7 部署约束）。
//
// 路径不必存在：会向上找到最近的已存在祖先再做判定。返回的 MediaInfo 永远可用，
// err 只在「连祖先目录都不存在/无法 stat」时非空（调用方按失败方向拒绝）。
func LocalMedia(path string) (MediaInfo, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return MediaInfo{}, fmt.Errorf("sqlite: 无法归一化数据目录 %s: %w", path, err)
	}
	probe := existingAncestor(abs)
	info := MediaInfo{Path: abs}
	if runtime.GOOS == "windows" && (looksLikeUNC(abs) || looksLikeUNC(probe)) {
		info.Verdict = MediaNetwork
		info.Device = probe
		info.Reason = "UNC 路径（SMB/DFS 命名空间）：WAL 的共享内存与文件锁在网络盘上不可靠"
		return info, nil
	}
	return probeLocalMedia(probe, info)
}

// existingAncestor 返回 path 或其最近的已存在祖先目录（用于对尚未创建的 dataDir 做判定）。
func existingAncestor(path string) string {
	cur := filepath.Clean(path)
	for {
		if fi, err := os.Stat(cur); err == nil && fi.IsDir() {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return cur
		}
		cur = parent
	}
}

// EnsureLocalDataDir 是 Core 启动前的闸门：本地盘返回 nil，网络盘或判不出来返回
// *MediaError（调用方据此拒绝启动，不打开 state.db）。
func EnsureLocalDataDir(dataDir string) error {
	info, err := LocalMedia(dataDir)
	if err != nil {
		return storageCorrupt(fmt.Sprintf("无法确认数据目录的存储介质，按失败方向拒绝启动：%v", err), err)
	}
	switch info.Verdict {
	case MediaLocal:
		return nil
	case MediaNetwork:
		return &MediaError{Info: info}
	default:
		unknown := info
		unknown.Reason = fmt.Sprintf("当前平台 %s/%s 无法判定该路径的存储介质（%s）。"+
			"WAL 依赖可靠的文件锁与共享内存，判不出来就拒绝启动（04 §5.7）",
			runtime.GOOS, runtime.GOARCH, info.Reason)
		return &MediaError{Info: unknown}
	}
}
