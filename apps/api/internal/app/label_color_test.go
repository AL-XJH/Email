package app

import (
	"context"
	"strings"
	"testing"
)

func TestNewLabelGetsStableGeneratedColor(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	mailboxID := adminMailboxIDForTest(t, a)

	label, err := a.ensureLabel(ctx, mailboxID, "账单", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(label.Color, "hsl(") {
		t.Fatalf("new label color=%q, want generated hsl color", label.Color)
	}
	if label.Color != labelColorForName("账单") {
		t.Fatalf("color not deterministic: %q != %q", label.Color, labelColorForName("账单"))
	}

	// 旧版本把默认颜色写成灰色；启动迁移必须把它换回按名称生成的颜色。
	if _, err := a.db.ExecContext(ctx, `UPDATE mail_labels SET color='#64748b' WHERE id=?`, label.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.migrateLabelColors(ctx); err != nil {
		t.Fatal(err)
	}
	var migrated string
	if err := a.db.QueryRowContext(ctx, `SELECT color FROM mail_labels WHERE id=?`, label.ID).Scan(&migrated); err != nil {
		t.Fatal(err)
	}
	if migrated != labelColorForName("账单") {
		t.Fatalf("migrated color=%q, want %q", migrated, labelColorForName("账单"))
	}
}

func TestEnsureLabelIsIdempotent(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	mailboxID := adminMailboxIDForTest(t, a)

	first, err := a.ensureLabel(ctx, mailboxID, "  Work   Items  ", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.ensureLabel(ctx, mailboxID, "work items", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.Name != "Work Items" || second.Name != "Work Items" {
		t.Fatalf("duplicate label was not reused: first=%+v second=%+v", first, second)
	}
	var count int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM mail_labels WHERE mailbox_id=?`, mailboxID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("label count=%d, want 1", count)
	}
	// 已有的同名标签不应被重复创建时的自动颜色覆盖。
	if second.Color != first.Color {
		t.Fatalf("duplicate changed color: %q -> %q", first.Color, second.Color)
	}
}
