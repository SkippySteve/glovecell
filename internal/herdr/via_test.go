package herdr

import "testing"

func TestViaConfigLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		line    string
		want    string
		wantErr bool
	}{
		{name: "default", want: DefaultConnect},
		{name: "hc connect", line: "hc connect", want: "hc connect"},
		{name: "herdr-core connect", line: "herdr-core connect", want: "herdr-core connect"},
		{name: "reject semicolon", line: "hc connect; rm -rf /", wantErr: true},
		{name: "reject pipe", line: "hc connect | cat", wantErr: true},
		{name: "reject other binary", line: "bash -c evil", wantErr: true},
		{name: "reject newline", line: "hc connect\necho x", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ViaConfig{RemoteLine: tc.line}.line()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestMachineViaCommand(t *testing.T) {
	t.Parallel()
	if got := MachineViaCommand("scrapecast"); got != "agent-project-controller herdr-via scrapecast" {
		t.Fatalf("got %q", got)
	}
}
