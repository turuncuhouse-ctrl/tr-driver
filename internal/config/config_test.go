package config

import "testing"

func TestParseTrustedProxies(t *testing.T) {
	got, err := parseTrustedProxies(" 10.0.0.0/8, 192.168.1.5/32 ,, ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].String() != "10.0.0.0/8" || got[1].String() != "192.168.1.5/32" {
		t.Fatalf("got %v", got)
	}
	if _, err := parseTrustedProxies("not-a-cidr"); err == nil {
		t.Fatal("expected error")
	}
}
