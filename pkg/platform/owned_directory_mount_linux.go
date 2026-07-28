//go:build linux

package platform

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const ownedDirectoryFDInfoLimit = 8 * 1024

func ownedDirectoryMountIdentityForHandle(file *os.File, info os.FileInfo) (ownedDirectoryMountIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return ownedDirectoryMountIdentity{}, errors.New("directory device identity is unavailable")
	}
	fdInfo, err := os.Open(fmt.Sprintf("/proc/self/fdinfo/%d", file.Fd()))
	if err != nil {
		return ownedDirectoryMountIdentity{}, fmt.Errorf("open directory mount identity: %w", err)
	}
	content, readErr := io.ReadAll(io.LimitReader(fdInfo, ownedDirectoryFDInfoLimit+1))
	closeErr := fdInfo.Close()
	if readErr != nil || closeErr != nil {
		return ownedDirectoryMountIdentity{}, errors.Join(readErr, closeErr)
	}
	if len(content) > ownedDirectoryFDInfoLimit {
		return ownedDirectoryMountIdentity{}, errors.New("directory mount identity exceeds its read bound")
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "mnt_id:" {
			continue
		}
		mount, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || mount == 0 {
			return ownedDirectoryMountIdentity{}, errors.New("directory mount identity is malformed")
		}
		return ownedDirectoryMountIdentity{
			device:     uint64(stat.Dev),
			mount:      mount,
			mountKnown: true,
		}, nil
	}
	return ownedDirectoryMountIdentity{}, errors.New("directory mount identity is unavailable")
}

func ownedDirectoryEntryIsFilesystemLink(os.FileInfo) bool {
	return false
}
