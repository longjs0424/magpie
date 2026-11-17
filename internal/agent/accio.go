package agent

// Accio Work reads these launch variables (TF49/AI-Helper's
// process_manager.rs). No installed bundle or Accio settings are patched.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
)

var noticeAccioOpen = newNotice("Fully quit Accio Work, then click Open on its Agents row to use the selected model.")

func accio(home string) *Agent {
	path := gateway.AccioConfigPath()
	get := jsonGet(path, "model")
	start := func() Starting {
		s := Starting{Bin: accioExecutable(home)}
		if model := get(); usesMagpie(model) {
			s.Model = strings.TrimPrefix(model, magpieID+"/")
			s.Env = accioEnv(gateway.URL(), s.Model)
		}
		return s
	}
	launch := func() string {
		s := start()
		if s.Model == "" || s.Bin == "" {
			return ""
		}
		return accioLaunch(s, runtime.GOOS)
	}
	return &Agent{
		ID: "accio", Name: "Accio Work", Icon: "generic", Aliases: []string{"acciowork", "accio-work"},
		UA:  []string{"accio", "accio-work", "acciowork"},
		Dir: filepath.Join(home, ".accio"), Path: path, Spelled: prefixed,
		detect: func() bool { return isDir(filepath.Join(home, ".accio")) || accioExecutable(home) != "" },
		Launch: launch, Start: start,
		Notice: func() string {
			if launch() != "" {
				return noticeAccioOpen.String()
			}
			return ""
		},
		Fields: []Field{{
			Key: "model", Label: "model", Get: get,
			Set: func(v string) error {
				if v == "" {
					return edit.DelJSON(path, "model")
				}
				if !usesMagpie(v) {
					return fmt.Errorf("choose a magpie model for Accio Work")
				}
				return edit.SetJSON(path, edit.KV{Path: "model", Value: v})
			},
			Options: func(map[string]string) []Option {
				return append([]Option{{Value: "", Note: "default"}}, viaMagpie("accio", magpieID+"/")...)
			},
		}},
	}
}

// accioExecutable accepts an explicit install path, then looks only in
// known install locations and PATH. Nothing is launched during detection.
func accioExecutable(home string) string {
	if p := appdir.Getenv("MAGPIE_ACCIO_BIN"); p != "" {
		if isFile(p) {
			return p
		}
		return ""
	}
	var candidates []string
	if runtime.GOOS == "windows" {
		for _, root := range []string{os.Getenv("LOCALAPPDATA"), filepath.Join(home, "AppData", "Local")} {
			for _, name := range []string{"Accio", "Accio Work", "AccioWork"} {
				for _, exe := range []string{"Accio.exe", "Accio Work.exe", "AccioWork.exe"} {
					candidates = append(candidates, filepath.Join(root, "Programs", name, exe), filepath.Join(root, name, exe))
				}
			}
		}
	} else if runtime.GOOS == "darwin" {
		for _, root := range append(proc.SystemDirs("/Applications"), filepath.Join(home, "Applications")) {
			for _, name := range []string{"Accio", "Accio Work"} {
				candidates = append(candidates, filepath.Join(root, name+".app", "Contents", "MacOS", name))
			}
		}
	}
	for _, p := range candidates {
		if isFile(p) {
			return p
		}
	}
	if p := accioShortcutExecutable(home); p != "" {
		return p
	}
	return proc.FindTool("accio")
}

func accioEnv(gw, model string) []string {
	base := strings.TrimRight(gw, "/") + gateway.AccioPath
	return []string{
		"GATEWAY_BASE_URL=" + base,
		"ADK_BASE_URL=" + base + "/api/adk/llm",
		"ADK_MODEL=" + model,
		"EMBEDDING_BASE_URL=" + base + "/api/adk/embedding/embed",
	}
}

// accioLaunch quotes executable paths and values as data, including a
// user-defined model name containing shell metacharacters.
func accioLaunch(s Starting, platform string) string {
	if platform == "windows" {
		q := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
		var before, set, restore []string
		for i, kv := range s.Env {
			k, v, _ := strings.Cut(kv, "=")
			tmp := fmt.Sprintf("$accioPrevious%d", i)
			before = append(before, tmp+" = $env:"+k)
			set = append(set, "$env:"+k+" = "+q(v))
			restore = append(restore, "$env:"+k+" = "+tmp)
		}
		return strings.Join(before, "; ") + "; try { " + strings.Join(set, "; ") + "; & " + q(s.Bin) + " } finally { " + strings.Join(restore, "; ") + " }"
	}
	var out []string
	for _, kv := range s.Env {
		k, v, _ := strings.Cut(kv, "=")
		out = append(out, k+"="+posixQuote(v))
	}
	return strings.Join(out, " ") + " " + posixQuote(s.Bin)
}
