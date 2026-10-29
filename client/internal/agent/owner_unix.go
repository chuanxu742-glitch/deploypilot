//go:build !windows

package agent

import (
	"os"
	"syscall"
)

// preserveOwner gives path the uid/gid recorded in info. It only succeeds when
// the process may chown (normally root), which is exactly when a rewrite would
// otherwise change the owner; other failures leave the default owner.
func preserveOwner(path string, info os.FileInfo) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		_ = os.Lchown(path, int(st.Uid), int(st.Gid))
	}
}
