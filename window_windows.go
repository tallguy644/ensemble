//go:build windows

package main

import (
	"errors"
	"net/url"
	"path/filepath"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

var ErrNoWindow = errors.New("no WebView2")

var (
	user32                        = windows.NewLazySystemDLL("user32.dll")
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware        = user32.NewProc("SetProcessDPIAware")
	procGetDpiForSystem           = user32.NewProc("GetDpiForSystem")
	procMessageBoxW               = user32.NewProc("MessageBoxW")
)

// makeDPIAware stops Windows from bitmap-stretching (blurring) the window on
// scaled displays, and returns the scale factor (1.0 = 100%).
func makeDPIAware() float64 {
	const systemAware = ^uintptr(1) // DPI_AWARENESS_CONTEXT_SYSTEM_AWARE (-2)
	if procSetProcessDpiAwarenessCtx.Find() == nil {
		procSetProcessDpiAwarenessCtx.Call(systemAware)
	} else if procSetProcessDPIAware.Find() == nil {
		procSetProcessDPIAware.Call()
	}
	if procGetDpiForSystem.Find() == nil {
		if dpi, _, _ := procGetDpiForSystem.Call(); dpi >= 96 {
			return float64(dpi) / 96
		}
	}
	return 1
}

// RunWindow shows the dashboard in a native window and blocks until it closes.
func RunWindow(pageURL string) error {
	scale := makeDPIAware()
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  filepath.Join(EnsureHome(), "webview2"),
		WindowOptions: webview2.WindowOptions{
			Title:  "Ensemble",
			Width:  uint(1280 * scale),
			Height: uint(860 * scale),
			IconId: 1, // from the embedded resource file; falls back to the default icon
			Center: true,
		},
	})
	if w == nil {
		return ErrNoWindow
	}
	defer w.Destroy()
	w.SetSize(int(760*scale), int(560*scale), webview2.HintMin)
	// Links (e.g. Perplexity sources) open in the normal browser, never inside the app.
	_ = w.Bind("ensembleOpenExternal", func(u string) {
		if p, err := url.Parse(u); err == nil && (p.Scheme == "https" || p.Scheme == "http") {
			OpenBrowser(p.String())
		}
	})
	_ = w.Bind("ensemblePing", func() { Ping() })
	w.Navigate(pageURL)
	w.Run()
	return nil
}

// ShowError shows a message box (the app has no console window).
func ShowError(title, msg string) {
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(msg)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10)
}
