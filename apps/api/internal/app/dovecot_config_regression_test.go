package app

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dovecotConfigFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../../../../deploy/dovecot", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func dovecotQuery(t *testing.T, content, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(content, "\n") {
		if query, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
			return query
		}
	}
	t.Fatalf("missing query %q", prefix)
	return ""
}

func TestDovecotPolicyFailsClosed(t *testing.T) {
	content := dovecotConfigFile(t, "dovecot.conf")
	if !strings.Contains(content, "\nauth_policy_reject_on_fail = yes\n") {
		t.Fatal("Dovecot must reject authentication if the policy API is unavailable")
	}
}

func TestDovecotPassdbParity(t *testing.T) {
	conf := dovecotConfigFile(t, "dovecot.conf")
	query := dovecotQuery(t, conf, "query = SELECT address AS user, password_hash AS password ")
	query = "SELECT address AS user, password_hash AS password " + query
	legacy := dovecotQuery(t, dovecotConfigFile(t, "dovecot-sql.conf.ext"), "password_query = ")
	generated := dovecotQuery(t, dovecotConfigFile(t, "configure-db.sh"), "password_query = ")
	if legacy != generated || strings.ReplaceAll(query, "%{user}", "%u") != legacy {
		t.Fatal("2.4 passdb, legacy SQL and configure-db.sh must agree")
	}
}

func TestDovecotUserdbCatchAllAndQuotaParity(t *testing.T) {
	queries := map[string]string{
		"2.4":              dovecotQuery(t, dovecotConfigFile(t, "dovecot.conf"), "query = SELECT '/var/mail/vhosts/'"),
		"legacy":           dovecotQuery(t, dovecotConfigFile(t, "dovecot-sql.conf.ext"), "user_query = "),
		"generated legacy": dovecotQuery(t, dovecotConfigFile(t, "configure-db.sh"), "user_query = "),
	}
	queries["2.4"] = "SELECT '/var/mail/vhosts/'" + queries["2.4"]
	if queries["legacy"] != queries["generated legacy"] {
		t.Fatal("legacy SQL template and configure-db.sh have different userdb queries")
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, stmt := range []string{
				`CREATE TABLE domains (id TEXT, name TEXT, status TEXT)`,
				`CREATE TABLE mailboxes (domain_id TEXT, local_part TEXT, quota_mb INTEGER, status TEXT)`,
				`CREATE TABLE system_settings (key TEXT, value TEXT)`,
				`INSERT INTO domains VALUES ('domain','example.test','active')`,
				`INSERT INTO mailboxes VALUES ('domain','alice',0,'active')`,
				`INSERT INTO system_settings VALUES ('catchAllEnabled','false')`,
			} {
				if _, err := db.Exec(stmt); err != nil {
					t.Fatal(err)
				}
			}
			lookup := func(address, home, quota string) {
				t.Helper()
				username, domain, _ := strings.Cut(address, "@")
				q := strings.NewReplacer("%{user | domain}", domain, "%{user | username}", username, "%u", address).Replace(query)
				rows, err := db.Query(q)
				if err != nil {
					t.Fatalf("query: %v", err)
				}
				defer rows.Close()
				if !rows.Next() {
					if err := rows.Err(); err != nil {
						t.Fatal(err)
					}
					if home != "" {
						t.Fatalf("%s: missing userdb result, want %s", address, home)
					}
					return
				}
				var gotHome, mail, gotQuota string
				var uid, gid int
				if err := rows.Scan(&gotHome, &mail, &uid, &gid, &gotQuota); err != nil {
					t.Fatal(err)
				}
				wantMail := home + "/Maildir"
				if name != "2.4" {
					wantMail, quota = "maildir:"+wantMail, "*:storage="+quota
				}
				if home == "" || gotHome != home || mail != wantMail || uid != 5000 || gid != 5000 || gotQuota != quota {
					t.Fatalf("%s: home=%s mail=%s uid=%d gid=%d quota=%s, want %s %s", address, gotHome, mail, uid, gid, gotQuota, home, quota)
				}
				if rows.Next() {
					t.Fatal("userdb returned multiple users")
				}
			}
			exec := func(stmt string) {
				t.Helper()
				if _, err := db.Exec(stmt); err != nil {
					t.Fatal(err)
				}
			}
			lookup("Alice+detail@EXAMPLE.TEST", "/var/mail/vhosts/example.test/alice", "0M")
			lookup("missing@example.test", "", "")
			exec(`UPDATE system_settings SET value='true'`)
			lookup("Missing+detail@EXAMPLE.TEST", "/var/mail/vhosts/example.test/__unregistered__", "1024M")
			lookup("alice@example.test", "/var/mail/vhosts/example.test/alice", "0M")
			exec(`UPDATE mailboxes SET quota_mb=12`)
			lookup("alice@example.test", "/var/mail/vhosts/example.test/alice", "12M")
			lookup("missing@unknown.test", "", "")
			exec(`UPDATE domains SET status='disabled'`)
			lookup("missing@example.test", "", "")
			lookup("alice@example.test", "", "")
			exec(`UPDATE domains SET status='active'`)
			exec(`UPDATE mailboxes SET status='disabled'`)
			lookup("alice@example.test", "/var/mail/vhosts/example.test/__unregistered__", "1024M")
			exec(`UPDATE system_settings SET value='false'`)
			lookup("alice@example.test", "", "")
		})
	}
}
