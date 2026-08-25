//go:build !bundleweb

package webfs

import "io/fs"

var bundled fs.FS
