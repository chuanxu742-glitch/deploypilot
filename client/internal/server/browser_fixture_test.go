package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Shared setup for the tests that drive the built web app in Chrome.

func requireBrowserIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("DP_BROWSER_INTEGRATION") != "1" {
		t.Skip("set DP_BROWSER_INTEGRATION=1 after building web to run Chrome integration")
	}
}

// browserAssets returns the repository root and the built web app, failing
// with a clear message when web has not been built.
func browserAssets(t *testing.T) (repo, assets string) {
	t.Helper()
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	assets = filepath.Join(repo, "client", "internal", "webui", "dist")
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal("build web before running browser integration: ", err)
	}
	return repo, assets
}

// isolateProfile points the server's audit and history output at a temp
// directory and returns the real profile, which Chrome still needs.
func isolateProfile(t *testing.T) string {
	real := os.Getenv("USERPROFILE")
	t.Setenv("USERPROFILE", t.TempDir())
	return real
}

// browserScript runs web/tests/<script> with node. Chrome gets the real
// profile back: changing its Windows profile root breaks its
// remote-debugging directory checks.
func browserScript(ctx context.Context, repo, script, realProfile string, env ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "node", filepath.Join(repo, "web", "tests", script))
	cmd.Env = append(os.Environ(), env...)
	if realProfile != "" {
		cmd.Env = append(cmd.Env, "USERPROFILE="+realProfile)
	}
	return cmd
}
