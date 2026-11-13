package main

import "testing"

func TestVendorTelemetryEnvironment(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
		bad   bool
	}{
		{"", true, false},
		{"true", true, false},
		{"false", false, false},
		{"TRUE", false, true},
		{"yes", false, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("VENDOR_TELEMETRY_ENABLED", tc.value)
			got, err := vendorTelemetryFromEnv()
			if got != tc.want || (err != nil) != tc.bad {
				t.Fatalf("value %q: enabled=%v error=%v", tc.value, got, err)
			}
		})
	}
}
