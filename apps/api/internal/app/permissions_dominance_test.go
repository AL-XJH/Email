package app

import "testing"

// 客服只有重置口令权限、目标账号能读全站邮件时，前者不能接管后者：
// 否则「重置口令 → 登录目标账号」就是一条完整的越权读信链。
func TestActorCannotGrantPermissionsItDoesNotHold(t *testing.T) {
	helpdesk := &User{
		Role:        "user",
		Permissions: []string{PermissionUsersResetPassword, PermissionUsersView},
	}
	if actorCanGrantPermissions(helpdesk, []string{PermissionMessagesRead}) {
		t.Fatal("helpdesk must not administer an account holding admin.messages.read")
	}
	if !actorCanGrantPermissions(helpdesk, []string{PermissionUsersResetPassword}) {
		t.Fatal("actor must be able to administer an account with a subset of its permissions")
	}
	if actorCanGrantPermissions(helpdesk, []string{PermissionUsersView, PermissionMessagesRead}) {
		t.Fatal("one missing permission is enough to block the whole action")
	}
	if !actorCanGrantPermissions(&User{Role: "admin"}, []string{PermissionMessagesRead}) {
		t.Fatal("super admin bypasses the dominance check")
	}
	if actorCanGrantPermissions(nil, []string{PermissionUsersView}) {
		t.Fatal("nil actor must not administer anything")
	}
}
