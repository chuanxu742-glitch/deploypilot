//go:build windows

package agent

import "os"

// preserveOwner is a no-op on Windows: a renamed file keeps the directory's
// inherited ACLs and there is no uid/gid to restore.
func preserveOwner(string, os.FileInfo) {}
