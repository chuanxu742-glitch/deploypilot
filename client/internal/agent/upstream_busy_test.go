package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/deploypilot/deploypilot/internal/llm"
)

func TestUpstreamBusyRetriesOnlyCurrentTurn(t *testing.T) {
	partial := "data: " + `{"choices":[{"delta":{"content":"准备验证","tool_calls":[{"index":0,"id":"discard","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"should-not-exist\",\"content\":\"bad\"}"}}]}}]}` + "\n\n"
	busy := "data: " + `{"error":{"message":"Service is temporarily busy. Please try again shortly or use another model.","code":"upstream_busy","metadata":{"provider_name":"BAILU AI"},"type":"api_error"}}` + "\n\n"
	model := newRecordingLLM(t,
		reply(toolCallSSE("install", toolRunCommand, `{"command":"install service once"}`)),
		reply(partial+busy),
		reply(toolCallSSE("health", toolRunCommand, `{"command":"check service health"}`)),
		reply(toolCallSSE("finish", toolFinish, `{"summary":"verified","success":true,"verification_call_ids":["health"]}`)),
	)
	executor := newFakeExec()
	emit, events, _ := collectEvents()
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: model.srv.URL, APIKey: "test"}), Executor: executor, Emit: emit})
	if err := a.Run(context.Background(), "deploy service"); err != nil {
		t.Fatalf("busy current turn should recover: %v", err)
	}
	if !reflect.DeepEqual(executor.commands, []string{"install service once", "check service health"}) || len(executor.writes) != 0 {
		t.Fatalf("busy response replayed or executed tools: commands=%v writes=%v", executor.commands, executor.writes)
	}
	if len(model.requests) != 4 || !reflect.DeepEqual(model.requests[1], model.requests[2]) {
		t.Fatal("retry must repeat only the same model request, retaining earlier tool results")
	}
	var waiting, success bool
	for _, event := range *events {
		waiting = waiting || event.Type == EventStatus && strings.Contains(event.Text, "模型接口暂不可用")
		success = success || event.Type == EventFinal && event.Success
	}
	if !waiting || !success {
		t.Fatalf("missing wait notification or verified final: waiting=%v success=%v", waiting, success)
	}
}
