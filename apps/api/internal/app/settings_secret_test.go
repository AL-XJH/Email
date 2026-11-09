package app

import "testing"

func TestSettingSecretsSealAndOpenLegacyValues(t *testing.T) {
	a := newTestAppWithConfigValue(Config{NotificationSecretKey: "test-notification-key"})
	sealed := a.sealSettingSecret("smtp-password")
	if sealed == "smtp-password" || len(sealed) < len("enc:v1:") || sealed[:7] != "enc:v1:" {
		t.Fatalf("secret was not sealed: %q", sealed)
	}
	if got := a.openSettingSecret(sealed); got != "smtp-password" {
		t.Fatalf("open(sealed)=%q", got)
	}
	if got := a.openSettingSecret("legacy-plaintext"); got != "legacy-plaintext" {
		t.Fatalf("legacy value changed: %q", got)
	}
	if got := a.sealSettingSecret(sealed); got != sealed {
		t.Fatal("sealing an already sealed value must be a no-op")
	}
}
