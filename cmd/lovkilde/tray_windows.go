package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"github.com/lkarlslund/lovkilde/internal/disclaimer"
)

// trayIcon is the site's seal, the green circle with a §, at the sizes a notification area uses.
//
//go:embed lovkilde.ico
var trayIcon []byte

var (
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleWindow  = kernel32.NewProc("GetConsoleWindow")
	procGetConsoleProcess = kernel32.NewProc("GetConsoleProcessList")
	procShowWindow        = windows.NewLazySystemDLL("user32.dll").NewProc("ShowWindow")

	consoleHidden bool
)

// ownConsole says whether this process is alone in its console window, which is how Windows starts
// a console program from Explorer — a double-click, or the Startup folder — and not from a prompt,
// which shares its window with the program.
func ownConsole() bool {
	var ids [2]uint32
	n, _, _ := procGetConsoleProcess.Call(uintptr(unsafe.Pointer(&ids[0])), 2)
	return n == 1
}

func showWindow(show bool) {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	cmd := uintptr(windows.SW_HIDE)
	if show {
		cmd = windows.SW_SHOW
	}
	procShowWindow.Call(hwnd, cmd)
}

// showConsole brings back a console window the tray hid, so an error can be read, and says
// whether it had been hidden.
func showConsole() bool {
	if !consoleHidden {
		return false
	}
	showWindow(true)
	consoleHidden = false
	return true
}

// startTray puts Lovkilde in the notification area when it was started from Explorer: the seal,
// a menu to open the website, about and exit, and a click on the icon opens the website. Once the
// icon is there the console window is hidden and the log goes to <data-dir>\lovkilde.log as well.
// Anything that keeps the icon from appearing leaves the window as it was: the tray is a
// convenience, and the server runs the same without it. The function returned removes the icon.
func startTray(ctx context.Context, url, dataDir string, exit func()) func() {
	if inService || !ownConsole() {
		return func() {}
	}
	ready, gone := make(chan struct{}), make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(gate.LogWriter(), "the tray icon failed (%v); Lovkilde runs in this window\n", r)
			}
		}()
		// The icon's window and its message loop must share a thread.
		runtime.LockOSThread()
		systray.Run(func() { trayMenu(url, exit); close(ready) }, func() { close(gone) })
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		fmt.Fprintln(gate.LogWriter(), "the tray icon did not appear; Lovkilde runs in this window")
		return func() {}
	case <-ctx.Done():
		return func() {}
	}
	if f, err := os.OpenFile(filepath.Join(dataDir, serviceLog), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		gate.SetOutput(io.MultiWriter(os.Stderr, f))
	}
	showWindow(false)
	consoleHidden = true
	fmt.Fprintf(gate.LogWriter(), "Lovkilde is in the notification area; this window is hidden, and the log is also in %s\n", filepath.Join(dataDir, serviceLog))
	var once sync.Once
	remove := func() {
		once.Do(func() {
			systray.Quit()
			select {
			case <-gone:
			case <-time.After(2 * time.Second):
			}
		})
	}
	go func() {
		<-ctx.Done()
		remove()
	}()
	return remove
}

func trayMenu(url string, exit func()) {
	systray.SetIcon(trayIcon)
	systray.SetTitle("Lovkilde")
	systray.SetTooltip("Lovkilde – " + url)
	systray.SetOnTapped(func() { _ = openBrowser(url) })
	open := systray.AddMenuItem("Åbn Lovkilde", url)
	about := systray.AddMenuItem("Om Lovkilde", "")
	systray.AddSeparator()
	quit := systray.AddMenuItem("Afslut", "Stop Lovkilde")
	go func() {
		for {
			select {
			case <-open.ClickedCh:
				_ = openBrowser(url)
			case <-about.ClickedCh:
				go showAbout(url)
			case <-quit.ClickedCh:
				exit()
				return
			}
		}
	}()
}

func showAbout(url string) {
	text := fmt.Sprintf("Lovkilde %s\n\nDansk lovgivning fra retsinformation.dk på din egen computer, på %s og som MCP-server.\n\n%s\n\nhttps://github.com/lkarlslund/lovkilde",
		version, url, disclaimer.ShortDanish)
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString("Om Lovkilde")
	_, _ = windows.MessageBox(0, t, c, windows.MB_OK|windows.MB_ICONINFORMATION)
}
