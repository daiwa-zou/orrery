package webfs

import "testing"

// The router asks this package one question — `bundle := webfs.FS(); bundle
// != nil` — and reads a nil answer as "this build carries no frontend, keep
// serving from server.webRoot". An ordinary build must therefore return nil
// and not an empty-but-non-nil fs.FS, which would take that branch and hand
// every client route a 404 from a filesystem with nothing in it.
//
// Only the default build is exercised here. Producing a `bundleweb` build
// needs web/dist copied into this package, which the release workflow does and
// which is gitignored the rest of the time, so a test of that tag would fail
// to compile in every checkout rather than test anything.
func TestFSIsNilWithoutTheBundleTag(t *testing.T) {
	if got := FS(); got != nil {
		t.Errorf("FS() = %#v, want nil: an untagged build carries no frontend", got)
	}
}
