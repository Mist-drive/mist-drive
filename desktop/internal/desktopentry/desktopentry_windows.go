//go:build windows

package desktopentry

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Install creates (or overwrites) a Start Menu shortcut at
//
//	%AppData%\Microsoft\Windows\Start Menu\Programs\Mist Drive.lnk
//
// pointing at the currently running executable. The icon is read
// straight from the .exe itself — Wails embeds appicon.ico into the
// Windows build, so `IconLocation = <exe>,0` gives us the right
// icon without needing to ship a separate .ico file.
//
// We shell out to PowerShell's WScript.Shell COM object instead of
// pulling in a go-ole dependency: .lnk creation is a one-shot at
// startup and COM via PowerShell is a four-line script.
func Install(_ []byte) error {
	exe, err := execPath()
	if err != nil {
		return fmt.Errorf("exec path: %w", err)
	}
	appData := os.Getenv("AppData")
	if appData == "" {
		return fmt.Errorf("AppData env not set")
	}
	startMenu := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs")
	if err := os.MkdirAll(startMenu, 0o755); err != nil {
		return fmt.Errorf("mkdir start menu: %w", err)
	}
	lnk := filepath.Join(startMenu, "Mist Drive.lnk")
	workDir := filepath.Dir(exe)

	// PowerShell is present on every supported Windows SKU.
	// Single-quoted args because PS parses them as literal strings
	// — no escape dance for paths with spaces.
	script := fmt.Sprintf(
		`$s=(New-Object -ComObject WScript.Shell).CreateShortcut('%s');`+
			`$s.TargetPath='%s';`+
			`$s.WorkingDirectory='%s';`+
			`$s.IconLocation='%s,0';`+
			`$s.Description='Mist Drive';`+
			`$s.Save()`,
		psEscape(lnk), psEscape(exe), psEscape(workDir), psEscape(exe))

	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("powershell: %w: %s", err, string(out))
	}
	return nil
}

// psEscape escapes single quotes for PowerShell single-quoted strings
// (the PowerShell rule is: a single quote is represented as two
// single quotes).
func psEscape(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'', '\'')
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}
