//go:build linux

package desktopentry

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Install writes (or overwrites) ~/.local/share/applications/mist-drive.desktop
// pointing at the currently running executable, and drops the icon
// PNG next to it so the launcher has something to render.
//
// Idempotent by design: every call rewrites both files atomically,
// so moving the binary and relaunching updates the menu entry.
func Install(iconPNG []byte) error {
	exe, err := execPath()
	if err != nil {
		return fmt.Errorf("exec path: %w", err)
	}

	// XDG_DATA_HOME with the standard ~/.local/share fallback —
	// mirroring what every well-behaved Linux app does.
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("home dir: %w", err)
		}
		dataHome = filepath.Join(home, ".local", "share")
	}

	iconDir := filepath.Join(dataHome, "mist-drive")
	if err := os.MkdirAll(iconDir, 0o755); err != nil {
		return fmt.Errorf("mkdir icon dir: %w", err)
	}
	iconPath := filepath.Join(iconDir, "icon.png")
	if err := atomicWrite(iconPath, iconPNG, 0o644); err != nil {
		return fmt.Errorf("write icon: %w", err)
	}

	appsDir := filepath.Join(dataHome, "applications")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir applications: %w", err)
	}
	// Quote the exec path so spaces in install locations don't get
	// parsed as separate argv entries by the launcher. The .desktop
	// spec says double quotes are the standard way to quote Exec.
	quotedExe := `"` + exe + `"`
	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Mist Drive
Comment=Self-hosted file sync
Exec=%s
TryExec=%s
Icon=%s
Terminal=false
Categories=Network;FileTransfer;Utility;
StartupWMClass=mist-drive-desktop
`, quotedExe, exe, iconPath)

	desktopPath := filepath.Join(appsDir, "mist-drive.desktop")
	// 0o755 is required by GNOME on Ubuntu 22.04+: .desktop files in
	// ~/.local/share/applications/ that point at binaries OUTSIDE
	// $PATH (e.g. ~/Downloads, ~/Apps) are silently refused unless
	// the file itself carries the executable bit. Writing with
	// 0o644 gives the "not in PATH" error the user just hit.
	if err := atomicWrite(desktopPath, []byte(entry), 0o755); err != nil {
		return err
	}

	// Mark the file as trusted so Nautilus (and, on some Ubuntu
	// builds, the Activities overview) launches it without the
	// "Untrusted Application Launcher" dialog. `gio` ships with
	// GNOME's userspace — if it's missing we don't care, the launch
	// works from the menu regardless thanks to the +x above.
	_ = exec.Command("gio", "set", desktopPath, "metadata::trusted", "true").Run()

	// Refresh GNOME's application cache. Without this the new entry
	// can take a minute to appear (or require a shell reload) on the
	// first install. Safe to ignore failures — it's best-effort.
	_ = exec.Command("update-desktop-database", appsDir).Run()

	return nil
}

// atomicWrite replaces dst in one step via temp+rename so a crash
// mid-write can never leave a half-written launcher entry on disk.
// Same pattern the settings store uses.
func atomicWrite(dst string, data []byte, mode os.FileMode) error {
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
