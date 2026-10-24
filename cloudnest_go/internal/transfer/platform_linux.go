//go:build linux

package transfer

import "golang.org/x/sys/unix"

func remoteFilesystem(st unix.Statfs_t) bool {
	return uint64(st.Type) == 0x6969 || uint64(st.Type) == 0xff534d42 || uint64(st.Type) == 0x517b
}
