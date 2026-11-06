// Package outputstream preserves UTF-8 characters across process read boundaries.
package outputstream

import (
	"strings"
	"sync"
	"unicode/utf8"
)

// Stream emits complete, valid UTF-8 strings. A process or PTY read can end in
// the middle of a character; JSON would irreversibly replace those bytes if
// each read were emitted immediately. At most three unfinished bytes are kept.
type Stream struct {
	mu      sync.Mutex
	emit    func(string)
	pending string
	closed  bool
}

func New(emit func(string)) *Stream { return &Stream{emit: emit} }

// Push accepts an arbitrary byte fragment as a string. Emission is serialized
// with Close so a final fragment cannot be published after stream completion.
func (s *Stream) Push(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.emit == nil {
		return
	}
	text = s.pending + text
	end := 0
	for end < len(text) && utf8.FullRuneInString(text[end:]) {
		_, size := utf8.DecodeRuneInString(text[end:])
		end += size
	}
	s.pending = strings.Clone(text[end:])
	if end > 0 {
		s.emit(strings.ToValidUTF8(text[:end], "\uFFFD"))
	}
}

// Close flushes an incomplete final character as a replacement character.
// Later fragments are ignored; Close is safe to call more than once.
func (s *Stream) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.pending != "" && s.emit != nil {
		s.emit(strings.ToValidUTF8(s.pending, "\uFFFD"))
	}
	s.pending = ""
}
