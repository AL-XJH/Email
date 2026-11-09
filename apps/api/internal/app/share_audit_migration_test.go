package app

import (
	"context"
	"testing"
)

// 旧表把 actor_user_id 设成 ON DELETE CASCADE，删用户会把他在别人邮箱上的
// 访问审计一起删掉。迁移必须加 actor_email 并改成 SET NULL。
func TestMigrateMailboxShareAuditActorsPreservesRows(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	if _, err := a.db.ExecContext(ctx, `DROP TABLE mailbox_share_audit_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `CREATE TABLE mailbox_share_audit_events (
		id TEXT PRIMARY KEY,
		share_id TEXT NOT NULL REFERENCES mailbox_shares(id) ON DELETE CASCADE,
		mailbox_id TEXT NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
		actor_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		event TEXT NOT NULL,
		details_json TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if err := a.migrateMailboxShareAuditActors(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := a.db.QueryContext(ctx, `PRAGMA table_info(mailbox_share_audit_events)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "actor_email" {
			found = true
		}
	}
	if !found {
		t.Fatal("actor_email column missing after migration")
	}
	var onDelete string
	fkRows, err := a.db.QueryContext(ctx, `PRAGMA foreign_key_list(mailbox_share_audit_events)`)
	if err != nil {
		t.Fatal(err)
	}
	defer fkRows.Close()
	for fkRows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDeleteValue, match string
		if err := fkRows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDeleteValue, &match); err != nil {
			t.Fatal(err)
		}
		if from == "actor_user_id" {
			onDelete = onDeleteValue
		}
	}
	if onDelete != "SET NULL" {
		t.Fatalf("actor_user_id ON DELETE=%q, want SET NULL", onDelete)
	}
}
