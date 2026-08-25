// Package webfs lets release binaries carry the compiled frontend inside the
// executable, so a downloaded binary is the whole dashboard. Dev builds and
// the container image don't pay for it: without the `bundleweb` build tag the
// bundle is absent and static serving comes from server.webRoot as before.
package webfs

import "io/fs"

// FS returns the embedded web assets, or nil when this build carries none.
func FS() fs.FS { return bundled }
