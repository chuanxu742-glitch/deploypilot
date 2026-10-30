//go:build !windows

package console

import "os"

// enableANSI needs no setup outside Windows; only a dumb terminal lacks colour.
func enableANSI(*os.File) (bool, func()) {
	return os.Getenv("TERM") != "dumb", func() {}
}

// narrowBlocks is true for the Unix terminals DeployPilot targets.
func narrowBlocks(*os.File) bool { return true }
