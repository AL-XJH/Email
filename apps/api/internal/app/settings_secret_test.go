package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

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

func TestSaveSystemSettingsSealsEverySecret(t *testing.T) {
	a := newTestApp(t)
	a.updateConfig(func(c *Config) { c.NotificationSecretKey = "test-notification-key" })
	cfg := *a.config()
	cfg.SMTPPassword = "smtp-secret"
	cfg.TurnstileSecretKey = "turnstile-secret"
	cfg.LinuxDoClientSecret = "linuxdo-secret"
	cfg.ExternalIMAPSecretKey = "imap-root-secret"
	cfg.ExternalIMAPGmailClientSecret = "gmail-secret"
	cfg.ExternalIMAPOutlookClientSecret = "outlook-secret"
	if err := a.saveSystemSettings(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"smtpPassword", "turnstileSecretKey", "linuxDoClientSecret", "externalImapSecretKey", "externalImapGmailClientSecret", "externalImapOutlookClientSecret"} {
		var value string
		if err := a.db.QueryRow(`SELECT value FROM system_settings WHERE key=?`, key).Scan(&value); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if !strings.HasPrefix(value, "enc:v1:") {
			t.Fatalf("%s stored in clear: %q", key, value)
		}
	}
}

func TestSealPersistedSettingSecretsMigratesLegacyPlaintext(t *testing.T) {
	a := newTestApp(t)
	a.updateConfig(func(c *Config) { c.NotificationSecretKey = "test-notification-key" })
	if _, err := a.db.Exec(`INSERT INTO system_settings(key,value,updated_at) VALUES('smtpPassword','legacy-pass',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, a.now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := a.sealPersistedSettingSecrets(t.Context()); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := a.db.QueryRow(`SELECT value FROM system_settings WHERE key='smtpPassword'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(value, "enc:v1:") {
		t.Fatalf("legacy secret not migrated: %q", value)
	}
	if got := a.openSettingSecret(value); got != "legacy-pass" {
		t.Fatalf("migrated secret does not decrypt: %q", got)
	}
}
