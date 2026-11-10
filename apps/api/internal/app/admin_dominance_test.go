package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 客服用 admin.users.reset_password 不能重置 role=admin 的超级管理员。
func TestDelegatedAdminCannotResetSuperAdminPassword(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	admin := &testClient{t: t, server: ts}
	var login map[string]any
	if code := admin.do("POST", "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, &login); code != http.StatusOK {
		t.Fatalf("admin login=%d", code)
	}
	var group struct {
		ID string `json:"id"`
	}
	if code := admin.do("POST", "/api/admin/permission-groups", map[string]any{
		"name": "Reset Desk", "permissions": []string{PermissionUsersView, PermissionUsersResetPassword},
	}, &group); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("create group=%d", code)
	}
	var desk struct {
		ID string `json:"id"`
	}
	if code := admin.do("POST", "/api/admin/users", map[string]any{
		"email": "desk@lanqin.local", "displayName": "Desk", "password": "Password123!", "role": "user", "permissionGroupIds": []string{group.ID},
	}, &desk); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create desk=%d", code)
	}
	var target struct {
		ID string `json:"id"`
	}
	if code := admin.do("POST", "/api/admin/users", map[string]any{
		"email": "target-admin@lanqin.local", "displayName": "Target Admin", "password": "Password123!", "role": "admin",
	}, &target); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create target admin=%d", code)
	}
	deskClient := &testClient{t: t, server: ts}
	if code := deskClient.do("POST", "/api/auth/login", map[string]string{"email": "desk@lanqin.local", "password": "Password123!"}, &login); code != http.StatusOK {
		t.Fatalf("desk login=%d", code)
	}
	var body map[string]any
	if code := deskClient.do("POST", "/api/admin/users/"+target.ID+"/password", map[string]string{"password": "Hijack123!"}, &body); code != http.StatusForbidden {
		t.Fatalf("delegated reset of super admin=%d body=%v", code, body)
	}
}

func TestOpenAPIDelegatedAdminCannotDeleteSuperAdminMailbox(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	admin := &testClient{t: t, server: ts}
	var login map[string]any
	if code := admin.do("POST", "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, &login); code != http.StatusOK {
		t.Fatalf("admin login=%d", code)
	}
	var group struct {
		ID string `json:"id"`
	}
	if code := admin.do("POST", "/api/admin/permission-groups", map[string]any{
		"name": "Mailbox Delete Desk", "permissions": []string{PermissionMailboxesView, PermissionMailboxesDelete},
	}, &group); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("create group=%d", code)
	}
	var desk struct {
		ID string `json:"id"`
	}
	if code := admin.do("POST", "/api/admin/users", map[string]any{
		"email": "delete-desk@lanqin.local", "displayName": "Delete Desk", "password": "Password123!", "role": "user", "permissionGroupIds": []string{group.ID},
	}, &desk); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create desk=%d", code)
	}
	target, err := a.mailboxByAddress(context.Background(), "admin@lanqin.local")
	if err != nil {
		t.Fatalf("load admin mailbox: %v", err)
	}
	deskClient := &testClient{t: t, server: ts}
	if code := deskClient.do("POST", "/api/auth/login", map[string]string{"email": "delete-desk@lanqin.local", "password": "Password123!"}, &login); code != http.StatusOK {
		t.Fatalf("desk login=%d", code)
	}
	token := createTestAPITokenWithScopes(t, deskClient, "delete-desk", []string{"mailboxes:write"})
	apiClient := &testClient{t: t, server: ts, bearer: token}
	var body map[string]any
	if code := apiClient.do("DELETE", "/api/open/mailboxes/"+target.ID, nil, &body); code != http.StatusForbidden {
		t.Fatalf("delegated delete of super admin mailbox=%d body=%v", code, body)
	}
	if _, err := a.mailboxByID(context.Background(), target.ID); err != nil {
		t.Fatalf("admin mailbox was deleted: %v", err)
	}
}

func TestOpenAPIMailboxPasswordResetRequiresDedicatedScope(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	admin := &testClient{t: t, server: ts}
	var login map[string]any
	if code := admin.do("POST", "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, &login); code != http.StatusOK {
		t.Fatalf("admin login=%d", code)
	}
	var group struct {
		ID string `json:"id"`
	}
	if code := admin.do("POST", "/api/admin/permission-groups", map[string]any{
		"name": "Password Desk", "permissions": []string{PermissionMailboxesView, PermissionUsersResetPassword},
	}, &group); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("create group=%d", code)
	}
	var desk struct {
		ID string `json:"id"`
	}
	if code := admin.do("POST", "/api/admin/users", map[string]any{
		"email": "password-desk@lanqin.local", "displayName": "Password Desk", "password": "Password123!", "role": "user", "permissionGroupIds": []string{group.ID},
	}, &desk); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create desk=%d", code)
	}
	var targetUser struct {
		ID string `json:"id"`
	}
	if code := admin.do("POST", "/api/admin/users", map[string]any{
		"email": "reset-target@lanqin.local", "displayName": "Reset Target", "password": "Password123!", "role": "user",
	}, &targetUser); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create target=%d", code)
	}
	var domainID string
	if err := a.db.QueryRow(`SELECT id FROM domains ORDER BY created_at LIMIT 1`).Scan(&domainID); err != nil {
		t.Fatalf("load domain: %v", err)
	}
	target := createTestMailbox(t, admin, domainID, "reset-target", "Reset Target", "Password123!", map[string]any{"userId": targetUser.ID})
	deskClient := &testClient{t: t, server: ts}
	if code := deskClient.do("POST", "/api/auth/login", map[string]string{"email": "password-desk@lanqin.local", "password": "Password123!"}, &login); code != http.StatusOK {
		t.Fatalf("desk login=%d", code)
	}
	mailboxToken := createTestAPITokenWithScopes(t, deskClient, "mailbox-write", []string{"mailboxes:write"})
	apiClient := &testClient{t: t, server: ts, bearer: mailboxToken}
	var body map[string]any
	if code := apiClient.do("POST", "/api/open/mailboxes/"+target.ID+"/password", map[string]string{"password": "Hijack123!"}, &body); code != http.StatusForbidden {
		t.Fatalf("mailboxes:write reset password=%d body=%v", code, body)
	}
	resetToken := createTestAPITokenWithScopes(t, deskClient, "password-reset", []string{"users:reset-password"})
	resetClient := &testClient{t: t, server: ts, bearer: resetToken}
	if code := resetClient.do("POST", "/api/open/mailboxes/"+target.ID+"/password", map[string]string{"password": "Reset123!"}, &body); code != http.StatusOK {
		t.Fatalf("users:reset-password reset=%d body=%v", code, body)
	}
	loginClient := &testClient{t: t, server: ts}
	if code := loginClient.do("POST", "/api/auth/login", map[string]string{"email": "reset-target@lanqin.local", "password": "Reset123!"}, &login); code != http.StatusOK {
		t.Fatalf("target login with new password=%d", code)
	}
}
