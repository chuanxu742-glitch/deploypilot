package server

import (
	"encoding/hex"
	"strings"
)

const maxStartReceipts = 1024

type startReceipt struct {
	ContentHash  string `json:"contentHash,omitempty"`
	ContinueFrom string `json:"continueFrom,omitempty"`
	// Older versions only recorded the latest request ID. Preserve that known
	// acceptance without inventing a content signature or permitting replay.
	Legacy bool `json:"legacy,omitempty"`
}

func validStartRequestID(id string) bool {
	return len(id) <= 128 && !strings.ContainsAny(id, "\x00\r\n")
}

func (s *Server) findStartReceiptLocked(id string) (*taskRecord, startReceipt, bool) {
	if id == "" {
		return nil, startReceipt{}, false
	}
	for _, task := range s.tasks {
		if receipt, ok := task.StartReceipts[id]; ok {
			return task, receipt, true
		}
		if task.RequestID == id {
			return task, startReceipt{Legacy: true}, true
		}
	}
	return nil, startReceipt{}, false
}

func startReceiptCount(task *taskRecord) int {
	count := len(task.StartReceipts)
	if task.RequestID != "" && validStartRequestID(task.RequestID) {
		if _, known := task.StartReceipts[task.RequestID]; !known {
			count++
		}
	}
	return count
}

func rememberStart(task *taskRecord, id, text, continueFrom string) {
	if task.StartReceipts == nil {
		task.StartReceipts = make(map[string]startReceipt)
	}
	if task.RequestID != "" && validStartRequestID(task.RequestID) {
		if _, known := task.StartReceipts[task.RequestID]; !known {
			task.StartReceipts[task.RequestID] = startReceipt{Legacy: true}
		}
	}
	if id != "" {
		task.StartReceipts[id] = startReceipt{ContentHash: steerDigest(text), ContinueFrom: continueFrom}
	}
}

func validStartReceipt(id string, receipt startReceipt) bool {
	if id == "" || !validStartRequestID(id) {
		return false
	}
	if receipt.Legacy {
		return receipt.ContentHash == "" && receipt.ContinueFrom == ""
	}
	decoded, err := hex.DecodeString(receipt.ContentHash)
	return err == nil && len(decoded) == 32 && len(receipt.ContinueFrom) <= 128 && !strings.ContainsAny(receipt.ContinueFrom, "/\\\x00\r\n")
}

func (s *Server) acceptedStartTaskID(requestID string) string {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if task, _, found := s.findStartReceiptLocked(requestID); found {
		return task.ID
	}
	return ""
}
