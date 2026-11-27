package app

import (
	"context"
	"database/sql"
)

// 侧栏与统计面板的热点聚合。
//
// 原写法在每一行邮件上跑两次相关子查询（`NOT EXISTS (SELECT 1 FROM
// external_imap_messages …)`），成本是 O(邮件数) 次索引查找。这里改成先对
// external_imap_messages 的 local_message_id 去重物化一次、再 LEFT JOIN：
// 排除外部账号导入件只做一次扫描，聚合本身走 `idx_messages_mailbox_folder_received_id`
// / `idx_messages_folder_read`。
//
// 去重（DISTINCT）是必须的：local_message_id 上只是普通索引，同一封本地邮件
// 理论上可能被多条外部映射引用，直接 JOIN 会把计数放大。

const externalLocalMessageJoin = ` LEFT JOIN (SELECT DISTINCT local_message_id FROM external_imap_messages WHERE local_message_id<>'') ex ON ex.local_message_id=m.id`

func (a *App) loadMailFolderCounts(ctx context.Context, mailboxID, shareID string, customScope bool) ([]MailFolder, error) {
	where := `f.mailbox_id=?`
	args := []any{mailboxID, mailboxID, mailboxID}
	where += ` AND NOT EXISTS (SELECT 1 FROM external_imap_folder_states efs WHERE efs.local_folder_id=f.id)`
	if customScope {
		where += ` AND EXISTS (SELECT 1 FROM mailbox_share_folders msf WHERE msf.share_id=? AND msf.folder_id=f.id)`
		args = append(args, shareID)
	}
	rows, err := a.db.QueryContext(ctx, `SELECT f.id,f.name,f.role,
		COALESCE(mc.unread,0) - COALESCE(ec.unread,0) AS unread,
		COALESCE(mc.total,0) - COALESCE(ec.total,0) AS total,
		f.sort_order,f.uid_validity,f.uid_next,f.highest_modseq
		FROM folders f
		LEFT JOIN (SELECT folder_id,
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN is_read=0 THEN 1 ELSE 0 END),0) AS unread
			FROM messages WHERE mailbox_id=? GROUP BY folder_id) mc ON mc.folder_id=f.id
		LEFT JOIN (SELECT m.folder_id, COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN m.is_read=0 THEN 1 ELSE 0 END),0) AS unread
			FROM external_imap_messages em JOIN messages m ON m.id=em.local_message_id
			WHERE m.mailbox_id=? GROUP BY m.folder_id) ec ON ec.folder_id=f.id
		WHERE `+where+` GROUP BY f.id,f.name,f.role,f.sort_order,f.uid_validity,f.uid_next,f.highest_modseq
		ORDER BY CASE
			WHEN lower(f.name)='inbox' THEN 1000
			WHEN lower(f.name)='sent' THEN 5000
			WHEN lower(f.name)='drafts' THEN 6000
			WHEN lower(f.name)='archive' THEN 7000
			WHEN lower(f.name)='spam' THEN 8000
			WHEN lower(f.name)='trash' THEN 9000
			ELSE f.sort_order
		END, f.created_at,f.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []MailFolder{}
	for rows.Next() {
		var f MailFolder
		if err := rows.Scan(&f.ID, &f.Name, &f.Role, &f.UnreadCount, &f.TotalCount, &f.SortOrder, &f.UIDValidity, &f.UIDNext, &f.HighestModSeq); err != nil {
			return nil, err
		}
		items = append(items, f)
	}
	return items, rows.Err()
}

type mailboxTotals struct {
	TotalMessages   int64
	UnreadMessages  int64
	StarredMessages int64
	StorageBytes    int64
}

func (a *App) loadMailboxTotals(ctx context.Context, mailboxID string) (mailboxTotals, error) {
	var t mailboxTotals
	err := a.db.QueryRowContext(ctx, `SELECT COUNT(m.id),
		COALESCE(SUM(CASE WHEN m.is_read=0 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN m.is_starred=1 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(m.size_bytes),0)
		FROM messages m`+externalLocalMessageJoin+`
		WHERE m.mailbox_id=? AND ex.local_message_id IS NULL`, mailboxID).
		Scan(&t.TotalMessages, &t.UnreadMessages, &t.StarredMessages, &t.StorageBytes)
	return t, err
}

// 附件统计（条数/字节）同样排除外部账号导入件。
func (a *App) loadMailboxAttachmentTotals(ctx context.Context, mailboxID string) (count, bytes int64, err error) {
	err = a.db.QueryRowContext(ctx, `SELECT COUNT(a.id),COALESCE(SUM(a.size_bytes),0)
		FROM attachments a JOIN messages m ON m.id=a.message_id`+externalLocalMessageJoin+`
		WHERE m.mailbox_id=? AND ex.local_message_id IS NULL`, mailboxID).Scan(&count, &bytes)
	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	return count, bytes, err
}

// 统计面板的按文件夹明细（单邮箱视图）。同样是「两次分组聚合相减」，避免
// 在每一封邮件上跑 `NOT EXISTS (external_imap_messages)`。
func (a *App) loadFolderBreakdown(ctx context.Context, mailboxID string) ([]MailStatsFolderCount, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT f.name,f.role,
		COALESCE(mc.total,0)-COALESCE(ec.total,0) AS total,
		COALESCE(mc.unread,0)-COALESCE(ec.unread,0) AS unread,
		COALESCE(mc.bytes,0)-COALESCE(ec.bytes,0) AS bytes
		FROM folders f
		LEFT JOIN (SELECT folder_id,COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN is_read=0 THEN 1 ELSE 0 END),0) AS unread,
			COALESCE(SUM(size_bytes),0) AS bytes
			FROM messages WHERE mailbox_id=? GROUP BY folder_id) mc ON mc.folder_id=f.id
		LEFT JOIN (SELECT m.folder_id,COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN m.is_read=0 THEN 1 ELSE 0 END),0) AS unread,
			COALESCE(SUM(m.size_bytes),0) AS bytes
			FROM external_imap_messages em JOIN messages m ON m.id=em.local_message_id
			WHERE m.mailbox_id=? GROUP BY m.folder_id) ec ON ec.folder_id=f.id
		WHERE f.mailbox_id=? AND NOT EXISTS (SELECT 1 FROM external_imap_folder_states efs WHERE efs.local_folder_id=f.id)
		GROUP BY f.id,f.name,f.role
		ORDER BY CASE f.role WHEN 'inbox' THEN 1 WHEN 'sent' THEN 2 WHEN 'drafts' THEN 3 WHEN 'archive' THEN 4 WHEN 'spam' THEN 5 WHEN 'trash' THEN 6 ELSE 99 END`,
		mailboxID, mailboxID, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []MailStatsFolderCount{}
	for rows.Next() {
		var item MailStatsFolderCount
		if err := rows.Scan(&item.Folder, &item.Role, &item.Count, &item.Unread, &item.Bytes); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
