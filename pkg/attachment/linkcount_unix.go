//go:build !windows

package attachment

import (
	"os"
	"syscall"
)

func regularFileLinkCount(file *os.File, info os.FileInfo) (uint64, bool) {
	if file == nil || info == nil {
		return 0, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Nlink), true
}
