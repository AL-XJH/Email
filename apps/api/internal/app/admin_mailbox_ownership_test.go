package app

import (
	"net/http"
	"testing"
)

// 邮箱换主人时，旧主人挂在这个邮箱上的外部 IMAP 账号（存着第三方邮箱凭据）
// 和共享授权都必须一起失效，否则等于把别人的邮箱交给新主人。
func TestAdminMailboxOwnershipChangeRevokesAttachments(t *testing.T) {
	a, admin, _, owner, domain := setupQuotaTest(t)
	userID := createHandoverUser(t, admin)
	adminID := testUserIDByEmail(t, a, "admin@lanqin.local")
	mailbox := createTestMailbox(t, admin, domain.ID, "handover", "Handover", "", map[string]any{
		"userId": owner.ID,
	})

	if _, err := a.db.Exec(`INSERT INTO external_imap_accounts(id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,created_at,updated_at)
		VALUES('ext_handover',?,?,'gmail','imap.gmail.com',993,'tls','someone@gmail.com','cipher','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		owner.ID, mailbox.ID); err != nil {
		t.Fatalf("insert external account: %v", err)
	}
	if _, err := a.db.Exec(`INSERT INTO mailbox_shares(id,mailbox_id,owner_user_id,shared_with_user_id,scope,created_at,updated_at)
		VALUES('shr_handover',?,?,?,'all','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		mailbox.ID, owner.ID, adminID); err != nil {
		t.Fatalf("insert share: %v", err)
	}

	if code := admin.do("POST", "/api/admin/mailboxes/"+mailbox.ID, map[string]any{
		"displayName": "Handover",
		"quotaMb":     1024,
		"status":      "active",
		"userId":      userID,
	}, nil); code != http.StatusOK {
		t.Fatalf("update mailbox code=%d", code)
	}

	var external int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM external_imap_accounts WHERE mailbox_id=?`, mailbox.ID).Scan(&external); err != nil {
		t.Fatalf("count external accounts: %v", err)
	}
	if external != 0 {
		t.Fatalf("external imap accounts still attached after ownership change: %d", external)
	}
	var revoked string
	if err := a.db.QueryRow(`SELECT COALESCE(revoked_at,'') FROM mailbox_shares WHERE id='shr_handover'`).Scan(&revoked); err != nil {
		t.Fatalf("load share: %v", err)
	}
	if revoked == "" {
		t.Fatal("share was not revoked after ownership change")
	}
}

// 没换主人时不应该动这些数据。
func TestAdminMailboxUpdateKeepsAttachmentsForSameOwner(t *testing.T) {
	a, admin, _, owner, domain := setupQuotaTest(t)
	mailbox := createTestMailbox(t, admin, domain.ID, "sameowner", "Same owner", "", map[string]any{
		"userId": owner.ID,
	})
	if _, err := a.db.Exec(`INSERT INTO external_imap_accounts(id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,created_at,updated_at)
		VALUES('ext_keep',?,?,'gmail','imap.gmail.com',993,'tls','someone@gmail.com','cipher','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		owner.ID, mailbox.ID); err != nil {
		t.Fatalf("insert external account: %v", err)
	}
	if code := admin.do("POST", "/api/admin/mailboxes/"+mailbox.ID, map[string]any{
		"displayName": "Renamed",
		"quotaMb":     2048,
		"status":      "active",
		"userId":      owner.ID,
	}, nil); code != http.StatusOK {
		t.Fatalf("update mailbox code=%d", code)
	}
	var external int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM external_imap_accounts WHERE mailbox_id=?`, mailbox.ID).Scan(&external); err != nil {
		t.Fatalf("count external accounts: %v", err)
	}
	if external != 1 {
		t.Fatalf("external imap account count=%d, want 1 when owner is unchanged", external)
	}
}

func createHandoverUser(t *testing.T, admin *testClient) string {
	t.Helper()
	var created AdminUser
	if code := admin.do("POST", "/api/admin/users", map[string]any{
		"email": "handover@example.net", "displayName": "Handover", "role": "user",
		"password": "Password123!", "disabled": false,
	}, &created); code != http.StatusCreated {
		t.Fatalf("create handover user code=%d", code)
	}
	return created.ID
}

func testUserIDByEmail(t *testing.T, a *App, email string) string {
	t.Helper()
	var id string
	if err := a.db.QueryRow(`SELECT id FROM users WHERE email=?`, email).Scan(&id); err != nil {
		t.Fatalf("load user %s: %v", email, err)
	}
	return id
}
