package agent

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"github.com/yetone/magpie/internal/appdir"
)

// A custom/portable installation may have no registry record or PATH
// entry. Read its existing desktop/Start-menu link; never save or edit it.
func accioShortcutExecutable(home string) string {
	var links []string
	for _, dir := range []string{appdir.UserFolder(appdir.DesktopFolder), filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs")} {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		for _, name := range []string{"Accio", "Accio Work", "AccioWork"} {
			p := filepath.Join(dir, name+".lnk")
			if isFile(p) {
				links = append(links, p)
			}
		}
	}
	if len(links) == 0 {
		return ""
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		var oe *ole.OleError
		if !errors.As(err, &oe) {
			return ""
		}
		if oe.Code() == 1 {
			defer ole.CoUninitialize()
		} else if oe.Code() != 0x80010106 {
			return ""
		}
	} else {
		defer ole.CoUninitialize()
	}
	unk, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return ""
	}
	defer unk.Release()
	shell, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return ""
	}
	defer shell.Release()
	for _, link := range links {
		v, err := oleutil.CallMethod(shell, "CreateShortcut", link)
		if err != nil {
			continue
		}
		sc := v.ToIDispatch()
		p, err := oleutil.GetProperty(sc, "TargetPath")
		sc.Release()
		if err != nil {
			continue
		}
		target := p.ToString()
		p.Clear()
		name := strings.ToLower(filepath.Base(target))
		if (name == "accio.exe" || name == "accio work.exe" || name == "acciowork.exe") && isFile(target) {
			return target
		}
	}
	return ""
}
