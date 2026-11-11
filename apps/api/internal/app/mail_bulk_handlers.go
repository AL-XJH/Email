package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	bulkMessageMaxIDs = 200
)

// 单封与批量操作共用同一套动作实现，避免两处逻辑走偏。
func (a *App) applyMessageRead(ctx context.Context, msg *MailMessage, read bool) error {
	if err := a.updateMessageMaildirFlags(ctx, msg.ID, &read, nil); err != nil {
		return err
	}
	modSeq, err := a.updateMessageModSeq(ctx, msg.ID, msg.FolderID)
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `UPDATE messages SET is_read=?, imap_modseq=CASE WHEN ? > 0 THEN ? ELSE imap_modseq END, updated_at=? WHERE id=?`, boolInt(read), modSeq, modSeq, a.now().UTC().Format(time.RFC3339Nano), msg.ID)
	return err
}

func (a *App) applyMessageStar(ctx context.Context, msg *MailMessage, starred bool) error {
	if err := a.updateMessageMaildirFlags(ctx, msg.ID, nil, &starred); err != nil {
		return err
	}
	modSeq, err := a.updateMessageModSeq(ctx, msg.ID, msg.FolderID)
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `UPDATE messages SET is_starred=?, imap_modseq=CASE WHEN ? > 0 THEN ? ELSE imap_modseq END, updated_at=? WHERE id=?`, boolInt(starred), modSeq, modSeq, a.now().UTC().Format(time.RFC3339Nano), msg.ID)
	return err
}

func (a *App) applyMessageMove(ctx context.Context, msg *MailMessage, folder string) error {
	name, err := normalizeFolderNameForUser(folder)
	if err != nil {
		return err
	}
	folderID, err := a.ensureFolder(ctx, msg.MailboxID, name)
	if err != nil {
		return err
	}
	return a.moveMessageMaildir(ctx, msg.ID, folderID)
}

// 已经在 Trash 里的再删就是彻底删除，否则先移进 Trash —— 与单封删除一致。
func (a *App) applyMessageDelete(ctx context.Context, msg *MailMessage) error {
	if strings.EqualFold(msg.Folder, "Trash") {
		// 文件删不掉就不能删库里的行：否则下一轮 maildir 同步会把文件
		// 当成新邮件重新导入，「清空垃圾箱」5 秒后自己撤销。
		if err := a.deleteMessageMaildirFile(ctx, msg.ID); err != nil {
			return fmt.Errorf("failed to remove maildir file: %w", err)
		}
		a.deleteMessageFiles(ctx, msg.ID)
		if _, err := a.db.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, msg.ID); err != nil {
			return err
		}
		_, _ = a.bumpFolderModSeq(ctx, msg.FolderID)
		return nil
	}
	trashID, err := a.ensureFolder(ctx, msg.MailboxID, "Trash")
	if err != nil {
		return err
	}
	return a.moveMessageMaildir(ctx, msg.ID, trashID)
}

func (a *App) handleBulkMessageAction(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req struct {
		IDs    []string `json:"ids"`
		Action string   `json:"action"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	var folder string
	switch action {
	case "read", "unread", "star", "unstar", "delete":
	case "archive":
		folder = "Archive"
	case "trash":
		folder = "Trash"
	case "spam":
		folder = "Spam"
	default:
		badRequest(w, errors.New("invalid bulk action"))
		return
	}
	normalizedFolder := ""
	if folder != "" {
		var err error
		normalizedFolder, err = normalizeFolderNameForUser(folder)
		if err != nil {
			badRequest(w, err)
			return
		}
	}

	seen := make(map[string]bool, len(req.IDs))
	ids := make([]string, 0, len(req.IDs))
	for _, id := range req.IDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
		if len(ids) >= bulkMessageMaxIDs {
			break
		}
	}
	if len(ids) == 0 {
		badRequest(w, errors.New("ids is required"))
		return
	}

	// 只处理属于当前用户的邮件；别人的 id 直接当不存在，不回显存在性。
	args := make([]any, 0, len(ids)+1)
	args = append(args, user.ID)
	marks := make([]string, 0, len(ids))
	for _, id := range ids {
		marks = append(marks, "?")
		args = append(args, id)
	}
	query := `SELECT m.id,m.mailbox_id,COALESCE(m.folder_id,''),COALESCE(f.name,''),m.is_read,m.is_starred FROM messages m JOIN mailboxes mb ON mb.id=m.mailbox_id LEFT JOIN folders f ON f.id=m.folder_id WHERE mb.user_id=? AND m.id IN (` + strings.Join(marks, ",") + `)`
	rows, err := a.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load messages")
		return
	}
	owned := map[string]*MailMessage{}
	for rows.Next() {
		var msg MailMessage
		var read, starred int
		if err := rows.Scan(&msg.ID, &msg.MailboxID, &msg.FolderID, &msg.Folder, &read, &starred); err != nil {
			rows.Close()
			respondError(w, http.StatusInternalServerError, "failed to load messages")
			return
		}
		msg.IsRead, msg.IsStarred = intBool(read), intBool(starred)
		owned[msg.ID] = &msg
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load messages")
		return
	}

	updated := 0
	missing := make([]string, 0)
	failed := make([]string, 0)
	folderIDs := map[string]string{}
	for _, id := range ids {
		msg := owned[id]
		if msg == nil {
			missing = append(missing, id)
			continue
		}
		switch action {
		case "read":
			err = a.applyMessageRead(r.Context(), msg, true)
		case "unread":
			err = a.applyMessageRead(r.Context(), msg, false)
		case "star":
			err = a.applyMessageStar(r.Context(), msg, true)
		case "unstar":
			err = a.applyMessageStar(r.Context(), msg, false)
		case "delete":
			err = a.applyMessageDelete(r.Context(), msg)
		default:
			folderID, ok := folderIDs[msg.MailboxID]
			if !ok {
				folderID, err = a.ensureFolder(r.Context(), msg.MailboxID, normalizedFolder)
				if err == nil {
					folderIDs[msg.MailboxID] = folderID
				}
			}
			if err == nil {
				err = a.moveMessageMaildir(r.Context(), msg.ID, folderID)
			}
		}
		if err != nil {
			failed = append(failed, id)
			continue
		}
		updated++
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"ok":      len(failed) == 0,
		"updated": updated,
		"missing": missing,
		"failed":  failed,
	})
}

// readAllFolderBatch 是单次请求最多处理的邮件数。逐封重命名 maildir 文件是
// 磁盘操作，一次几千封会让请求挂住；客户端按返回的 remaining 接着调。
const readAllFolderBatch = 500

func (a *App) handleReadAllFolder(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req struct {
		MailboxID string `json:"mailboxId"`
		Folder    string `json:"folder"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	name, err := normalizeFolderNameForUser(req.Folder)
	if err != nil {
		badRequest(w, err)
		return
	}
	mb, err := a.mailboxByID(r.Context(), strings.TrimSpace(req.MailboxID))
	if err != nil || mb.UserID != user.ID || mb.Status != "active" {
		respondError(w, http.StatusNotFound, "mailbox not found")
		return
	}
	var folderID string
	if err := a.db.QueryRowContext(r.Context(), `SELECT id FROM folders WHERE mailbox_id=? AND lower(name)=lower(?)`, mb.ID, name).Scan(&folderID); err != nil {
		respondError(w, http.StatusNotFound, "folder not found")
		return
	}
	updated, err := a.markFolderRead(r.Context(), mb.ID, folderID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to mark folder read")
		return
	}
	var remaining int
	if err := a.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM messages WHERE mailbox_id=? AND folder_id=? AND is_read=0`, mb.ID, folderID).Scan(&remaining); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to count remaining messages")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"updated": updated, "remaining": remaining})
}

// markFolderRead 把整个文件夹标为已读，并且同步改 maildir 文件名的 S 标志。
// 只改库不改文件名的话，下一轮 maildir 同步会按 `:2,` 里缺 S 把它们判回未读，
// 「全部已读」等于没点。
func (a *App) markFolderRead(ctx context.Context, mailboxID, folderID string) (int, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id,COALESCE(raw_path,''),is_starred FROM messages
		WHERE mailbox_id=? AND folder_id=? AND is_read=0 LIMIT ?`, mailboxID, folderID, readAllFolderBatch)
	if err != nil {
		return 0, err
	}
	type unreadMessage struct {
		id, rawPath string
		starred     bool
	}
	items := []unreadMessage{}
	for rows.Next() {
		var item unreadMessage
		var starred int
		if err := rows.Scan(&item.id, &item.rawPath, &starred); err != nil {
			rows.Close()
			return 0, err
		}
		item.starred = starred != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if len(items) == 0 {
		return 0, nil
	}
	modSeq, err := a.bumpFolderModSeq(ctx, folderID)
	if err != nil {
		return 0, err
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	updated := 0
	for _, item := range items {
		newPath := item.rawPath
		if item.rawPath != "" {
			ok, pathErr := a.pathIsUnderMaildirRoot(item.rawPath)
			if pathErr != nil {
				// 路径都算不出来时不能碰数据库：否则库已读、文件仍未读，
				// 下一轮同步还会按文件名翻回去。
				a.log.Warn("failed to validate maildir path for read-all", "message", item.id, "error", pathErr)
				continue
			}
			if !ok {
				// raw_path 过期、配置换过 MaildirRoot、或数据被写脏：跳过这条，
				// 让接口如实返回 remaining，而不是假装成功。
				a.log.Warn("maildir path outside root for read-all", "message", item.id, "path", item.rawPath)
				continue
			}
			target := maildirPathWithFlags(item.rawPath, true, item.starred)
			if target != item.rawPath {
				if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr == nil {
					if renameErr := os.Rename(item.rawPath, target); renameErr == nil {
						_ = applyMaildirOwnership(target)
						newPath = target
					} else {
						// 文件没改成 S 标志就绝不能把库标成已读：下一轮同步
						// 会按缺 S 翻回未读，接口却已经报告成功了。
						a.log.Warn("failed to rename maildir file for read-all", "message", item.id, "error", renameErr)
						continue
					}
				} else {
					a.log.Warn("failed to prepare maildir directory for read-all", "message", item.id, "error", mkErr)
					continue
				}
			}
		}
		if _, err := a.db.ExecContext(ctx, `UPDATE messages SET is_read=1,raw_path=?,imap_modseq=?,updated_at=? WHERE id=?`,
			newPath, modSeq, now, item.id); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}
