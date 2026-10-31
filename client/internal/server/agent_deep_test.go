package server

// End-to-end agent scenarios: the real server and WebSocket protocol, the
// real persistent terminal (ConPTY + PowerShell on Windows, bash on Linux)
// and a scripted model. Opt-in because each task pays real shell and
// machine-probe time: DP_AGENT_DEEP=1 go test ./internal/server -run TestAgentDeep -v

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/llm"
	"github.com/gorilla/websocket"
)

// ---- scripted model ---------------------------------------------------------

type modelTurn func(w http.ResponseWriter, req llm.ChatRequest)

type scriptedModel struct {
	mu       sync.Mutex
	turns    []modelTurn
	requests []llm.ChatRequest
	extra    int
}

func (m *scriptedModel) set(turns ...modelTurn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.turns, m.requests, m.extra = turns, nil, 0
}

func (m *scriptedModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req llm.ChatRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	m.mu.Lock()
	n := len(m.requests)
	m.requests = append(m.requests, req)
	var turn modelTurn
	if n < len(m.turns) {
		turn = m.turns[n]
	} else {
		m.extra++
	}
	m.mu.Unlock()
	if turn == nil {
		http.Error(w, "unexpected extra model request", http.StatusBadRequest)
		return
	}
	turn(w, req)
}

func (m *scriptedModel) snapshot() ([]llm.ChatRequest, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]llm.ChatRequest(nil), m.requests...), m.extra
}

func sse(w http.ResponseWriter, chunks ...any) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		data, _ := json.Marshal(c)
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func reply(text string) modelTurn {
	return func(w http.ResponseWriter, _ llm.ChatRequest) {
		sse(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": text}, "finish_reason": "stop"}}})
	}
}

type call struct {
	id, name string
	args     any
}

func cmd(id, command string, timeout int) call {
	args := map[string]any{"command": command}
	if timeout > 0 {
		args["timeout_seconds"] = timeout
	}
	return call{id, "run_command", args}
}

func finish(id string, success bool, summary string, evidence ...string) call {
	args := map[string]any{"summary": summary, "success": success}
	if len(evidence) > 0 {
		args["verification_call_ids"] = evidence
	}
	return call{id, "finish", args}
}

// tools emits one assistant turn with the calls in order. A string args value
// is sent verbatim, so malformed JSON can be scripted.
func tools(calls ...call) modelTurn {
	return func(w http.ResponseWriter, _ llm.ChatRequest) {
		var list []any
		for i, c := range calls {
			var arguments string
			if s, ok := c.args.(string); ok {
				arguments = s
			} else {
				b, _ := json.Marshal(c.args)
				arguments = string(b)
			}
			list = append(list, map[string]any{"index": i, "id": c.id, "type": "function",
				"function": map[string]any{"name": c.name, "arguments": arguments}})
		}
		sse(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": list}, "finish_reason": "tool_calls"}}})
	}
}

func status(code int, body string) modelTurn {
	return func(w http.ResponseWriter, _ llm.ChatRequest) { http.Error(w, body, code) }
}

// toolResult returns the model-visible result for callID in req.
func toolResult(req llm.ChatRequest, callID string) (string, bool) {
	for _, m := range req.Messages {
		if m.Role == llm.RoleTool && m.ToolCallID == callID {
			return m.Content, true
		}
	}
	return "", false
}

func lastUser(req llm.ChatRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == llm.RoleUser {
			return req.Messages[i].Content
		}
	}
	return ""
}

// ---- harness ----------------------------------------------------------------

type deepHarness struct {
	t      *testing.T
	srv    *Server
	model  *scriptedModel
	conn   *websocket.Conn
	url    string
	cookie *http.Cookie
	work   string
	seq    int
	// current is the running subtest, for failure dumps.
	current *testing.T
}

func (h *deepHarness) sub(t *testing.T, name string, fn func(t *testing.T)) {
	t.Run(name, func(t *testing.T) {
		h.current = t
		defer func() { h.current = nil }()
		fn(t)
	})
}

type taskRun struct {
	id       string
	events   []map[string]any
	finals   []map[string]any
	errors   []map[string]any
	elapsed  time.Duration
	requests []llm.ChatRequest
	extra    int
}

func (r *taskRun) of(kind string) []map[string]any {
	var out []map[string]any
	for _, e := range r.events {
		if e["type"] == kind {
			out = append(out, e)
		}
	}
	return out
}

func (r *taskRun) toolDone(callID string) map[string]any {
	for _, e := range r.events {
		if e["type"] == "tool_done" && e["callId"] == callID {
			return e
		}
	}
	return nil
}

func (r *taskRun) hasText(kind, substr string) bool {
	for _, e := range r.of(kind) {
		if s, _ := e["text"].(string); strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func newDeepHarness(t *testing.T) *deepHarness {
	if os.Getenv("DP_AGENT_DEEP") != "1" {
		t.Skip("set DP_AGENT_DEEP=1 to run end-to-end agent scenarios on the real terminal")
	}
	srv, cookie := newLLMServer(t)
	model := &scriptedModel{}
	provider := httptest.NewServer(model)
	t.Cleanup(provider.Close)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test-only"})
	conn := agentSocket(t, srv, cookie)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	work := t.TempDir()
	return &deepHarness{t: t, srv: srv, model: model, conn: conn, url: ts.URL, cookie: cookie, work: work}
}

func (h *deepHarness) send(msg agentClientMsg) {
	h.t.Helper()
	if err := h.conn.WriteJSON(msg); err != nil {
		h.t.Fatal(err)
	}
}

func (h *deepHarness) read(deadline time.Duration) map[string]any {
	h.t.Helper()
	_ = h.conn.SetReadDeadline(time.Now().Add(deadline))
	var m map[string]any
	if err := h.conn.ReadJSON(&m); err != nil {
		h.t.Fatalf("read agent socket: %v", err)
	}
	return m
}

// start submits a task and returns its id once the server reports running.
func (h *deepHarness) start(text, continueFrom string) string {
	h.t.Helper()
	h.seq++
	h.send(agentClientMsg{Type: "start", Task: text, RequestID: fmt.Sprintf("deep-%d-%d", time.Now().UnixNano(), h.seq), ContinueFrom: continueFrom})
	for {
		m := h.read(30 * time.Second)
		switch {
		case m["type"] == "run_state" && m["state"] == "running":
			id, _ := m["taskId"].(string)
			return id
		case m["type"] == "error":
			h.t.Fatalf("start rejected: %v", m["text"])
		}
	}
}

// collect reads until the task is idle again. during, if set, sees every
// message and may act on the socket.
func (h *deepHarness) collect(id string, limit time.Duration, during func(m map[string]any)) *taskRun {
	h.t.Helper()
	run := &taskRun{id: id}
	began := time.Now()
	for {
		m := h.read(limit)
		if during != nil {
			during(m)
		}
		switch m["type"] {
		case "snapshot":
			continue
		case "run_state":
			if m["state"] == "idle" {
				run.elapsed = time.Since(began)
				run.requests, run.extra = h.model.snapshot()
				return run
			}
			continue
		case "final":
			run.finals = append(run.finals, m)
		case "error":
			run.errors = append(run.errors, m)
		}
		run.events = append(run.events, m)
	}
}

func (h *deepHarness) run(text string, limit time.Duration, turns ...modelTurn) *taskRun {
	h.t.Helper()
	h.model.set(turns...)
	return h.collect(h.start(text, ""), limit, nil)
}

func (h *deepHarness) record(id string) map[string]any {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.url+"/api/tasks/"+id, nil)
	req.AddCookie(h.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		h.t.Fatalf("task record %s: %d %s", id, resp.StatusCode, body)
	}
	var rec map[string]any
	if err := json.Unmarshal(body, &rec); err != nil {
		h.t.Fatal(err)
	}
	return rec
}

func (h *deepHarness) report(name string, run *taskRun) {
	h.t.Logf("%s: %d events, %d model requests, %d unexpected, %.1fs", name, len(run.events), len(run.requests), run.extra, run.elapsed.Seconds())
	if t := h.current; t != nil {
		t.Cleanup(func() {
			if !t.Failed() {
				return
			}
			for _, e := range run.events {
				if e["type"] == "output" || e["type"] == "thinking" {
					continue
				}
				b, _ := json.Marshal(e)
				t.Logf("  event %s", truncateUTF8(string(b), 400))
			}
			for i, req := range run.requests {
				if n := len(req.Messages); n > 0 {
					last := req.Messages[n-1]
					t.Logf("  request %d last %s: %s", i, last.Role, truncateUTF8(last.Content, 300))
				}
			}
		})
	}
	if run.extra != 0 {
		h.t.Errorf("%s: %d model requests beyond the script", name, run.extra)
	}
}

func ps(windows, unix string) string {
	if runtime.GOOS == "windows" {
		return windows
	}
	return unix
}

func quoted(path string) string { return "'" + strings.ReplaceAll(path, "'", "''") + "'" }

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// ---- scenarios --------------------------------------------------------------

func TestAgentDeep(t *testing.T) {
	h := newDeepHarness(t)
	work := h.work
	cd := ps("Set-Location -LiteralPath "+quoted(work), "cd "+quoted(work))

	h.sub(t, "deploy_background_service_and_verify", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("PowerShell service script")
		}
		port := freePort(t)
		pidFile := filepath.Join(work, "web.pid")
		t.Cleanup(func() {
			if b, err := os.ReadFile(pidFile); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(string(b), "\ufeff"))); err == nil {
					if p, err := os.FindProcess(pid); err == nil {
						_ = p.Kill()
					}
				}
			}
		})
		start := fmt.Sprintf(`%s; $p = Start-Process python -ArgumentList '-m','http.server','%d','--bind','127.0.0.1' -WorkingDirectory %s -RedirectStandardOutput web.out -RedirectStandardError web.err -WindowStyle Hidden -PassThru; $p.Id | Set-Content web.pid; "started $($p.Id)"`, cd, port, quoted(work))
		check := fmt.Sprintf(`(Invoke-WebRequest -UseBasicParsing http://127.0.0.1:%d/ -TimeoutSec 5).StatusCode`, port)
		run := h.run("部署一个静态网站服务并验证", 90*time.Second,
			tools(call{"w1", "write_file", map[string]any{"path": filepath.Join(work, "index.html"), "content": "<h1>deep ok</h1>"}}),
			tools(cmd("c1", start, 30)),
			tools(call{"wf1", "wait_for", map[string]any{"kind": "url", "target": fmt.Sprintf("http://127.0.0.1:%d/", port), "timeout_seconds": 30, "interval_seconds": 1}}),
			tools(cmd("c2", check, 20)),
			tools(finish("f1", true, "网站已启动", "c2")),
		)
		h.report(t.Name(), run)
		if len(run.finals) != 1 || run.finals[0]["success"] != true {
			t.Fatalf("want one successful final, got %v errors=%v", run.finals, run.errors)
		}
		if s, _ := run.finals[0]["text"].(string); !strings.Contains(s, "实际验证记录") || !strings.Contains(s, "200") {
			t.Fatalf("final lacks real evidence: %q", s)
		}
		if res, _ := toolResult(run.requests[4], "c2"); !strings.Contains(res, "200") {
			t.Fatalf("model did not see the check output: %q", res)
		}
		rec := h.record(run.id)
		if rec["phase"] != "finished" || rec["canContinue"] != true {
			t.Fatalf("record phase=%v canContinue=%v", rec["phase"], rec["canContinue"])
		}
	})

	h.sub(t, "failures_are_reported_and_the_model_recovers", func(t *testing.T) {
		run := h.run("检查失败处理", 90*time.Second,
			tools(cmd("e1", ps("cmd /c exit 3", "exit 3"), 20)),
			tools(cmd("e2", ps(`cmd /c "echo diag 1>&2"`, "echo diag 1>&2"), 20)),
			tools(cmd("e3", ps(`Get-Item C:\definitely-missing-deep-path`, "ls /definitely-missing-deep-path"), 20)),
			tools(cmd("e4", ps(`cmd /c exit 0; "after"`, "true; echo after"), 20)),
			reply("已检查"),
		)
		h.report(t.Name(), run)
		want := map[string]int{"e1": 3, "e2": 0, "e4": 0}
		for id, code := range want {
			done := run.toolDone(id)
			if done == nil || int(done["exitCode"].(float64)) != code {
				t.Errorf("%s: want exit %d, got %v", id, code, done)
			}
		}
		if done := run.toolDone("e3"); done == nil || done["success"] != false {
			t.Errorf("missing path should fail: %v", done)
		}
		e1, _ := toolResult(run.requests[1], "e1")
		if !strings.Contains(e1, "exit=3") {
			t.Errorf("model was not told the exit code: %q", e1)
		}
		e2, _ := toolResult(run.requests[2], "e2")
		if !strings.Contains(e2, "diag") || strings.HasPrefix(e2, "error:") {
			t.Errorf("native stderr with exit 0 must be output, not failure: %q", e2)
		}
		e4, _ := toolResult(run.requests[4], "e4")
		if !strings.HasPrefix(e4, "exit=0") || !strings.Contains(e4, "after") {
			t.Errorf("exit code leaked from an earlier command: %q", e4)
		}
		if len(run.finals) != 1 || run.finals[0]["state"] != "reply" {
			t.Fatalf("want a natural reply, got %v", run.finals)
		}
	})

	h.sub(t, "blank_stderr_lines_stay_blank", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("Windows PowerShell ErrorRecord formatting")
		}
		// curl's progress meter writes blank stderr lines; Windows PowerShell
		// used to show each as "System.Management.Automation.RemoteException".
		run := h.run("空 stderr 行", 60*time.Second,
			tools(cmd("se", `python -c "import sys; sys.stderr.write('first\n\nthird\n'); print('out')"`, 30)),
			reply("ok"),
		)
		h.report(t.Name(), run)
		res, _ := toolResult(run.requests[1], "se")
		if strings.Contains(res, "RemoteException") || !strings.Contains(res, "first") || !strings.Contains(res, "third") || !strings.Contains(res, "out") {
			t.Fatalf("stderr lines mangled: %q", res)
		}
		live := ""
		for _, e := range run.of("output") {
			live += fmt.Sprint(e["text"])
		}
		if strings.Contains(live, "RemoteException") {
			t.Fatalf("live output shows the exception type: %q", live)
		}
	})

	h.sub(t, "unicode_output_round_trips", func(t *testing.T) {
		text := "部署成功 ✓ — naïve 日本語"
		run := h.run("输出中文", 60*time.Second,
			tools(cmd("u1", ps("Write-Output '"+text+"'", "echo '"+text+"'"), 20)),
			reply("ok"),
		)
		h.report(t.Name(), run)
		res, _ := toolResult(run.requests[1], "u1")
		if !strings.Contains(res, text) {
			t.Fatalf("unicode mangled for the model: %q", res)
		}
		live := ""
		for _, e := range run.of("output") {
			if e["callId"] == "u1" {
				live += e["text"].(string)
			}
		}
		if !strings.Contains(live, text) {
			t.Fatalf("unicode mangled in live output: %q", live)
		}
	})

	h.sub(t, "large_output_is_bounded_and_logged", func(t *testing.T) {
		run := h.run("大量输出", 120*time.Second,
			tools(cmd("big", ps(`1..30000 | ForEach-Object { "line $_ " + ('x' * 40) }`, `for i in $(seq 1 30000); do echo "line $i xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"; done`), 120)),
			reply("ok"),
		)
		h.report(t.Name(), run)
		res, _ := toolResult(run.requests[1], "big")
		if len(res) > 40*1024 {
			t.Errorf("model result is %d bytes; it should be bounded", len(res))
		}
		if !strings.Contains(res, "line 1 ") || !strings.Contains(res, "line 30000 ") || !strings.Contains(res, "truncated") {
			t.Errorf("bounded result should keep head and tail and say it was cut: head=%q", res[:min(200, len(res))])
		}
		done := run.toolDone("big")
		logPath, _ := done["logPath"].(string)
		data, err := os.ReadFile(logPath)
		if err != nil || !bytes.Contains(data, []byte("line 30000 ")) || !bytes.Contains(data, []byte("line 15000 ")) {
			t.Errorf("full log %q incomplete: %v (%d bytes)", logPath, err, len(data))
		}
		rec := h.record(run.id)
		if b, _ := json.Marshal(rec); len(b) > 1024*1024 {
			t.Errorf("stored task record is %d bytes", len(b))
		}
	})

	h.sub(t, "timeout_kills_the_command_and_the_terminal_recovers", func(t *testing.T) {
		run := h.run("超时", 90*time.Second,
			tools(cmd("t1", ps("Start-Sleep -Seconds 60", "sleep 60"), 2)),
			tools(cmd("t2", ps(cd+"; 'alive'", cd+"; echo alive"), 20)),
			reply("ok"),
		)
		h.report(t.Name(), run)
		if done := run.toolDone("t1"); done == nil || done["state"] != "timeout" {
			t.Fatalf("want timeout state, got %v", done)
		}
		if run.elapsed > 45*time.Second {
			t.Errorf("timeout took %.1fs", run.elapsed.Seconds())
		}
		if res, _ := toolResult(run.requests[2], "t2"); !strings.Contains(res, "alive") {
			t.Fatalf("terminal unusable after timeout: %q", res)
		}
	})

	h.sub(t, "interactive_prompt_times_out_instead_of_hanging", func(t *testing.T) {
		run := h.run("交互提示", 90*time.Second,
			tools(cmd("i1", ps("$name = Read-Host 'Your name'; \"got $name\"", "read -p 'name: ' n; echo got $n"), 3)),
			tools(cmd("i2", ps("'still alive'", "echo still alive"), 20)),
			reply("ok"),
		)
		h.report(t.Name(), run)
		done := run.toolDone("i1")
		if done == nil || done["success"] == true {
			t.Fatalf("an interactive prompt must not succeed silently: %v", done)
		}
		if res, _ := toolResult(run.requests[2], "i2"); !strings.Contains(res, "still alive") {
			t.Fatalf("terminal unusable after interactive prompt: %q", res)
		}
	})

	h.sub(t, "exit_in_a_command_does_not_break_the_next_command", func(t *testing.T) {
		run := h.run("exit", 90*time.Second,
			tools(cmd("x1", "exit 0", 15)),
			tools(cmd("x2", ps("'after exit'", "echo after exit"), 30)),
			reply("ok"),
		)
		h.report(t.Name(), run)
		t.Logf("x1 done: %v", run.toolDone("x1"))
		res, _ := toolResult(run.requests[2], "x2")
		if !strings.Contains(res, "after exit") {
			t.Fatalf("command after exit failed: %q", res)
		}
		if !strings.Contains(res, "previous terminal shell exited") {
			t.Errorf("the model was not told the shell restarted: %q", res)
		}
		if run.elapsed > 25*time.Second {
			t.Errorf("recovering from exit took %.1fs", run.elapsed.Seconds())
		}
	})

	h.sub(t, "tool_errors_only_fail_the_call", func(t *testing.T) {
		run := h.run("参数错误", 60*time.Second,
			tools(call{"b1", "run_command", map[string]any{"command": 42}}),
			tools(call{"b2", "launch_rockets", map[string]any{}}),
			tools(call{"b3", "read_file", map[string]any{"path": filepath.Join(work, "missing.txt")}}),
			tools(call{"b4", "edit_file", map[string]any{"path": filepath.Join(work, "index.html"), "old_string": "not-present", "new_string": "x"}}),
			reply("ok"),
		)
		h.report(t.Name(), run)
		for i, id := range []string{"b1", "b2", "b3", "b4"} {
			res, ok := toolResult(run.requests[i+1], id)
			if !ok || !strings.HasPrefix(res, "error:") {
				t.Errorf("%s should return an error to the model, got %q", id, res)
			}
		}
		if len(run.finals) != 1 {
			t.Fatalf("task should continue to the reply: finals=%v", run.finals)
		}
	})

	h.sub(t, "finish_requires_fresh_evidence", func(t *testing.T) {
		run := h.run("证据", 90*time.Second,
			tools(finish("f0", true, "成功了")),
			tools(call{"r1", "read_file", map[string]any{"path": filepath.Join(work, "index.html")}}),
			tools(finish("f1", true, "成功了", "r1")),
			tools(cmd("v1", ps("'check ok'", "echo check ok"), 20)),
			tools(call{"w2", "write_file", map[string]any{"path": filepath.Join(work, "later.txt"), "content": "changed"}}),
			tools(finish("f2", true, "成功了", "v1")),
			tools(cmd("v2", ps("'check ok again'", "echo check ok again"), 20)),
			tools(finish("f3", true, "成功了", "v2")),
		)
		h.report(t.Name(), run)
		for i, id := range []string{"f0", "f1", "f2"} {
			req := run.requests[[]int{1, 3, 6}[i]]
			if res, _ := toolResult(req, id); !strings.HasPrefix(res, "error:") {
				t.Errorf("%s should be rejected, got %q", id, res)
			}
		}
		if len(run.finals) != 1 || run.finals[0]["success"] != true {
			t.Fatalf("only the evidenced finish should complete: %v", run.finals)
		}
	})

	h.sub(t, "finish_mid_turn_skips_later_calls", func(t *testing.T) {
		marker := filepath.Join(work, "must-not-exist.txt")
		run := h.run("中途结束", 60*time.Second,
			tools(cmd("m1", ps("'done'", "echo done"), 20), finish("m2", true, "完成", "m1"),
				call{"m3", "write_file", map[string]any{"path": marker, "content": "x"}}),
		)
		h.report(t.Name(), run)
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("a call after finish was executed")
		}
		if len(run.finals) != 1 || run.finals[0]["success"] != true {
			t.Fatalf("finals: %v", run.finals)
		}
		// The saved conversation must stay valid to continue.
		h.model.set(func(w http.ResponseWriter, req llm.ChatRequest) {
			res, ok := toolResult(req, "m3")
			if !ok {
				t.Errorf("skipped call has no result in the continued conversation")
			}
			t.Logf("m3 result on continue: %q", res)
			reply("继续完成")(w, req)
		})
		cont := h.collect(h.start("再确认一下", run.id), 60*time.Second, nil)
		h.report(t.Name()+"/continue", cont)
		if len(cont.finals) != 1 {
			t.Fatalf("continue finals: %v", cont.finals)
		}
	})

	h.sub(t, "repeated_identical_calls_pause_the_task", func(t *testing.T) {
		same := cmd("", ps("'same'", "echo same"), 20)
		var turns []modelTurn
		for i := 1; i <= 4; i++ {
			c := same
			c.id = fmt.Sprintf("rep%d", i)
			turns = append(turns, tools(c))
		}
		run := h.run("重复", 90*time.Second, turns...)
		h.report(t.Name(), run)
		if !run.hasText("status", "连续出现 3 次") {
			t.Error("no warning at the third repetition")
		}
		if len(run.finals) != 1 || !strings.Contains(run.finals[0]["text"].(string), "重复 4 次") {
			t.Fatalf("task should pause at the fourth repetition: %v", run.finals)
		}
	})

	h.sub(t, "empty_replies_end_with_a_clear_message", func(t *testing.T) {
		run := h.run("空回复", 60*time.Second, reply(""), reply("   "))
		h.report(t.Name(), run)
		if len(run.finals) != 1 || !strings.Contains(run.finals[0]["text"].(string), "没有返回有效答复") {
			t.Fatalf("finals: %v", run.finals)
		}
		if !strings.Contains(lastUser(run.requests[1]), "本轮没有有效答复") {
			t.Error("the model was not nudged after the first empty reply")
		}
	})

	h.sub(t, "malformed_tool_json_ends_with_an_error_and_can_continue", func(t *testing.T) {
		run := h.run("坏 JSON", 60*time.Second, tools(call{"j1", "run_command", `{"command": "echo hi"`}))
		h.report(t.Name(), run)
		if len(run.errors) == 0 {
			t.Fatal("malformed model output should be reported as an error")
		}
		t.Logf("error shown: %v", run.errors[len(run.errors)-1]["text"])
		rec := h.record(run.id)
		if rec["phase"] != "finished" {
			t.Fatalf("phase %v", rec["phase"])
		}
		h.model.set(reply("恢复了"))
		cont := h.collect(h.start("继续", run.id), 60*time.Second, nil)
		h.report(t.Name()+"/continue", cont)
		if rec["canContinue"] != true || len(cont.finals) != 1 {
			t.Fatalf("canContinue=%v continue finals=%v", rec["canContinue"], cont.finals)
		}
	})

	h.sub(t, "interrupted_stream_is_retried", func(t *testing.T) {
		broken := func(w http.ResponseWriter, _ llm.ChatRequest) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial \"}}]}\n\n")
			w.(http.Flusher).Flush()
			hj, ok := w.(http.Hijacker)
			if !ok {
				return
			}
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		}
		run := h.run("断流", 60*time.Second, broken, reply("完整答复"))
		h.report(t.Name(), run)
		if !run.hasText("status", "模型连接中断") {
			t.Error("no retry notice after the stream broke")
		}
		if len(run.finals) != 1 || run.finals[0]["text"] != "完整答复" {
			t.Fatalf("finals: %v", run.finals)
		}
	})

	h.sub(t, "context_overflow_tightens_the_budget", func(t *testing.T) {
		run := h.run("超长", 60*time.Second,
			status(http.StatusBadRequest, `{"error":{"message":"This model's maximum context length is 8192 tokens","code":"context_length_exceeded"}}`),
			reply("缩短后成功"))
		h.report(t.Name(), run)
		if !run.hasText("status", "上下文超长") {
			t.Error("no overflow notice")
		}
		if len(run.finals) != 1 {
			t.Fatalf("finals: %v errors: %v", run.finals, run.errors)
		}
	})

	h.sub(t, "auth_error_is_not_retried_and_hides_the_key", func(t *testing.T) {
		run := h.run("鉴权", 60*time.Second, status(http.StatusUnauthorized, `{"error":"invalid api key test-only"}`))
		h.report(t.Name(), run)
		if len(run.requests) != 1 {
			t.Errorf("401 retried %d times", len(run.requests))
		}
		for _, e := range run.events {
			if b, _ := json.Marshal(e); bytes.Contains(b, []byte("test-only")) {
				t.Fatalf("API key leaked to the browser: %s", b)
			}
		}
	})

	h.sub(t, "steer_during_a_command_is_applied_after_it", func(t *testing.T) {
		h.model.set(
			tools(cmd("s1", ps("Start-Sleep -Seconds 4; 'slept'", "sleep 4; echo slept"), 30)),
			func(w http.ResponseWriter, req llm.ChatRequest) {
				if !strings.Contains(lastUser(req), "改用 8081 端口") {
					t.Errorf("steer missing from the next request; last user: %q", lastUser(req))
				}
				reply("已按补充改用 8081")(w, req)
			},
		)
		id := h.start("长命令中补充", "")
		steered, acked := false, false
		var order []string
		run := h.collect(id, 90*time.Second, func(m map[string]any) {
			if m["type"] == "tool_call" && m["callId"] == "s1" && !steered {
				steered = true
				h.send(agentClientMsg{Type: "steer", TaskID: id, Task: "改用 8081 端口", RequestID: "steer-1"})
			}
			if m["type"] == "steer_ack" {
				acked = true
			}
			if m["type"] == "tool_done" && m["callId"] == "s1" {
				order = append(order, "tool_done")
			}
			if m["type"] == "activity" && m["state"] == "steer_applied" {
				order = append(order, "steer_applied")
			}
		})
		h.report(t.Name(), run)
		if !acked {
			t.Error("steer was not acknowledged")
		}
		if strings.Join(order, ",") != "tool_done,steer_applied" {
			t.Errorf("steer must apply after the running command: %v", order)
		}
	})

	h.sub(t, "steer_arriving_with_finish_defers_it", func(t *testing.T) {
		// The steer is queued while the model is producing finish, so it
		// misses the step boundary and must defer the finish instead.
		entered, release := make(chan struct{}), make(chan struct{})
		h.model.set(
			tools(cmd("d1", ps("'checked'", "echo checked"), 20)),
			func(w http.ResponseWriter, req llm.ChatRequest) {
				close(entered)
				<-release
				tools(finish("d2", true, "完成", "d1"))(w, req)
			},
			func(w http.ResponseWriter, req llm.ChatRequest) {
				if !strings.Contains(lastUser(req), "顺便检查日志") {
					t.Errorf("deferred steer not delivered: %q", lastUser(req))
				}
				reply("日志也检查过了")(w, req)
			},
		)
		id := h.start("完成时补充", "")
		go func() {
			<-entered
			_ = h.conn.WriteJSON(agentClientMsg{Type: "steer", TaskID: id, Task: "顺便检查日志", RequestID: "steer-2"})
		}()
		released := false
		run := h.collect(id, 90*time.Second, func(m map[string]any) {
			if m["type"] == "steer_ack" && !released {
				released = true
				close(release)
			}
		})
		h.report(t.Name(), run)
		if !released {
			t.Fatal("steer was not acknowledged")
		}
		if res, _ := toolResult(run.requests[2], "d2"); !strings.Contains(res, "not finished") {
			t.Errorf("finish should be answered as deferred, got %q", res)
		}
		if len(run.finals) != 1 || run.finals[0]["text"] != "日志也检查过了" {
			t.Fatalf("finish should be deferred until the steer is handled: %v", run.finals)
		}
	})

	h.sub(t, "steer_validation", func(t *testing.T) {
		release := make(chan struct{})
		h.model.set(func(w http.ResponseWriter, req llm.ChatRequest) { <-release; reply("ok")(w, req) }, reply("ok2"))
		id := h.start("补充校验", "")
		results := map[string]string{}
		// Replies arrive in send order on this socket.
		steer := func(task, text, reqID string) {
			h.send(agentClientMsg{Type: "steer", TaskID: task, Task: text, RequestID: reqID})
		}
		steer(id, "a", "v-1")
		steer(id, "a", "v-1")
		steer(id, "b", "v-1")
		steer(id, "c", "")
		steer(id, strings.Repeat("x", 17*1024), "v-2")
		steer(id, "d", "v-3")
		steer("not-a-task", "d", "v-4")
		keys := []string{"ok", "dup-same", "dup-diff", "empty-id", "too-big", "second-ok", "other-task"}
		i := 0
		run := h.collect(id, 60*time.Second, func(m map[string]any) {
			if (m["type"] == "steer_ack" || (m["type"] == "error" && m["requestId"] != nil)) && i < len(keys) {
				if m["type"] == "steer_ack" {
					results[keys[i]] = "ack"
				} else {
					results[keys[i]] = "error: " + m["text"].(string)
				}
				i++
				if i == len(keys) {
					close(release)
				}
			}
		})
		h.report(t.Name(), run)
		t.Logf("steer results: %v", results)
		if results["ok"] != "ack" || results["dup-same"] != "ack" || results["second-ok"] != "ack" {
			t.Errorf("valid and idempotent steers should ack: %v", results)
		}
		for _, k := range []string{"dup-diff", "empty-id", "too-big", "other-task"} {
			if !strings.HasPrefix(results[k], "error") {
				t.Errorf("%s should be rejected: %v", k, results[k])
			}
		}
	})

	h.sub(t, "stop_during_a_command_cleans_up_and_can_continue", func(t *testing.T) {
		h.model.set(tools(cmd("p1", ps("Start-Sleep -Seconds 120", "sleep 120"), 300)))
		id := h.start("停止测试", "")
		var stopAt time.Time
		run := h.collect(id, 60*time.Second, func(m map[string]any) {
			if m["type"] == "tool_call" && m["callId"] == "p1" && stopAt.IsZero() {
				time.Sleep(500 * time.Millisecond)
				stopAt = time.Now()
				h.send(agentClientMsg{Type: "stop"})
			}
		})
		h.report(t.Name(), run)
		took := time.Since(stopAt)
		t.Logf("stop to idle: %.1fs", took.Seconds())
		if took > 10*time.Second {
			t.Errorf("stop took %.1fs", took.Seconds())
		}
		if done := run.toolDone("p1"); done == nil || done["state"] != "cancelled" {
			t.Errorf("want cancelled command, got %v", done)
		}
		stoppedStatus := 0
		for _, e := range run.of("status") {
			if e["text"] == "已停止。" {
				stoppedStatus++
			}
		}
		if stoppedStatus != 1 || len(run.finals) != 0 {
			t.Errorf("a stop during a command must show the stopped status once and no final: status=%d finals=%d", stoppedStatus, len(run.finals))
		}
		rec := h.record(id)
		if rec["canContinue"] != true {
			t.Fatalf("stopped task should be continuable: %v", rec["canContinue"])
		}
		h.model.set(func(w http.ResponseWriter, req llm.ChatRequest) {
			res, ok := toolResult(req, "p1")
			if !ok || !strings.Contains(res, "cancel") {
				t.Errorf("continued conversation lost the cancelled call: %q", res)
			}
			if !strings.Contains(lastUser(req), "换个方法") {
				t.Errorf("follow-up missing: %q", lastUser(req))
			}
			reply("好的，换个方法")(w, req)
		})
		cont := h.collect(h.start("换个方法", id), 60*time.Second, nil)
		h.report(t.Name()+"/continue", cont)
		if len(cont.finals) != 1 {
			t.Fatalf("continue finals: %v", cont.finals)
		}
		after := h.run("停止后终端可用", 60*time.Second, tools(cmd("p2", ps("'terminal ok'", "echo terminal ok"), 20)), reply("ok"))
		h.report(t.Name()+"/after", after)
		if len(after.requests) < 2 {
			t.Fatalf("terminal check did not run: errors=%v", after.errors)
		}
		if res, _ := toolResult(after.requests[1], "p2"); !strings.Contains(res, "terminal ok") {
			t.Fatalf("terminal unusable after stop: %q", res)
		}
	})

	h.sub(t, "stop_during_model_retry_wait", func(t *testing.T) {
		h.model.set(status(http.StatusServiceUnavailable, "busy"), status(http.StatusServiceUnavailable, "busy"), status(http.StatusServiceUnavailable, "busy"))
		id := h.start("重试中停止", "")
		var stopAt time.Time
		run := h.collect(id, 60*time.Second, func(m map[string]any) {
			if m["type"] == "status" && strings.Contains(fmt.Sprint(m["text"]), "重试") && stopAt.IsZero() {
				stopAt = time.Now()
				h.send(agentClientMsg{Type: "stop"})
			}
		})
		h.report(t.Name(), run)
		if stopAt.IsZero() {
			t.Fatal("no retry notice was shown")
		}
		if took := time.Since(stopAt); took > 5*time.Second {
			t.Errorf("stop during retry wait took %.1fs", took.Seconds())
		}
	})

	h.sub(t, "second_start_while_running_is_rejected", func(t *testing.T) {
		release := make(chan struct{})
		h.model.set(func(w http.ResponseWriter, req llm.ChatRequest) { <-release; reply("first")(w, req) })
		id := h.start("第一个", "")
		other := agentSocket(t, h.srv, h.cookie)
		_ = other.WriteJSON(agentClientMsg{Type: "start", Task: "第二个", RequestID: "concurrent-2"})
		rejected := ""
		_ = other.SetReadDeadline(time.Now().Add(10 * time.Second))
		for rejected == "" {
			var m map[string]any
			if err := other.ReadJSON(&m); err != nil {
				t.Fatal(err)
			}
			if m["type"] == "error" {
				rejected = m["text"].(string)
			}
		}
		close(release)
		run := h.collect(id, 30*time.Second, nil)
		h.report(t.Name(), run)
		if !strings.Contains(rejected, "已有任务") {
			t.Fatalf("second start: %q", rejected)
		}
	})
}
