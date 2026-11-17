package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
)

func TestAccioWorkDesktopChildEnvironment(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("desktop platforms")
	}
	home := syncHome(t)
	output := filepath.Join(home, "child-env.txt")
	bin := filepath.Join(home, "accio-test")
	script := "#!/bin/sh\nprintf '%s\\n' \"$GATEWAY_BASE_URL\" \"$ADK_BASE_URL\" \"$ADK_MODEL\" \"$EMBEDDING_BASE_URL\" > " + posixQuote(output) + "\n"
	if runtime.GOOS == "windows" {
		bin += ".cmd"
		script = "@echo off\r\n(\r\necho %GATEWAY_BASE_URL%\r\necho %ADK_BASE_URL%\r\necho %ADK_MODEL%\r\necho %EMBEDDING_BASE_URL%\r\n)>\"" + output + "\"\r\n"
	}
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_ACCIO_BIN", bin)
	t.Setenv("ADK_MODEL", "old-model")
	if err := os.MkdirAll(filepath.Dir(gateway.AccioConfigPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gateway.AccioConfigPath(), []byte(`{"model":"magpie/fake/m1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := OpenAccioWork(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(output)
		if err == nil && strings.Count(string(b), "\n") >= 4 {
			got := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(b), "\r", "")), "\n")
			want := []string{gateway.URL() + "/accio", gateway.URL() + "/accio/api/adk/llm", "fake/m1", gateway.URL() + "/accio/api/adk/embedding/embed"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("child environment: %q, want %q", got, want)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("test child did not write its environment")
}

func TestAccioWorkDesktopOpen(t *testing.T) {
	for _, platform := range []string{"windows", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			bin := "/Applications/Accio Work.app/Contents/MacOS/Accio Work"
			if platform == "windows" {
				bin = `D:\Accio Work\Accio.exe`
			}
			s := Starting{Bin: bin, Model: "fake/m1", Env: accioEnv("http://127.0.0.1:3425", "fake/m1")}
			for _, state := range []string{"stopped", "running", "unreadable", "list-error", "start-error", "cancelled"} {
				t.Run(state, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					if state == "cancelled" {
						cancel()
					}
					started := false
					err := openAccioWork(ctx, platform, s, func(context.Context, string) ([]proc.Process, error) {
						if state == "list-error" {
							return nil, errors.New("cannot list")
						}
						if state == "running" {
							return []proc.Process{{PID: 42, Args: `"` + bin + `" --flag`}}, nil
						}
						if state == "unreadable" && platform == "windows" {
							return []proc.Process{{PID: 42}}, nil
						}
						return []proc.Process{{PID: 43, Args: bin + "-unrelated"}}, nil
					}, func(got Starting) error {
						started = true
						if !reflect.DeepEqual(got, s) {
							t.Fatalf("launch settings changed: %+v", got)
						}
						if state == "start-error" {
							return errors.New("cannot start")
						}
						return nil
					})
					wantStart := state == "stopped" || state == "start-error" || state == "unreadable" && platform != "windows"
					wantOK := wantStart && state != "start-error"
					if started != wantStart || (err == nil) != wantOK {
						t.Fatalf("started=%v error=%v", started, err)
					}
				})
			}
		})
	}
	for _, s := range []Starting{{}, {Bin: "fake"}} {
		if err := openAccioWork(context.Background(), "windows", s, nil, nil); err == nil {
			t.Fatal("missing executable or model accepted")
		}
	}
	if err := openAccioWork(context.Background(), "linux", Starting{}, nil, nil); err == nil {
		t.Fatal("unsupported desktop accepted")
	}
}
