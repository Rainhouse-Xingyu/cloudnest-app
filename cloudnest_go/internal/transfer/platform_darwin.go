//go:build darwin

package transfer

import (
	"golang.org/x/sys/unix"
	"strings"
)

func remoteFilesystem(st unix.Statfs_t) bool {
	name := strings.TrimRight(string(st.Fstypename[:]), "\x00")
	return name == "nfs" || name == "smbfs" || name == "webdav"
}
