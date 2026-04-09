package main

import (
	"embed"
	"fmt"
	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"mist-drive-desktop/internal/desktopentry"
	"mist-drive-desktop/internal/tray"
)

// version is overridden at build time via
//
//	go build -ldflags "-X main.version=v1.2.3" ...
//
// Defaults to "dev" so an ad-hoc `wails dev` build still shows
// something sensible in the header instead of an empty string.
var version = "dev"

//go:embed all:frontend/dist
var assets embed.FS

// Two copies of the same icon embedded as different formats because
// fyne.io/systray is picky per OS: Linux/macOS decode PNG bytes,
// Windows needs a real .ico with its sub-icon directory. Passing a
// PNG on Windows results in an empty tray slot (the root cause of
// the "no tray icon on Windows" bug). The Linux desktop launcher
// helper keeps using the PNG because that's what .desktop + GNOME's
// icon theme expect.
//
//go:embed build/appicon.png
var trayIconPNG []byte

//go:embed build/windows/icon.ico
var trayIconICO []byte

// trayIcon returns the right byte slice for the current OS.
func trayIcon() []byte {
	if runtime.GOOS == "windows" {
		return trayIconICO
	}
	return trayIconPNG
}

func main() {
	app := NewApp()
	app.version = version

	// Register the app with the OS launcher every time we start (but
	// skip `wails dev` — its binary name contains "-dev-" and gets
	// deleted when the dev session ends, so any .desktop entry we
	// wrote would immediately break). Production `wails build`
	// binaries live at a stable path and always register. Failure is
	// non-fatal: a missing menu entry is annoying, not broken.
	if desktopentry.IsDevBinary() {
		fmt.Println("[desktop entry] skipping install (dev binary)")
	} else if err := desktopentry.Install(trayIconPNG); err != nil {
		fmt.Println("[desktop entry]", err)
	}

	// Start the system tray before Wails. fyne.io/systray runs its own
	// goroutine on Linux/Windows so it does not compete with Wails for
	// the main thread. The tray calls back into the App for state
	// changes — it has no knowledge of Wails runtime.
	tray.Start(trayIcon(), "Mist Drive", tray.Callbacks{
		OnOpen: app.ShowWindow,
		OnQuit: app.RequestQuit,
	})

	err := wails.Run(&options.App{
		Title:     "Mist Drive",
		Width:     1024,
		Height:    768,
		MinWidth:  800,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		// Intercept window close so the app hides to the tray instead
		// of exiting. The tray's Quit menu sets forceQuit first, which
		// lets the next close through.
		OnBeforeClose: app.beforeClose,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
