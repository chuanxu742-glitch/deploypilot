package outputstream

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestValidUnicodeAcrossEverySplit(t *testing.T) {
	text := "prefix é中🌍 𐍈 e\u0301 end\r\n"
	for split := 0; split <= len(text); split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			var got strings.Builder
			stream := New(func(value string) {
				if !utf8.ValidString(value) {
					t.Errorf("invalid callback: %q", value)
				}
				got.WriteString(value)
			})
			stream.Push(text[:split])
			stream.Push("")
			stream.Push(text[split:])
			stream.Close()
			if got.String() != text {
				t.Fatalf("split %d: %q", split, got.String())
			}
		})
	}
}

func TestBytewiseAndIncompleteCompletion(t *testing.T) {
	for _, text := range []string{"é中🌍", "hello", "", "bad\xffbytes", "partial\xe4\xb8"} {
		t.Run(fmt.Sprintf("%x", text), func(t *testing.T) {
			var got strings.Builder
			stream := New(func(value string) {
				if !utf8.ValidString(value) {
					t.Fatal("invalid callback")
				}
				got.WriteString(value)
			})
			for index := 0; index < len(text); index++ {
				stream.Push(text[index : index+1])
			}
			stream.Close()
			stream.Close()
			stream.Push("ignored")
			if want := strings.ToValidUTF8(text, "\uFFFD"); got.String() != want {
				t.Fatalf("got %q want %q", got.String(), want)
			}
		})
	}
}

func TestASCIIIsNotBufferedBehindCompletion(t *testing.T) {
	var got strings.Builder
	stream := New(func(value string) { got.WriteString(value) })
	stream.Push("ready\n")
	if got.String() != "ready\n" {
		t.Fatal("ordinary output was delayed")
	}
	stream.Push("\xe4")
	if got.String() != "ready\n" {
		t.Fatal("incomplete character emitted")
	}
	stream.Push("\xb8\xad")
	if got.String() != "ready\n中" {
		t.Fatal("completed character not emitted")
	}
	stream.Close()
}

func TestConcurrentPushAndCloseRemainValid(t *testing.T) {
	var got strings.Builder
	stream := New(func(value string) {
		if !utf8.ValidString(value) {
			t.Error("invalid callback")
		}
		got.WriteString(value)
	})
	var workers sync.WaitGroup
	for index := 0; index < 4; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for count := 0; count < 100; count++ {
				stream.Push("中")
			}
		}()
	}
	workers.Add(1)
	go func() { defer workers.Done(); stream.Close() }()
	workers.Wait()
	stream.Close()
	if !utf8.ValidString(got.String()) {
		t.Fatal("invalid completed stream")
	}
}

func TestNilCallback(t *testing.T) {
	stream := New(nil)
	stream.Push("中\xe4")
	stream.Close()
}
