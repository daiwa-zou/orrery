package api

import (
	"net/http"
	"testing"

	"github.com/daiwa-zou/orrery/internal/config"
)

// The flag was added after the proxy shipped, so an existing config that never
// mentions it must keep the behaviour it already had. Absent means enabled.
func TestProxyEnabledDefaultsOn(t *testing.T) {
	on, off := true, false

	cases := []struct {
		name string
		cfg  config.ProxyConfig
		want bool
	}{
		{name: "absent from the config", cfg: config.ProxyConfig{}, want: true},
		{name: "explicitly enabled", cfg: config.ProxyConfig{Enabled: &on}, want: true},
		{name: "explicitly disabled", cfg: config.ProxyConfig{Enabled: &off}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.ProxyEnabled(); got != tc.want {
				t.Errorf("ProxyEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Disabling must remove the route, not just the button. A handler that is
// still mounted is still reachable by typing the URL, which is the whole
// difference between a feature flag and a hidden control.
func TestDisabledProxyRouteIsAbsent(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			rig := hndNewRigWith(t, func(c *config.Config) {
				v := enabled
				c.Proxy.Enabled = &v
			})

			rec := rig.do(t, http.MethodGet,
				"/api/v1/clusters/fake/proxy/demo/pods/web-1/healthz", "", nil)

			if enabled {
				if rec.Code == http.StatusNotFound {
					t.Fatalf("proxy route missing while enabled (status %d)", rec.Code)
				}
			} else if rec.Code != http.StatusNotFound {
				t.Fatalf("disabled proxy answered with %d, want 404", rec.Code)
			}
		})
	}
}
