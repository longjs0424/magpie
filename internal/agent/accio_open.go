package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
)

var accioOpenMu sync.Mutex

// OpenAccioWork starts the installed desktop app directly, without a shell
// or terminal. Serialize check/start so two clicks cannot start two copies.
func OpenAccioWork(ctx context.Context) error {
	accioOpenMu.Lock()
	defer accioOpenMu.Unlock()
	home, err := appdir.Home()
	if err != nil {
		return err
	}
	model, err := gateway.AccioModel()
	if err != nil {
		return err
	}
	s := Starting{Bin: accioExecutable(home), Model: model, Env: accioEnv(gateway.URL(), model)}
	return openAccioWork(ctx, runtime.GOOS, s, proc.List, func(s Starting) error {
		cmd := proc.Command(s.Bin)
		cmd.Dir = filepath.Dir(s.Bin)
		cmd.Env = append(os.Environ(), s.Env...)
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	})
}

func openAccioWork(ctx context.Context, platform string, s Starting, list func(context.Context, string) ([]proc.Process, error), start func(Starting) error) error {
	if platform != "windows" && platform != "darwin" {
		return errors.New("Accio Work desktop launch is available on Windows and macOS only")
	}
	if s.Bin == "" {
		return errors.New("Accio Work was not found; set MAGPIE_ACCIO_BIN to its executable")
	}
	if s.Model == "" {
		return errors.New("choose an Accio Work model in magpie first")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ps, err := list(ctx, filepath.Base(s.Bin))
	if err != nil {
		return fmt.Errorf("check whether Accio Work is running: %w", err)
	}
	for _, p := range ps {
		args := strings.TrimLeft(p.Args, "\"")
		bin := s.Bin
		if platform == "windows" {
			args, bin = strings.ToLower(args), strings.ToLower(bin)
		}
		// An unreadable Windows Accio* process is unknown, not absent.
		if platform == "windows" && args == "" {
			return errors.New("could not check a running Accio process; fully quit Accio Work first")
		}
		if strings.HasPrefix(args, bin) && (len(args) == len(bin) || strings.ContainsRune("\" \t", rune(args[len(bin)]))) {
			return errors.New("Accio Work is already running; fully quit it, then click Open again")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return start(s)
}
