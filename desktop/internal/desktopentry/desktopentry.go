// Package desktopentry installs a "Mist Drive" launcher entry in the
// OS's application menu, pointing at the currently-running executable.
// It's called on every startup so the entry always reflects the latest
// install location — useful when the user moves or reinstalls the
// binary without uninstalling the previous entry.
//
// The public surface is a single Install(iconPNG) function. Each
// supported OS has its own build-tagged implementation file; anything
// not explicitly handled (macOS) is a no-op because .app bundles are
// the correct mechanism there and are produced by `wails build`.
package desktopentry

import (
	"os"
	"path/filepath"
	"strings"
)

// IsDevBinary reports whether the running executable was produced by
// `wails dev` (as opposed to `wails build`). The dev runner names its
// output `<app>-dev-<os>-<arch>` and deletes it when the session ends,
// so installing a launcher entry pointing at it is guaranteed to break
// as soon as dev exits. Production builds drop the `-dev-` infix and
// live at a stable path, which is the only case we want to register.
func IsDevBinary() bool {
	p, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.Contains(filepath.Base(p), "-dev-")
}

// ExecPath returns the absolute path to use as the Exec= target in a
// generated .desktop file, resolving symlinks so it points at the real
// file (not a wrapper symlink the user might delete).
//
// Inside an AppImage, os.Executable() resolves to a path under the
// per-run FUSE mount (/tmp/.mount_*), which is torn down the moment
// this process exits — any .desktop entry written with that path 404s
// on the next launch. The AppImage runtime sets $APPIMAGE to the
// actual, stable path of the .AppImage file on disk, so that takes
// priority whenever it's set.
func ExecPath() (string, error) {
	if appImage := os.Getenv("APPIMAGE"); appImage != "" {
		return appImage, nil
	}
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := os.Readlink(p); err == nil && resolved != "" {
		// os.Readlink returns the link target only if p IS a symlink;
		// on a regular file it errors, which we ignore.
		return resolved, nil
	}
	return p, nil
}
