package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// GET /mail/messages 必须无副作用：以前 ?folder=NeverExisted 会顺手建一个
// 文件夹，既绕过创建校验/配额，也让只读请求改了数据。
func TestListMessagesUnknownFolderDoesNotCreateIt(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Router())
	defer ts.Close()

	admin := &testClient{t: t, server: ts}
	var login map[string]any
	if code := admin.do("POST", "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, &login); code != http.StatusOK {
		t.Fatalf("admin login=%d", code)
	}
	var domains struct {
		Items []Domain `json:"items"`
	}
	if code := admin.do("GET", "/api/admin/domains", nil, &domains); code != http.StatusOK || len(domains.Items) == 0 {
		t.Fatalf("domains=%d", code)
	}
	mb := createTestMailbox(t, admin, domains.Items[0].ID, "folder-read", "Folder Read", "Password123!", nil)

	owner := &testClient{t: t, server: ts}
	if code := owner.do("POST", "/api/auth/login", map[string]string{"email": mb.Address, "password": "Password123!"}, &login); code != http.StatusOK {
		t.Fatalf("owner login=%d", code)
	}

	var before int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM folders WHERE mailbox_id=?`, mb.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []MailMessage `json:"items"`
	}
	if code := owner.do("GET", "/api/mail/messages?mailboxId="+mb.ID+"&folder=GhostFolder", nil, &list); code != http.StatusOK || len(list.Items) != 0 {
		t.Fatalf("ghost folder code=%d items=%d", code, len(list.Items))
	}
	var after, created int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM folders WHERE mailbox_id=?`, mb.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM folders WHERE mailbox_id=? AND lower(name)='ghostfolder'`, mb.ID).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if before != after || created != 0 {
		t.Fatalf("GET created a folder: before=%d after=%d ghost=%d", before, after, created)
	}
}
