package transport

import "testing"

func TestCleanCommandEcho(t *testing.T) {
	raw := "show version\nCisco IOS Software\nrouter#"
	got := cleanCommandEcho(raw, "show version")
	want := "Cisco IOS Software\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestPromptRegex(t *testing.T) {
	cases := map[string]bool{
		"router#":         true,
		"router>":         true,
		"router(config)#": true,
		"r1$":             true,
		"not a prompt":    false,
		"":                false,
		// MikroTik RouterOS prompt variants
		"[admin@MikroTik] > ":    true,
		"[admin@MikroTik] /ip> ": true,
		"[admin@MikroTik] >":     true,
	}
	for s, want := range cases {
		got := promptRE.MatchString(s)
		if got != want {
			t.Errorf("%q: got %v want %v", s, got, want)
		}
	}
}
