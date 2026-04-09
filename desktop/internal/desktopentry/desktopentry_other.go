//go:build !linux

package desktopentry

// Install is a no-op on every platform except Linux.
//
// - macOS: the right unit of installation is a .app bundle produced by
//   `wails build`, not a runtime-generated launcher.
// - Windows: we used to write a Start Menu shortcut from a PowerShell
//   COM call, but that briefly flashed a PowerShell window on every
//   launch — which looked legitimately spooky to users — and the
//   shortcut never picked up the right icon anyway. If we want a Start
//   Menu entry on Windows later, the right place is an installer
//   (NSIS / MSIX / wails built-in installer), not a runtime poke at
//   the user's profile.
func Install(_ []byte) error { return nil }
