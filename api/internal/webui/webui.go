// Package webui serves the embedded Vite SPA.
//
// The built frontend (`web/dist/`) is baked into the Go binary via
// `//go:embed` so the whole stack ships as one container. This removes
// a moving part: API and UI can never drift out of sync across a
// rollout, same-origin means no CORS, and there's only one image to
// build, tag, and deploy.
//
// The Mount function MUST be called AFTER all real routes are
// registered on the app. Fiber runs handlers in registration order and
// falls through to later middleware only if earlier ones didn't write
// a response — so api/auth/ws routes take precedence automatically and
// everything else lands on the SPA.
//
// Caching strategy (mirrors the previous nginx rules):
//
//	/assets/*          → public, max-age=1y, immutable  (Vite content-hashes)
//	*.{js,css,png,…}   → same                           (favicon, fonts, …)
//	.html / no ext     → no-cache, no-store, must-revalidate
//
// The "never cache HTML" rule is the important one: index.html holds
// the <script src="/assets/index.<hash>.js"> reference, so if it's
// cached, a redeploy ships new assets under new hashes that the old
// index.html never asks for — and users keep seeing the stale app
// until they hard-refresh. no-store on index.html is non-negotiable.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/filesystem"
)

// The `all:` prefix makes embed include dotfiles and files under
// directories starting with an underscore (Vite occasionally emits
// `_app/` in some setups; harmless to allow-list it preemptively).
//
//go:embed all:dist
var distFS embed.FS

// Mount wires the SPA as a fall-through handler on app. Returns an
// error only if the embedded filesystem is unusable — which can only
// happen if someone deletes dist/index.html without a rebuild.
func Mount(app *fiber.App) error {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return err
	}

	// Cache-Control is decided per request by extension. This runs
	// only for paths that fell through every earlier route, so it
	// never touches /api, /auth, /health, /ws, or /download-zip responses.
	app.Use(func(c *fiber.Ctx) error {
		if c.Method() == fiber.MethodGet || c.Method() == fiber.MethodHead {
			if isImmutableAsset(c.Path()) {
				c.Set(fiber.HeaderCacheControl, "public, max-age=31536000, immutable")
			} else {
				// index.html itself + every SPA client route (no
				// extension → filesystem middleware falls back to
				// index.html → still HTML).
				c.Set(fiber.HeaderCacheControl, "no-store, no-cache, must-revalidate")
			}
		}
		return c.Next()
	})

	app.Use("/", filesystem.New(filesystem.Config{
		Root:         http.FS(sub),
		Index:        "index.html",
		NotFoundFile: "index.html", // SPA fallback for client-side routes
	}))
	return nil
}

// isImmutableAsset decides whether a path looks like a Vite-built,
// content-hashed static asset safe to cache forever. We check the
// extension rather than a /assets/ prefix because the favicon, fonts
// and any public/ files live at the root.
func isImmutableAsset(p string) bool {
	dot := strings.LastIndex(p, ".")
	if dot < 0 || dot < strings.LastIndex(p, "/") {
		return false
	}
	switch strings.ToLower(p[dot:]) {
	case ".js", ".mjs", ".css", ".map",
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".ico", ".svg",
		".woff", ".woff2", ".ttf", ".otf", ".eot":
		return true
	}
	return false
}
