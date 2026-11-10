package app

import (
	"context"
	"strings"
	"testing"
)

func TestNewLabelGetsDistinctPaletteColor(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	mailboxID := adminMailboxIDForTest(t, a)

	first, err := a.ensureLabel(ctx, mailboxID, "账单", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.ensureLabel(ctx, mailboxID, "工作", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Color != labelColorForIndex(0) {
		t.Fatalf("first color=%q, want %q", first.Color, labelColorForIndex(0))
	}
	if second.Color != labelColorForIndex(1) {
		t.Fatalf("second color=%q, want %q", second.Color, labelColorForIndex(1))
	}
	if first.Color == second.Color {
		t.Fatalf("adjacent labels got the same color: %q", first.Color)
	}

	// 删掉中间标签后，新标签优先复用空出来的色相，而不是继续往后挤。
	if _, err := a.db.ExecContext(ctx, `DELETE FROM mail_labels WHERE id=?`, second.ID); err != nil {
		t.Fatal(err)
	}
	third, err := a.ensureLabel(ctx, mailboxID, "第三个", "")
	if err != nil {
		t.Fatal(err)
	}
	if third.Color != labelColorForIndex(1) {
		t.Fatalf("refilled color=%q, want %q", third.Color, labelColorForIndex(1))
	}
}

func TestMigrateLabelColorsDistributesOldLabels(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	mailboxID := adminMailboxIDForTest(t, a)

	first, err := a.ensureLabel(ctx, mailboxID, "账单", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.ensureLabel(ctx, mailboxID, "工作", "")
	if err != nil {
		t.Fatal(err)
	}
	// 模拟 .94 之前的库：全是灰色，且还没有新配色标记。
	if _, err := a.db.ExecContext(ctx, `UPDATE mail_labels SET color='#64748b',created_at='2026-01-01T00:00:01Z' WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `UPDATE mail_labels SET color='#64748b',created_at='2026-01-01T00:00:02Z' WHERE id=?`, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `DELETE FROM system_settings WHERE key='labelColorScheme'`); err != nil {
		t.Fatal(err)
	}
	if err := a.migrateLabelColors(ctx); err != nil {
		t.Fatal(err)
	}
	var firstColor, secondColor string
	if err := a.db.QueryRowContext(ctx, `SELECT color FROM mail_labels WHERE id=?`, first.ID).Scan(&firstColor); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRowContext(ctx, `SELECT color FROM mail_labels WHERE id=?`, second.ID).Scan(&secondColor); err != nil {
		t.Fatal(err)
	}
	if firstColor != labelColorForIndex(0) || secondColor != labelColorForIndex(1) {
		t.Fatalf("migrated colors=%q,%q want %q,%q", firstColor, secondColor, labelColorForIndex(0), labelColorForIndex(1))
	}
	var marker string
	if err := a.db.QueryRowContext(ctx, `SELECT value FROM system_settings WHERE key='labelColorScheme'`).Scan(&marker); err != nil || marker != "golden-angle-v1" {
		t.Fatalf("migration marker=%q err=%v", marker, err)
	}
	// 再跑一次不能把颜色重新洗一遍。
	if err := a.migrateLabelColors(ctx); err != nil {
		t.Fatal(err)
	}
	var again string
	if err := a.db.QueryRowContext(ctx, `SELECT color FROM mail_labels WHERE id=?`, first.ID).Scan(&again); err != nil || again != firstColor {
		t.Fatalf("second migration changed color: %q -> %q err=%v", firstColor, again, err)
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
	if second.Color != first.Color {
		t.Fatalf("duplicate changed color: %q -> %q", first.Color, second.Color)
	}
}

func TestLabelColorForIndexMovesAroundWheel(t *testing.T) {
	seen := map[string]bool{}
	for index := 0; index < 12; index++ {
		color := labelColorForIndex(index)
		if !strings.HasPrefix(color, "hsl(") {
			t.Fatalf("index %d color=%q", index, color)
		}
		if seen[color] {
			t.Fatalf("index %d repeated color %q", index, color)
		}
		seen[color] = true
	}
}
