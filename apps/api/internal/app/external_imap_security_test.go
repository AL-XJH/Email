package app

import "testing"

func TestPlaintextIMAPOnlyAllowedForLoopback(t *testing.T) {
	a := newTestAppWithConfigValue(Config{})
	for _, host := range []string{"127.0.0.1", "localhost", "[::1]"} {
		if !a.plaintextIMAPAllowed(host) {
			t.Fatalf("loopback host %q should allow plaintext IMAP", host)
		}
	}
	for _, host := range []string{"imap.example.com", "10.0.0.5", "192.168.1.2", "::1.example"} {
		if a.plaintextIMAPAllowed(host) {
			t.Fatalf("non-loopback host %q must not allow plaintext IMAP", host)
		}
	}
	a = newTestAppWithConfigValue(Config{ExternalIMAPAllowInsecure: true})
	if !a.plaintextIMAPAllowed("imap.example.com") {
		t.Fatal("explicit opt-in should allow plaintext IMAP")
	}
}
