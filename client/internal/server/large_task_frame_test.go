package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A pasted log at or over the text limit gets a readable rejection instead of
// a closed socket, however much JSON escaping inflates the frame.
func TestLargeTaskTextIsRejectedWithAReason(t *testing.T) {
	for _, c := range []struct {
		name, text, want string
	}{
		{"60KiB of Windows paths", strings.Repeat(`C:\a\b `+"\n", 60*1024/8), "尚未配置模型"},
		{"64KiB of control characters", strings.Repeat("\x01", 64*1024), "尚未配置模型"},
		{"70KiB plain text", strings.Repeat("x", 70*1024), "不能超过64KiB"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, cookie := newLLMServer(t)
			conn := agentSocket(t, srv, cookie)
			frame, _ := json.Marshal(agentClientMsg{Type: "start", Task: c.text, RequestID: "large-task-request-1"})
			if err := conn.WriteMessage(1, frame); err != nil {
				t.Fatal(err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			for {
				_, data, err := conn.ReadMessage()
				if err != nil {
					t.Fatalf("frame of %d bytes closed the socket without a reason: %v", len(frame), err)
				}
				var m map[string]any
				if json.Unmarshal(data, &m) == nil && m["type"] == "error" {
					if text, _ := m["text"].(string); !strings.Contains(text, c.want) {
						t.Fatalf("reason %q, want %q", text, c.want)
					}
					return
				}
			}
		})
	}
}
