//go:build windows

package console

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// enableANSI turns on virtual terminal processing. It fails on consoles that
// predate Windows 10 1511 or run in "legacy console" mode; those get plain text.
func enableANSI(f *os.File) (bool, func()) {
	handle := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return false, func() {}
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true, func() {}
	}
	if err := windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		return false, func() {}
	}
	return true, func() { _ = windows.SetConsoleMode(handle, mode) }
}

// narrowBlocks measures how far the console cursor moves for one block
// character and erases it again. Whether ▀ takes one or two cells depends on
// the console host, its version, font and code page, so measuring is more
// reliable than guessing from environment variables that child windows inherit.
func narrowBlocks(f *os.File) bool {
	handle := windows.Handle(f.Fd())
	before, ok := cursorX(handle)
	if !ok {
		return true
	}
	if _, err := f.WriteString("▀"); err != nil {
		return true
	}
	after, ok := cursorX(handle)
	if !ok || after <= before {
		// Wrapped to the next line or unreadable: assume the common case.
		return true
	}
	width := after - before
	back := strings.Repeat("\b", width)
	_, _ = f.WriteString(back + strings.Repeat(" ", width) + back)
	return width == 1
}

func cursorX(handle windows.Handle) (int, bool) {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(handle, &info); err != nil {
		return 0, false
	}
	return int(info.CursorPosition.X), true
}
