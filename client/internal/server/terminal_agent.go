package server

import (
	"encoding/json"
	"io"

	"github.com/deploypilot/deploypilot/internal/agent"
)

// Terminal subscribers share agentMu with the task slot so a new connection
// observes its current owner before any subsequent output or ownership change.
func (s *Server) subscribeTerminalAgent() (<-chan []byte, func()) {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	updates := make(chan []byte, 128)
	if s.terminalSubscribers == nil {
		s.terminalSubscribers = make(map[chan []byte]struct{})
	}
	s.terminalSubscribers[updates] = struct{}{}
	state := s.terminalAgentState
	if state == "" {
		state = "idle"
	}
	data, _ := json.Marshal(map[string]string{"type": "agent_state", "state": state})
	updates <- data
	return updates, func() {
		s.agentMu.Lock()
		defer s.agentMu.Unlock()
		if _, exists := s.terminalSubscribers[updates]; exists {
			delete(s.terminalSubscribers, updates)
			close(updates)
		}
	}
}

// A slow viewer is disconnected rather than losing output silently or blocking
// the task. The terminal WebSocket handler closes when its queue is drained.
func (s *Server) broadcastTerminalLocked(message any) {
	data, err := json.Marshal(message)
	if err != nil {
		return
	}
	for updates := range s.terminalSubscribers {
		select {
		case updates <- data:
		default:
			delete(s.terminalSubscribers, updates)
			close(updates)
		}
	}
}

func (s *Server) publishTerminalAgent(event agent.Event) {
	if event.Type == agent.EventThinking || event.Type == agent.EventStatus {
		return
	}
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	s.broadcastTerminalLocked(struct {
		Type  string      `json:"type"`
		Event agent.Event `json:"event"`
	}{"agent_event", event})
}

func (s *Server) markAgentStopping() {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	if !s.agentBusy || s.terminalAgentState == "stopping" {
		return
	}
	s.terminalAgentState = "stopping"
	s.broadcastTerminalLocked(map[string]string{"type": "agent_state", "state": "stopping"})
}

// Discard input while a task owns the machine, including its cleanup period.
// Never queue it to execute unexpectedly after the task releases ownership.
func (s *Server) writeTerminalInput(shell io.Writer, data []byte) (int, error) {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	if s.agentBusy {
		return 0, nil
	}
	return shell.Write(data)
}
