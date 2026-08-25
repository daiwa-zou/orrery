//go:build bundleweb

package webfs

import (
	"embed"
	"io/fs"
)

// The release workflow copies web/dist here before building with
// `-tags bundleweb`; the directory is gitignored and absent otherwise.
//
//go:embed all:dist
var distFS embed.FS

var bundled fs.FS

func init() {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Unreachable: the embed directive fails the build if dist is missing.
		panic(err)
	}
	bundled = sub
}
