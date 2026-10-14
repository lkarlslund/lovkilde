package main

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"runtime"
)

// browserURL is the address a browser on this machine opens for a server bound to addr: a
// server on every interface is reached at loopback.
func browserURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr + "/"
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

// errNoDisplay is returned on a Unix machine with no graphical session, such as a server or a
// systemd unit, where there is no browser to open.
var errNoDisplay = errors.New("no graphical session")

// openBrowser asks the desktop to open url and does not wait for the browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errNoDisplay
		}
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
