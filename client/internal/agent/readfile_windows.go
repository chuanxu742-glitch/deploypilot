//go:build windows

package agent

import (
	"fmt"
	"os"
)

func openReadFile(path string) (*os.File, error) {
	// Windows has no O_NONBLOCK file-open flag. Reject known special paths
	// before opening; ReadFile also checks the opened handle's file type.
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return nil, fmt.Errorf("%s is not a regular file; read_file does not read devices or pipes", path)
	}
	return os.Open(path)
}
