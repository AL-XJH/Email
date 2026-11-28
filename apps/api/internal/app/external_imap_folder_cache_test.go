package app

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// 文件夹下拉不该每展开一次就新建一条 IMAP 连接：本地存储模式的账号在同步时
// 已经 LIST 过，接口应优先吃快照。这里把账号指向一个必然连不上的地址，再塞一份
// 快照；只要接口还能返回文件夹，就证明它没有去建连接。
func TestExternalIMAPFoldersServedFromSnapshotWithoutDialing(t *testing.T) {
	dir := newTestDir(t)
	a := newTestAppWithConfig(t, Config{
		Addr:                          ":0",
		DBPath:                        filepath.Join(dir, "lanqin.db"),
		DataDir:                       filepath.Join(dir, "data"),
		CookieName:                    "lanqin_test",
		SessionTTLHours:               24,
		AdminEmail:                    "admin@lanqin.local",
		AdminPassword:                 "ChangeMe123!",
		PublicHostname:                "mail.example.test",
		PublicBaseURL:                 "http://localhost:5173",
		AllowInsecureHTTP:             true,
		ExternalIMAPEnabled:           true,
		ExternalIMAPSecretKey:         "test-secret",
		ExternalIMAPAllowPrivateHosts: true,
	})
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	admin := &testClient{t: t, server: ts}
	if code := admin.do("POST", "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, nil); code != http.StatusOK {
		t.Fatalf("login code=%d", code)
	}
	user, mailbox := defaultAdminUserAndMailbox(t, a)
	now := a.now().UTC().Format(time.RFC3339Nano)
	accountID := "ximap_folder_cache_test"
	if _, err := a.db.Exec(`INSERT INTO external_imap_accounts(id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,storage_mode,sync_read_state,enabled,last_status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, accountID, user.ID, mailbox.ID, "Cached folders", "127.0.0.1", 1, externalIMAPTLS, "remote@example.test", "ciphertext", externalIMAPStorageLocal, 0, 1, "idle", now, now); err != nil {
		t.Fatal(err)
	}
	inboxID, err := a.ensureFolder(t.Context(), mailbox.ID, "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO external_imap_folder_states(account_id,remote_folder,local_folder_id,uid_validity,last_uid,last_sync_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?)`, accountID, "INBOX", inboxID, 3, 0, now, now, now); err != nil {
		t.Fatal(err)
	}
	a.saveExternalIMAPFolderSnapshot(t.Context(), accountID, []externalIMAPRemoteFolder{{Name: "INBOX", Role: "inbox"}, {Name: "Sent", Role: "sent"}})

	var listed struct {
		Items []ExternalIMAPFolder `json:"items"`
	}
	if code := admin.do("GET", "/api/mail/external-accounts/"+accountID+"/folders", nil, &listed); code != http.StatusOK {
		t.Fatalf("list code=%d", code)
	}
	if len(listed.Items) != 2 {
		t.Fatalf("items=%+v", listed.Items)
	}
	if listed.Items[0].Name != "INBOX" || listed.Items[0].DisplayName != "收件箱" {
		t.Fatalf("first item=%+v", listed.Items[0])
	}
}
