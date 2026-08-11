package mailer

import "testing"

func TestResolveTLSMode(t *testing.T) {
	cases := []struct {
		port   int
		useTLS bool
		mode   string
		want   string
	}{
		{587, true, "auto", "starttls"},
		{465, true, "auto", "smtps"},
		{587, false, "auto", "none"},
		{587, true, "smtps", "smtps"},
		{465, true, "starttls", "starttls"},
		{25, true, "none", "none"},
	}
	for _, c := range cases {
		got := resolveTLSMode(Settings{Port: c.port, UseTLS: c.useTLS, TLSMode: c.mode})
		if got != c.want {
			t.Fatalf("port=%d useTLS=%v mode=%q => %q want %q", c.port, c.useTLS, c.mode, got, c.want)
		}
	}
}
