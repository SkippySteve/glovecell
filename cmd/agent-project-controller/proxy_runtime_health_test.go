package main

import "testing"

func TestProxyRuntimeGuardEnvironmentFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		value     string
		want, bad bool
	}{{"", false, false}, {"true", true, false}, {"false", false, false}, {"TRUE", false, true}, {"yes", false, true}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("PROXY_RUNTIME_GUARD_ENABLED", tc.value)
			got, err := proxyRuntimeGuardFromEnv()
			if got != tc.want || (err != nil) != tc.bad {
				t.Fatal(got, err)
			}
		})
	}
}
