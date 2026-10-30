package console

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareNonTerminalIsPlain(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	caps, restore := Prepare(f)
	defer restore()
	if caps.Terminal || caps.Color || caps.Width != 0 {
		t.Fatalf("a file is not a terminal: %+v", caps)
	}
	if caps.QR() != QRHalfBlocks {
		t.Fatalf("logs keep the compact QR code, got %v", caps.QR())
	}
}

func TestQRModeFallsBackWhenBlocksAreWide(t *testing.T) {
	cases := []struct {
		caps Caps
		want QRMode
	}{
		{Caps{Terminal: true, Color: true, NarrowBlocks: true}, QRHalfBlocks},
		{Caps{Terminal: true, Color: false, NarrowBlocks: true}, QRHalfBlocks},
		{Caps{Terminal: true, Color: true, NarrowBlocks: false}, QRColorCells},
		{Caps{Terminal: true, Color: false, NarrowBlocks: false}, QRNone},
	}
	for _, c := range cases {
		if got := c.caps.QR(); got != c.want {
			t.Errorf("%+v: got %v, want %v", c.caps, got, c.want)
		}
	}
}
