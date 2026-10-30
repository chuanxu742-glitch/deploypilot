package tunnel

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHelperOwner is not a real test: run as a subprocess, it starts a
// long-lived child through startOwned, prints the child's pid, then waits to
// be killed. The parent test kills it hard and checks the child died too.
func TestHelperOwner(t *testing.T) {
	if os.Getenv("DP_OWNER_HELPER") != "1" {
		t.Skip("helper process only")
	}
	var child *exec.Cmd
	if runtime.GOOS == "windows" {
		child = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "Start-Sleep -Seconds 60")
	} else {
		child = exec.Command("sleep", "60")
	}
	if _, err := startOwned(child); err != nil {
		fmt.Println("ERR", err)
		os.Exit(2)
	}
	fmt.Println("CHILD", child.Process.Pid)
	time.Sleep(60 * time.Second)
}

func processAlive(pid int) bool {
	if runtime.GOOS == "windows" {
		out, _ := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH").Output()
		return strings.Contains(string(out), strconv.Itoa(pid))
	}
	return exec.Command("kill", "-0", strconv.Itoa(pid)).Run() == nil
}

func TestTunnelChildDiesWithParent(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("no kernel-enforced parent binding on this platform")
	}
	helper := exec.Command(os.Args[0], "-test.run=^TestHelperOwner$", "-test.v")
	helper.Env = append(os.Environ(), "DP_OWNER_HELPER=1")
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer helper.Process.Kill()
	pid := 0
	lines := bufio.NewScanner(stdout)
	deadline := time.After(30 * time.Second)
	found := make(chan int, 1)
	go func() {
		for lines.Scan() {
			if v, ok := strings.CutPrefix(strings.TrimSpace(lines.Text()), "CHILD "); ok {
				n, _ := strconv.Atoi(v)
				found <- n
				return
			}
		}
	}()
	select {
	case pid = <-found:
	case <-deadline:
		t.Fatal("helper did not start its child")
	}
	if !processAlive(pid) {
		t.Fatal("child not running before the parent was killed")
	}
	// Kill the parent the way a crash or Stop-Process would: no cleanup runs.
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	for i := 0; i < 50; i++ {
		if !processAlive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
	t.Fatalf("child %d outlived its killed parent", pid)
}
