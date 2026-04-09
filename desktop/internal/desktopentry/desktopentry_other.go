//go:build !linux && !windows

package desktopentry

// Install is a no-op on platforms where a runtime-generated launcher
// entry is the wrong mechanism — notably macOS, where the correct
// unit of installation is a .app bundle produced by `wails build`.
func Install(_ []byte) error { return nil }
