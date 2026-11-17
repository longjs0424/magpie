package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

func TestAccioWorkDesktopShortcut(t *testing.T) {
	home := syncHome(t)
	t.Setenv("MAGPIE_ACCIO_BIN", "")
	bin := filepath.Join(home, "custom install", "Accio.exe")
	link := filepath.Join(home, "Desktop", "Accio.lnk")
	for _, dir := range []string{filepath.Dir(bin), filepath.Dir(link)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(bin, []byte("test executable"), 0600); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		if e, ok := err.(*ole.OleError); !ok || e.Code() != 1 {
			t.Fatal(err)
		}
	}
	defer ole.CoUninitialize()
	unk, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		t.Fatal(err)
	}
	defer unk.Release()
	shell, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Release()
	v, err := oleutil.CallMethod(shell, "CreateShortcut", link)
	if err != nil {
		t.Fatal(err)
	}
	sc := v.ToIDispatch()
	defer sc.Release()
	if _, err := oleutil.PutProperty(sc, "TargetPath", bin); err != nil {
		t.Fatal(err)
	}
	if _, err := oleutil.CallMethod(sc, "Save"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(link)
	if err != nil {
		t.Fatal(err)
	}
	if got := accioExecutable(home); got != bin {
		t.Fatalf("found %q, want shortcut target %q", got, bin)
	}
	after, err := os.ReadFile(link)
	if err != nil || string(before) != string(after) {
		t.Fatalf("shortcut was changed: %v", err)
	}
}
