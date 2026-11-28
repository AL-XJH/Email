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
	// 外部账号导入件：先把远端也移过去，成功才动本地。远端失败就整体失败，
	// 避免本地显示已移动、邮箱那边还在原地。
	if err := a.externalIMAPMoveOnServer(ctx, msg, folderID, name); err != nil {
		return err
	}
	return a.moveMessageMaildir(ctx, msg.ID, folderID)
}

// 已经在 Trash 里的再删就是彻底删除，否则先移进 Trash —— 与单封删除一致。
func (a *App) applyMessageDelete(ctx context.Context, msg *MailMessage) error {
	// 外部导入件在修复去重之前可能留下同一封邮件的多份本地副本（不同文件夹各
	// 一行）。删除时只处理一行会让另一份继续留在收件箱里，看起来像没删掉。
	// 这里把同 mailbox、同 Message-ID 的其它副本一起处理。
	external := a.messageHasExternalMapping(ctx, msg.ID)
	err := a.applyMessageDeleteOne(ctx, msg)
	if err != nil {
		return err
	}
	if !external || strings.TrimSpace(msg.MessageID) == "" {
		return nil
	}
	twins, twinErr := a.otherMessageCopies(ctx, msg.MailboxID, msg.MessageID, msg.ID)
	if twinErr != nil {
		// 找同 Message-ID 副本失败不该阻断主删除，但得留下痕迹：否则「所有邮件删了
		// 收件箱还在」这类问题会再次无声复发。
		a.log.Warn("failed to find duplicate message copies", "message", msg.ID, "error", twinErr)
	}
	for _, twin := range twins {
		if twinErr := a.applyMessageDeleteOne(ctx, twin); twinErr != nil {
			a.log.Warn("failed to delete duplicate external message copy", "message", twin.ID, "message_id", msg.MessageID, "error", twinErr)
		}
	}
	return nil
}

func (a *App) messageHasExternalMapping(ctx context.Context, messageID string) bool {
	var exists int
	return a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM external_imap_messages WHERE local_message_id=?`, messageID).Scan(&exists) == nil && exists > 0
}

// otherMessageCopies 找同邮箱里 Message-ID 相同的其它本地副本。
func (a *App) otherMessageCopies(ctx context.Context, mailboxID, messageID, excludeID string) ([]*MailMessage, error) {
	if messageID == "" {
		return nil, nil
	}
	rows, err := a.db.QueryContext(ctx, `SELECT m.id,m.mailbox_id,COALESCE(m.folder_id,''),COALESCE(f.name,''),m.is_read,m.is_starred,m.message_id
		FROM messages m LEFT JOIN folders f ON f.id=m.folder_id
		WHERE m.mailbox_id=? AND m.message_id=? AND m.id<>?`, mailboxID, messageID, excludeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MailMessage{}
	for rows.Next() {
		var item MailMessage
		var read, starred int
		if err := rows.Scan(&item.ID, &item.MailboxID, &item.FolderID, &item.Folder, &read, &starred, &item.MessageID); err != nil {
			return nil, err
		}
		item.IsRead, item.IsStarred = intBool(read), intBool(starred)
		out = append(out, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *App) applyMessageDeleteOne(ctx context.Context, msg *MailMessage) error {
	// 外部导入件要跟着邮箱一起动：第一下移到远端回收站，回收站里再删就是
	// 远端 \Deleted + EXPUNGE。本地也放进镜像回收站，外部视图才看得到。
	if mappings, mapErr := a.externalIMAPMappingsForMessage(ctx, msg.ID); mapErr != nil {
		a.log.Warn("failed to load external imap mapping for delete", "message", msg.ID, "error", mapErr)
	} else if len(mappings) > 0 {
		accountID := mappings[0].AccountID
		if strings.EqualFold(msg.Folder, "Trash") {
			if err := a.externalIMAPDeleteOnServer(ctx, msg); err != nil {
				return err
			}
		} else {
			trashID, hasMirror := a.externalIMAPMirrorFolderIDForRole(ctx, accountID, "Trash")
			if err := a.externalIMAPMoveOnServer(ctx, msg, trashID, "Trash"); err != nil {
				return err
			}
			if hasMirror {
				return a.moveMessageMaildir(ctx, msg.ID, trashID)
			}
		}
	}
	return a.applyMessageDeleteLocal(ctx, msg)
}

// applyMessageDeleteLocal 只动本地：Trash 里的彻底删文件+记墓碑，否则移进
// 本地 Trash。远端操作由调用方先做（单封逐条、批量按文件夹合并）。
func (a *App) applyMessageDeleteLocal(ctx context.Context, msg *MailMessage) error {
	if strings.EqualFold(msg.Folder, "Trash") {
		// 文件删不掉就不能删库里的行：否则下一轮 maildir 同步会把文件
		// 当成新邮件重新导入，「清空垃圾箱」5 秒后自己撤销。
		if err := a.deleteMessageMaildirFile(ctx, msg.ID); err != nil {
			return fmt.Errorf("failed to remove maildir file: %w", err)
		}
		a.deleteMessageFiles(ctx, msg.ID)
		// 外部导入件要留远端 UID 墓碑并摘掉映射，否则下一轮同步会把删掉的信
		// 当成「没导入过」重新拉回来。
		a.recordExternalIMAPDeletion(ctx, msg.ID)
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
		IDs               []string `json:"ids"`
		Action            string   `json:"action"`
		ExternalAccountID string   `json:"externalAccountId"`
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
	query := `SELECT m.id,m.mailbox_id,COALESCE(m.folder_id,''),COALESCE(f.name,''),m.is_read,m.is_starred,COALESCE(m.message_id,'') FROM messages m JOIN mailboxes mb ON mb.id=m.mailbox_id LEFT JOIN folders f ON f.id=m.folder_id WHERE mb.user_id=? AND m.id IN (` + strings.Join(marks, ",") + `)`
	rows, err := a.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load messages")
		return
	}
	owned := map[string]*MailMessage{}
	for rows.Next() {
		var msg MailMessage
		var read, starred int
		if err := rows.Scan(&msg.ID, &msg.MailboxID, &msg.FolderID, &msg.Folder, &read, &starred, &msg.MessageID); err != nil {
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

	// 外部账号视图的批量「移入垃圾邮件/回收站」同样要落到镜像文件夹，
	// 否则邮件会掉进外部列表看不见的普通系统文件夹。
	mirrorFolder := ""
	externalMapped := map[string]bool{}
	if strings.TrimSpace(req.ExternalAccountID) != "" && folder != "" {
		if mirror, ok := a.externalIMAPMirrorFolderNameForRole(r.Context(), req.ExternalAccountID, folder); ok {
			mirrorFolder = mirror
			marks := make([]string, 0, len(ids))
			args := []any{req.ExternalAccountID}
			for _, id := range ids {
				marks = append(marks, "?")
				args = append(args, id)
			}
			rows, err := a.db.QueryContext(r.Context(), `SELECT local_message_id FROM external_imap_messages WHERE account_id=? AND local_message_id IN (`+strings.Join(marks, ",")+`)`, args...)
			if err == nil {
				for rows.Next() {
					var id string
					if rows.Scan(&id) == nil {
						externalMapped[id] = true
					}
				}
				rows.Close()
			}
		}
	}
	// 外部映射件的移动/删除先把远端批量做完（每账号/源文件夹一条连接），
	// 本地循环里就只挪 maildir，不然 200 封会开 200 次 IMAP。
	remoteFailed := map[string]error{}
	batchDeleted := map[string]bool{}
	folderIDs := map[string]string{}
	if action == "delete" && len(ids) > 0 {
		marks := make([]string, 0, len(ids))
		args := make([]any, 0, len(ids))
		for _, id := range ids {
			marks = append(marks, "?")
			args = append(args, id)
		}
		rows, err := a.db.QueryContext(r.Context(), `SELECT DISTINCT local_message_id FROM external_imap_messages WHERE local_message_id IN (`+strings.Join(marks, ",")+`)`, args...)
		if err == nil {
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					externalMapped[id] = true
				}
			}
			rows.Close()
		}
		var trashMessages []*MailMessage
		for id := range externalMapped {
			if msg := owned[id]; msg != nil && strings.EqualFold(msg.Folder, "Trash") {
				trashMessages = append(trashMessages, msg)
			}
		}
		if len(trashMessages) > 0 {
			for id, itemErr := range a.externalIMAPDeleteManyOnServer(r.Context(), trashMessages) {
				remoteFailed[id] = itemErr
			}
			for _, msg := range trashMessages {
				if remoteFailed[msg.ID] == nil {
					batchDeleted[msg.ID] = true
				}
			}
		}
	}
	if mirrorFolder != "" && len(externalMapped) > 0 && action != "delete" {
		byMailbox := map[string][]*MailMessage{}
		for id := range externalMapped {
			if msg := owned[id]; msg != nil {
				byMailbox[msg.MailboxID] = append(byMailbox[msg.MailboxID], msg)
			}
		}
		for mailboxID, list := range byMailbox {
			targetID, err := a.ensureFolder(r.Context(), mailboxID, mirrorFolder)
			if err != nil {
				for _, msg := range list {
					remoteFailed[msg.ID] = err
				}
				continue
			}
			folderIDs[mailboxID] = targetID
			for id, itemErr := range a.externalIMAPMoveManyOnServer(r.Context(), list, targetID, mirrorFolder) {
				remoteFailed[id] = itemErr
			}
		}
	}
	updated := 0
	missing := make([]string, 0)
	failed := make([]string, 0)
	for _, id := range ids {
		msg := owned[id]
		if msg == nil {
			missing = append(missing, id)
			continue
		}
		// 每封独立的错误变量：之前复用外层的 err，前面某封建文件夹失败后，
		// 后面命中 folderIDs 缓存、根本没尝试移动的邮件也会被算成失败。
		var itemErr error
		switch action {
		case "read":
			itemErr = a.applyMessageRead(r.Context(), msg, true)
		case "unread":
			itemErr = a.applyMessageRead(r.Context(), msg, false)
		case "star":
			itemErr = a.applyMessageStar(r.Context(), msg, true)
		case "unstar":
			itemErr = a.applyMessageStar(r.Context(), msg, false)
		case "delete":
			if batchDeleted[msg.ID] {
				itemErr = a.applyMessageDeleteLocal(r.Context(), msg)
			} else {
				itemErr = a.applyMessageDelete(r.Context(), msg)
			}
		default:
			if err := remoteFailed[msg.ID]; err != nil {
				itemErr = err
				break
			}
			targetFolder := normalizedFolder
			if mirrorFolder != "" && externalMapped[msg.ID] {
				targetFolder = mirrorFolder
			}
			folderID, ok := folderIDs[msg.MailboxID]
			if !ok {
				folderID, itemErr = a.ensureFolder(r.Context(), msg.MailboxID, targetFolder)
				if itemErr == nil {
					folderIDs[msg.MailboxID] = folderID
				}
			}
			if itemErr == nil {
				itemErr = a.moveMessageMaildir(r.Context(), msg.ID, folderID)
			}
		}
		if itemErr != nil {
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
