//go:build linux || android

package sqlite

import (
	"fmt"
	"os"
	"syscall"
)

// statfs 魔数 → 介质判定（04 §5.7「Unix 用挂载点文件系统类型」的第一信号源）。
// 数值来源：Linux <linux/magic.h> 与 NFS/CIFS/SMB/9P/Lustre/GFS/OCFS2/CEPH/VBOXSF 文档。
var statfsMagics = map[int64]struct {
	kind    MediaVerdict
	name    string
	comment string
}{
	0x6e6f7370:     {MediaNetwork, "nfs", "NFS_SUPER_MAGIC"},
	0xff534d42:     {MediaNetwork, "cifs", "CIFS_MAGIC_NUMBER（SMB/CIFS 客户端）"},
	0x517b:         {MediaNetwork, "smb", "SMB_SUPER_MAGIC"},
	0x65616365:     {MediaNetwork, "samba", "SAMBA_SUPER_MAGIC"},
	0x01021997:     {MediaNetwork, "9p", "V9FS_MAGIC"},
	0x0bd00bd0:     {MediaNetwork, "lustre", "LUSTRE_FPRIVATE_DRIVE"},
	0x01161970:     {MediaNetwork, "gfs", "GFS_SUPER_MAGIC"},
	0x7461636f:     {MediaNetwork, "ocfs2", "OCFS2_SUPER_MAGIC（集群文件系统）"},
	0x00c36400:     {MediaNetwork, "ceph", "CEPH_SUPER_MAGIC"},
	0x6f736676:     {MediaNetwork, "vboxsf", "VBOXSF_SUPER_MAGIC（VirtualBox 共享文件夹）"},
	0x65736f6c6f75: {MediaUnknown, "fuse", "FUSE_SUPER_MAGIC：必须再看挂载表才知道是哪个网络后端"},
	0xef53:         {MediaLocal, "ext4", "EXT4_SUPER_MAGIC（ext2/3/4 共用）"},
	0x9123683e:     {MediaLocal, "btrfs", "BTRFS_SUPER_MAGIC"},
	0x58465342:     {MediaLocal, "xfs", "XFS_SB_MAGIC"},
	0x01021994:     {MediaLocal, "tmpfs", "TMPFS_MAGIC"},
}

// probeLocalMedia 是 Linux 的介质判定：statfs 魔数优先，其次 /proc/mounts 挂载表。
//
// 两个信号源都判不出来时返回 MediaUnknown（调用方按失败方向拒绝启动）：
// FUSE 类挂载（sshfs/rclone/glusterfs-fuse）只靠魔数不够，必须看 fstype 字符串；
// 挂载表读不到（容器里 /proc 被裁剪）同样只报 unknown，不猜测为本地。
func probeLocalMedia(probe string, info MediaInfo) (MediaInfo, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(probe, &st); err == nil {
		if entry, ok := statfsMagics[int64(st.Type)]; ok {
			info.FileSystem = entry.name
			switch entry.kind {
			case MediaNetwork:
				info.Reason = fmt.Sprintf("statfs 魔数 0x%x = %s（%s）", st.Type, entry.name, entry.comment)
				info.Verdict = MediaNetwork
				return info, nil
			case MediaLocal:
				// 本地魔数还要防「fuse 叠在本地栈上」的误判：FUSE 单独列在 unknown，
				// 所以这里可以直接结论；但挂载表若能给出更精确的 fstype，优先采信它。
				if m, ok := mountForPath(readProcMounts(), probe); ok &&
					classifyFileSystemName(m.FSType) == MediaNetwork {
					info.MountPoint = m.MountPoint
					info.Device = m.Device
					info.FileSystem = m.FSType
					info.Reason = fmt.Sprintf("挂载表显示 %s 是网络文件系统 %s（设备 %s）",
						m.MountPoint, m.FSType, m.Device)
					info.Verdict = MediaNetwork
					return info, nil
				}
				info.Verdict = MediaLocal
				info.Reason = fmt.Sprintf("statfs 魔数 0x%x = %s（%s）", st.Type, entry.name, entry.comment)
				return info, nil
			default:
				info.Reason = fmt.Sprintf("statfs 魔数 0x%x 需要挂载表进一步确认：%s", st.Type, entry.comment)
			}
		} else {
			info.Reason = fmt.Sprintf("未知 statfs 魔数 0x%x", st.Type)
		}
	} else {
		info.Reason = fmt.Sprintf("statfs(%s) 失败：%v", probe, err)
	}

	mounts := readProcMounts()
	if m, ok := mountForPath(mounts, probe); ok {
		info.MountPoint = m.MountPoint
		info.Device = m.Device
		info.FileSystem = m.FSType
		info.Verdict = classifyFileSystemName(m.FSType)
		switch info.Verdict {
		case MediaNetwork:
			info.Reason = fmt.Sprintf("挂载点 %s（设备 %s）是网络文件系统 %s", m.MountPoint, m.Device, m.FSType)
			return info, nil
		case MediaLocal:
			info.Reason = fmt.Sprintf("挂载点 %s 的 fstype=%s 属于本地文件系统", m.MountPoint, m.FSType)
			return info, nil
		}
	}
	info.Verdict = MediaUnknown
	return info, nil
}

// readProcMounts 读 /proc/mounts（读不到返回空表，由调用方退回其它信号源）。
func readProcMounts() []MountEntry {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseMountTable(f)
}
