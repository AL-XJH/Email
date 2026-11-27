package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

// 热点查询基准：文件夹侧栏与邮件统计面板每次失效都会重跑，随邮件量增长是
// O(邮件数 × 每行子查询)。这里把「旧写法」和「新写法」放在同一份数据上对比，
// 数字才是可复核的。
//
// 跑法（只在显式 -bench 时执行，不拖慢普通 go test）：
//   go test ./internal/app -run '^$' -bench 'BenchmarkFolderCounts|BenchmarkMailStats' -benchtime 20x -count 1

const benchOldFolderCountsSQL = `SELECT f.id,f.name,f.role,
	COALESCE(SUM(CASE WHEN m.is_read=0 AND NOT EXISTS (SELECT 1 FROM external_imap_messages em WHERE em.local_message_id=m.id) THEN 1 ELSE 0 END),0) AS unread,
	COUNT(CASE WHEN NOT EXISTS (SELECT 1 FROM external_imap_messages em WHERE em.local_message_id=m.id) THEN m.id END) AS total,
	f.sort_order,f.uid_validity,f.uid_next,f.highest_modseq
	FROM folders f LEFT JOIN messages m ON m.folder_id=f.id
	WHERE f.mailbox_id=? AND NOT EXISTS (SELECT 1 FROM external_imap_folder_states efs WHERE efs.local_folder_id=f.id)
	GROUP BY f.id,f.name,f.role,f.sort_order,f.uid_validity,f.uid_next,f.highest_modseq
	ORDER BY CASE
		WHEN lower(f.name)='inbox' THEN 1000
		WHEN lower(f.name)='sent' THEN 5000
		WHEN lower(f.name)='drafts' THEN 6000
		WHEN lower(f.name)='archive' THEN 7000
		WHEN lower(f.name)='spam' THEN 8000
		WHEN lower(f.name)='trash' THEN 9000
		ELSE f.sort_order
	END, f.created_at,f.name`

// newTestApp 只接受 *testing.T，基准里需要同样的初始化但用 testing.TB。
func benchNewApp(tb testing.TB) *App {
	tb.Helper()
	dir := tb.TempDir()
	cfg := Config{
		Addr:              ":0",
		DBPath:            filepath.Join(dir, "lanqin.db"),
		DataDir:           filepath.Join(dir, "data"),
		CookieName:        "lanqin_test",
		SessionTTLHours:   24,
		AdminEmail:        "admin@lanqin.local",
		AdminPassword:     "ChangeMe123!",
		PublicHostname:    "mail.example.test",
		PublicBaseURL:     "http://localhost:5173",
		AllowInsecureHTTP: true,
		AuthPolicySecret:  "test-auth-policy-secret",
	}
	a, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = a.Close() })
	return a
}

func benchSeed(tb testing.TB, n int) (*App, context.Context, string) {
	tb.Helper()
	a := benchNewApp(tb)
	ctx := context.Background()
	var mailboxID string
	if err := a.db.QueryRow(`SELECT id FROM mailboxes ORDER BY created_at LIMIT 1`).Scan(&mailboxID); err != nil {
		tb.Fatal(err)
	}

	names := []string{"Inbox", "Sent", "Archive", "Spam", "Trash", "Projects", "Receipts"}
	folderIDs := make([]string, 0, len(names))
	for _, name := range names {
		id, err := a.ensureFolder(ctx, mailboxID, name)
		if err != nil {
			tb.Fatal(err)
		}
		folderIDs = append(folderIDs, id)
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		tb.Fatal(err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO messages
		(id,mailbox_id,folder_id,message_uid,message_id,thread_id,subject,from_addr,to_addrs,sent_at,received_at,snippet,body_text,body_html,is_read,is_starred,has_attachments,size_bytes,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tb.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		stamp := base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
		read := 0
		if i%3 == 0 {
			read = 1
		}
		thread := fmt.Sprintf("thr-%d", i/4)
		if _, err := stmt.ExecContext(ctx,
			fmt.Sprintf("bench-msg-%d", i), mailboxID, folderIDs[i%len(folderIDs)],
			fmt.Sprintf("uid-%d", i), fmt.Sprintf("<bench-%d@example.test>", i), thread,
			fmt.Sprintf("subject %d", i), "sender@example.test", `["admin@lanqin.local"]`,
			stamp, stamp, "snippet text", "body text", "<p>body</p>", read, i%11 == 0, 0, 2048, stamp, stamp,
		); err != nil {
			tb.Fatal(err)
		}
	}
	if err := stmt.Close(); err != nil {
		tb.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `ANALYZE`); err != nil {
		tb.Fatal(err)
	}
	return a, ctx, mailboxID
}

func drainFolders(tb testing.TB, a *App, ctx context.Context, query, mailboxID string) {
	tb.Helper()
	rows, err := a.db.QueryContext(ctx, query, mailboxID)
	if err != nil {
		tb.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, name, role                             string
			unread, total, sortOrder, uidValidity, uidNext, modSeq int
		)
		if err := rows.Scan(&id, &name, &role, &unread, &total, &sortOrder, &uidValidity, &uidNext, &modSeq); err != nil {
			tb.Fatal(err)
		}
	}
}

func BenchmarkFolderCounts(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		a, ctx, mailboxID := benchSeed(b, n)
		b.Run(fmt.Sprintf("old/%dmessages", n/1000), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				drainFolders(b, a, ctx, benchOldFolderCountsSQL, mailboxID)
			}
		})
		b.Run(fmt.Sprintf("new/%dmessages", n/1000), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := a.loadMailFolderCounts(ctx, mailboxID, "", false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

const benchOldStatsSQL = `SELECT COALESCE(SUM(CASE WHEN NOT EXISTS (SELECT 1 FROM external_imap_messages em WHERE em.local_message_id=m.id) THEN 1 ELSE 0 END),0),
	COALESCE(SUM(CASE WHEN m.is_read=0 AND NOT EXISTS (SELECT 1 FROM external_imap_messages em WHERE em.local_message_id=m.id) THEN 1 ELSE 0 END),0),
	COALESCE(SUM(CASE WHEN m.is_starred=1 AND NOT EXISTS (SELECT 1 FROM external_imap_messages em WHERE em.local_message_id=m.id) THEN 1 ELSE 0 END),0),
	COALESCE(SUM(CASE WHEN NOT EXISTS (SELECT 1 FROM external_imap_messages em WHERE em.local_message_id=m.id) THEN m.size_bytes ELSE 0 END),0)
	FROM mailboxes mb LEFT JOIN messages m ON m.mailbox_id=mb.id WHERE mb.id=?`

func BenchmarkMailStats(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		a, ctx, mailboxID := benchSeed(b, n)
		b.Run(fmt.Sprintf("old/%dmessages", n/1000), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var total, unread, starred, bytes int64
				if err := a.db.QueryRowContext(ctx, benchOldStatsSQL, mailboxID).Scan(&total, &unread, &starred, &bytes); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("new/%dmessages", n/1000), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := a.loadMailboxTotals(ctx, mailboxID); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// 列表页每行都会读一段 body_text（用于服务端提取验证码）。这里量一下这段
// 截取本身的成本：8 KB × 30 行 vs 只读 64 字节。
const benchListPageSQL = `SELECT m.id,m.subject,m.snippet,substr(COALESCE(m.body_text,''),1,%s)
	FROM messages m WHERE m.mailbox_id=?
	ORDER BY m.received_at DESC,m.id DESC LIMIT 30`

func benchListPage(b *testing.B, limit string) {
	a, ctx, mailboxID := benchSeed(b, 10000)
	q := fmt.Sprintf(benchListPageSQL, limit)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rows, err := a.db.QueryContext(ctx, q, mailboxID)
		if err != nil {
			b.Fatal(err)
		}
		for rows.Next() {
			var id, subject, snippet, body string
			if err := rows.Scan(&id, &subject, &snippet, &body); err != nil {
				b.Fatal(err)
			}
		}
		rows.Close()
	}
}

func BenchmarkMessageListPageBody8k(b *testing.B) { benchListPage(b, "8000") }
func BenchmarkMessageListPageBody64(b *testing.B) { benchListPage(b, "64") }

// 翻页时每次都会重跑 COUNT(*)（前端用 total 显示「第 x–y / N」）。量一下这条
// 计数在 1 万封时的成本，决定是否值得让第 2 页以后跳过它。
func BenchmarkMessageListCount(b *testing.B) {
	a, ctx, mailboxID := benchSeed(b, 10000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var n int
		if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages m WHERE m.mailbox_id=?`, mailboxID).Scan(&n); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMessageListCountVariants(b *testing.B) {
	a, ctx, mailboxID := benchSeed(b, 10000)
	variants := map[string]string{
		"plain":      `SELECT COUNT(*) FROM messages m WHERE m.mailbox_id=?`,
		"index_hint": `SELECT COUNT(*) FROM messages m INDEXED BY idx_messages_mailbox_received_id WHERE m.mailbox_id=?`,
		"subquery":   `SELECT COUNT(*) FROM (SELECT 1 FROM messages INDEXED BY idx_messages_mailbox_received_id WHERE mailbox_id=?)`,
	}
	names := []string{"plain", "index_hint", "subquery"}
	for _, name := range names {
		q := variants[name]
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var n int
				if err := a.db.QueryRowContext(ctx, q, mailboxID).Scan(&n); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
