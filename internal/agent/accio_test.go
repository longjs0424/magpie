package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func TestAccioWorkConnectLaunchDisconnect(t *testing.T) {
	home := syncHome(t)
	bin := filepath.Join(home, "Accio Work.exe")
	if err := os.WriteFile(bin, []byte("fake executable"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_ACCIO_BIN", bin)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	a := accio(home)
	if !a.Detected() {
		t.Fatal("explicit executable wasn't detected")
	}
	if a.Launch() != "" || len(a.Start().Env) != 0 {
		t.Fatal("unconnected Accio was launched through magpie")
	}
	if err := a.Apply("model", "magpie/fake/m1"); err != nil {
		t.Fatal(err)
	}
	if !a.Wired() {
		t.Fatal("selected model did not connect Accio")
	}
	s := a.Start()
	if s.Bin != bin || s.Model != "fake/m1" || s.Flag != "" {
		t.Fatalf("launch: %+v", s)
	}
	env := strings.Join(s.Env, "\n")
	for _, want := range []string{"GATEWAY_BASE_URL=" + gateway.URL() + "/accio", "ADK_BASE_URL=" + gateway.URL() + "/accio/api/adk/llm", "ADK_MODEL=fake/m1", "EMBEDDING_BASE_URL=" + gateway.URL() + "/accio/api/adk/embedding/embed"} {
		if !strings.Contains(env, want) {
			t.Errorf("missing %q in %s", want, env)
		}
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if a.Wired() || a.Launch() != "" || len(a.Start().Env) != 0 {
		t.Fatal("disconnected Accio still uses magpie")
	}
	if _, err := os.Stat(filepath.Join(home, ".accio")); !os.IsNotExist(err) {
		t.Fatalf("Accio's own settings were changed: %v", err)
	}
}

func TestAccioWorkLaunchQuotesShellData(t *testing.T) {
	s := Starting{Bin: "C:/User's Files/Accio Work.exe", Env: []string{"ADK_MODEL=fake/user's $(echo hello); `echo hi`"}}
	windows := accioLaunch(s, "windows")
	if !strings.Contains(windows, "'fake/user''s $(echo hello); `echo hi`'") || !strings.Contains(windows, "finally {") || !strings.Contains(windows, "'C:/User''s Files/Accio Work.exe'") {
		t.Fatalf("PowerShell command: %s", windows)
	}
	posix := accioLaunch(s, "darwin")
	if !strings.Contains(posix, "'\\''") || !strings.Contains(posix, "ADK_MODEL='") {
		t.Fatalf("POSIX command: %s", posix)
	}
}
