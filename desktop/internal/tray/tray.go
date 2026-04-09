// Package tray owns the system tray icon and its menu. It stays
// deliberately dumb: it knows how to draw the icon, flip a "paused"
// label, and invoke callbacks. The wiring to the sync engine, window
// show/hide and quit lives in main.go so the tray has zero knowledge
// of Wails internals (which keeps it testable and swappable).
package tray

import (
	"fyne.io/systray"
)

// Callbacks is the minimal surface the tray needs from the app. Keeping
// it as a struct of funcs (not an interface) means main.go can construct
// it inline without a shim type.
type Callbacks struct {
	OnOpen func() // show the main window
	OnQuit func() // graceful full exit
}

// Start launches the systray. fyne.io/systray runs its onReady callback
// on a background goroutine on Linux/Windows, so this is non-blocking.
// iconPNG must be raw PNG bytes; systray handles the decode.
func Start(iconPNG []byte, tooltip string, cb Callbacks) {
	onReady := func() {
		systray.SetIcon(iconPNG)
		systray.SetTooltip(tooltip)

		open := systray.AddMenuItem("Open Mist Drive", "Show the main window")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quit", "Exit Mist Drive")

		go func() {
			for {
				select {
				case <-open.ClickedCh:
					if cb.OnOpen != nil {
						cb.OnOpen()
					}
				case <-quit.ClickedCh:
					if cb.OnQuit != nil {
						cb.OnQuit()
					}
					systray.Quit()
					return
				}
			}
		}()
	}
	// onExit is a no-op — OnQuit does the app-level cleanup before
	// calling systray.Quit().
	go systray.Run(onReady, func() {})
}
