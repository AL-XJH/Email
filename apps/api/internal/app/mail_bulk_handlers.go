package app

import (
	"context"
	"errors"
	"net/http"
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
		a.deleteMessageMaildirFile(ctx, msg.ID)
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
	query := `SELECT m.id FROM messages m JOIN mailboxes mb ON mb.id=m.mailbox_id WHERE mb.user_id=? AND m.id IN (` + strings.Join(marks, ",") + `)`
	rows, err := a.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load messages")
		return
	}
	owned := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			respondError(w, http.StatusInternalServerError, "failed to load messages")
			return
		}
		owned[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load messages")
		return
	}

	updated := 0
	missing := make([]string, 0)
	failed := make([]string, 0)
	for _, id := range ids {
		if !owned[id] {
			missing = append(missing, id)
			continue
		}
		msg, err := a.messageByID(r.Context(), id, false)
		if err != nil {
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
			err = a.applyMessageMove(r.Context(), msg, folder)
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
