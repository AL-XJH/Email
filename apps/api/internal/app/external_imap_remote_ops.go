package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 外部邮箱的「本地操作」以前只改本地库，远端原封不动：在网页面板里把邮件
// 移进垃圾邮件/回收站，Gmail、Outlook、QQ 那边看不到任何变化。这里补齐远端
// 写入，语义跟主流客户端一致：
//   - 移动 = 远端 MOVE（服务端不支持时 go-imap 自动降级 COPY+STORE+EXPUNGE）
//   - 删除第一下 = 远端移到回收站
//   - 在回收站里再删 = 远端加 \Deleted 并 EXPUNGE，真正删掉
//
// 远端失败直接返回错误让接口报给前端，不做「本地成功远端静默失败」，否则两边
// 会越走越偏。

type externalIMAPRemoteMutationError struct {
	Err error
}

func (e *externalIMAPRemoteMutationError) Error() string { return e.Err.Error() }
func (e *externalIMAPRemoteMutationError) Unwrap() error { return e.Err }

func externalIMAPRemoteError(format string, args ...any) error {
	return &externalIMAPRemoteMutationError{Err: fmt.Errorf(format, args...)}
}

type externalIMAPRemoteMapping struct {
	AccountID    string
	RemoteFolder string
	UIDValidity  uint32
	UID          uint32
	MessageID    string
	IsRead       bool
}

func (a *App) externalIMAPMappingsForMessage(ctx context.Context, messageID string) ([]externalIMAPRemoteMapping, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT account_id,remote_folder,uid_validity,uid,message_id,is_read FROM external_imap_messages WHERE local_message_id=?`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []externalIMAPRemoteMapping{}
	for rows.Next() {
		var item externalIMAPRemoteMapping
		var read int
		if err := rows.Scan(&item.AccountID, &item.RemoteFolder, &item.UIDValidity, &item.UID, &item.MessageID, &read); err != nil {
			return nil, err
		}
		item.IsRead = intBool(read)
		items = append(items, item)
	}
	return items, rows.Err()
}

// externalIMAPRemoteFolderForLocal 把本地文件夹换成该账号的远端文件夹名。
// 先按 folder_states 的 local_folder_id 精确匹配（镜像文件夹），再按系统
// 角色（Inbox/Spam/Trash/Archive/Drafts/Sent）在文件夹快照里找。都找不到就
// 说明这是纯本地文件夹，调用方不做远端操作。
func (a *App) externalIMAPRemoteFolderForLocal(ctx context.Context, accountID, localFolderID, localFolderName string) (string, bool) {
	if strings.TrimSpace(accountID) == "" {
		return "", false
	}
	if strings.TrimSpace(localFolderID) != "" {
		var remote string
		if err := a.db.QueryRowContext(ctx, `SELECT remote_folder FROM external_imap_folder_states WHERE account_id=? AND local_folder_id=? LIMIT 1`, accountID, localFolderID).Scan(&remote); err == nil && strings.TrimSpace(remote) != "" {
			return remote, true
		}
	}
	role := externalIMAPSystemRole(localFolderName)
	if role == "" {
		return "", false
	}
	folders, _, ok := a.loadExternalIMAPFolderSnapshot(ctx, accountID)
	if !ok {
		return "", false
	}
	for _, folder := range folders {
		if strings.EqualFold(folder.Role, role) {
			return folder.Name, true
		}
	}
	// QQ 的回收站不带 SPECIAL-USE，role 是 "Deleted Messages" 这种非标准值。
	if role == "Trash" {
		for _, candidate := range []string{"Deleted Messages", "已删除", "Deleted"} {
			for _, folder := range folders {
				if strings.EqualFold(folder.Name, candidate) {
					return folder.Name, true
				}
			}
		}
	}
	return "", false
}

func externalIMAPSystemRole(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "inbox", "收件箱":
		return "Inbox"
	case "spam", "junk", "垃圾邮件":
		return "Spam"
	case "trash", "deleted", "已删除", "已删除邮件":
		return "Trash"
	case "archive", "存档", "归档":
		return "Archive"
	case "drafts", "草稿":
		return "Drafts"
	case "sent", "已发送", "已发邮件":
		return "Sent"
	}
	return ""
}

// externalIMAPPrimaryMapping 选一封邮件的主映射。Gmail 一封邮件会同时挂在
// INBOX 和「所有邮件」下，移动要挑跟当前本地文件夹对应的那条，否则从「所有
// 邮件」里 MOVE 等于把整封信从所有位置删掉。
func (a *App) externalIMAPPrimaryMapping(ctx context.Context, msg *MailMessage, mappings []externalIMAPRemoteMapping) *externalIMAPRemoteMapping {
	if len(mappings) == 0 {
		return nil
	}
	if msg != nil && strings.TrimSpace(msg.FolderID) != "" {
		var remote string
		if err := a.db.QueryRowContext(ctx, `SELECT remote_folder FROM external_imap_folder_states WHERE local_folder_id=? AND account_id=? LIMIT 1`, msg.FolderID, mappings[0].AccountID).Scan(&remote); err == nil && strings.TrimSpace(remote) != "" {
			for i := range mappings {
				if strings.EqualFold(mappings[i].RemoteFolder, remote) {
					return &mappings[i]
				}
			}
		}
	}
	for i := range mappings {
		if !strings.EqualFold(mappings[i].RemoteFolder, "[Gmail]/所有邮件") {
			return &mappings[i]
		}
	}
	return &mappings[0]
}

type externalIMAPRemoteWriter interface {
	MoveMessage(ctx context.Context, folder string, uid uint32, dest string) (newUID uint32, uidValidity uint32, hasNewUID bool, err error)
	MoveMessages(ctx context.Context, folder string, uids []uint32, dest string) (newUIDs []uint32, uidValidity uint32, err error)
	DeleteMessage(ctx context.Context, folder string, uid uint32) error
	DeleteMessages(ctx context.Context, folder string, uids []uint32) error
}

func (a *App) openExternalIMAPWriter(ctx context.Context, accountID string) (externalIMAPRemoteWriter, func(), error) {
	account, err := a.externalIMAPAccountByID(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	client, err := a.openExternalIMAPClient(ctx, account)
	if err != nil {
		return nil, nil, externalIMAPRemoteError("连不上 %s：%v", account.Host, err)
	}
	writer, ok := client.(externalIMAPRemoteWriter)
	if !ok {
		client.Close()
		return nil, nil, externalIMAPRemoteError("当前 IMAP 连接不支持写入")
	}
	return writer, func() { _ = client.Close() }, nil
}

// externalIMAPMoveOnServer 把邮件在远端从当前文件夹移到 target。target 必须
// 能映射到远端文件夹；映射不到（纯本地文件夹）就只做本地移动。
func (a *App) externalIMAPMoveOnServer(ctx context.Context, msg *MailMessage, targetFolderID, targetFolderName string) error {
	mappings, err := a.externalIMAPMappingsForMessage(ctx, msg.ID)
	if err != nil {
		return err
	}
	if len(mappings) == 0 {
		return nil
	}
	source := a.externalIMAPPrimaryMapping(ctx, msg, mappings)
	if source == nil {
		return nil
	}
	target, ok := a.externalIMAPRemoteFolderForLocal(ctx, source.AccountID, targetFolderID, targetFolderName)
	if !ok {
		return nil
	}
	if strings.EqualFold(target, source.RemoteFolder) {
		return nil
	}
	writer, closeFn, err := a.openExternalIMAPWriter(ctx, source.AccountID)
	if err != nil {
		return err
	}
	defer closeFn()
	newUID, uidValidity, hasNewUID, err := writer.MoveMessage(ctx, source.RemoteFolder, source.UID, target)
	if err != nil {
		return externalIMAPRemoteError("远端移动 %s → %s 失败：%v", source.RemoteFolder, target, err)
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	// 远端 UID 变了，本地映射得跟着走；服务端没回 COPYUID（没有 UIDPLUS）时
	// 就摘掉映射，让下一轮同步按 Message-ID 去重后重新挂上。
	_, _ = a.db.ExecContext(ctx, `DELETE FROM external_imap_messages WHERE account_id=? AND remote_folder=? AND uid_validity=? AND uid=?`,
		source.AccountID, source.RemoteFolder, source.UIDValidity, source.UID)
	if hasNewUID {
		if uidValidity == 0 {
			uidValidity = source.UIDValidity
		}
		_, _ = a.db.ExecContext(ctx, `INSERT OR REPLACE INTO external_imap_messages(account_id,remote_folder,uid_validity,uid,message_id,local_message_id,is_read,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			source.AccountID, target, uidValidity, newUID, source.MessageID, msg.ID, boolInt(source.IsRead), now, now)
	}
	return nil
}

// externalIMAPMoveManyOnServer 是批量移动：同一账号、同一源文件夹的邮件用
// 一条连接、一条 MOVE 命令搞定，避免 200 封邮件开 200 次 IMAP。返回按消息
// ID 记的失败原因，成功的消息由调用方接着做本地移动。
func (a *App) externalIMAPMoveManyOnServer(ctx context.Context, messages []*MailMessage, targetFolderID, targetFolderName string) map[string]error {
	failed := map[string]error{}
	type group struct {
		accountID string
		source    string
		items     []struct {
			msg     *MailMessage
			mapping externalIMAPRemoteMapping
		}
	}
	groups := map[string]*group{}
	for _, msg := range messages {
		mappings, err := a.externalIMAPMappingsForMessage(ctx, msg.ID)
		if err != nil || len(mappings) == 0 {
			continue
		}
		primary := a.externalIMAPPrimaryMapping(ctx, msg, mappings)
		if primary == nil {
			continue
		}
		key := primary.AccountID + "\x00" + primary.RemoteFolder
		item, ok := groups[key]
		if !ok {
			item = &group{accountID: primary.AccountID, source: primary.RemoteFolder}
			groups[key] = item
		}
		item.items = append(item.items, struct {
			msg     *MailMessage
			mapping externalIMAPRemoteMapping
		}{msg: msg, mapping: *primary})
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	for _, item := range groups {
		target, ok := a.externalIMAPRemoteFolderForLocal(ctx, item.accountID, targetFolderID, targetFolderName)
		if !ok || strings.EqualFold(target, item.source) {
			continue
		}
		writer, closeFn, err := a.openExternalIMAPWriter(ctx, item.accountID)
		if err != nil {
			for _, entry := range item.items {
				failed[entry.msg.ID] = err
			}
			continue
		}
		uids := make([]uint32, 0, len(item.items))
		uidValidity := item.items[0].mapping.UIDValidity
		for _, entry := range item.items {
			uids = append(uids, entry.mapping.UID)
		}
		newUIDs, destValidity, err := writer.MoveMessages(ctx, item.source, uids, target)
		closeFn()
		if err != nil {
			for _, entry := range item.items {
				failed[entry.msg.ID] = err
			}
			continue
		}
		if destValidity == 0 {
			destValidity = uidValidity
		}
		for _, entry := range item.items {
			_, _ = a.db.ExecContext(ctx, `DELETE FROM external_imap_messages WHERE account_id=? AND remote_folder=? AND uid_validity=? AND uid=?`,
				entry.mapping.AccountID, item.source, entry.mapping.UIDValidity, entry.mapping.UID)
		}
		if len(newUIDs) == len(item.items) {
			for i, entry := range item.items {
				_, _ = a.db.ExecContext(ctx, `INSERT OR REPLACE INTO external_imap_messages(account_id,remote_folder,uid_validity,uid,message_id,local_message_id,is_read,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
					entry.mapping.AccountID, target, destValidity, newUIDs[i], entry.mapping.MessageID, entry.msg.ID, boolInt(entry.mapping.IsRead), now, now)
			}
		}
	}
	return failed
}

// externalIMAPDeleteMapping 选一封邮件该从哪个远端文件夹彻底删除。
// 当前在远端回收站里就从回收站删（Gmail 的「所有邮件」不含回收站里的邮件，
// 拿旧 UID 去 EXPUNGE 是空操作）；否则优先「所有邮件」，一次清干净。
func (a *App) externalIMAPDeleteMapping(ctx context.Context, msg *MailMessage) (externalIMAPRemoteMapping, bool) {
	mappings, err := a.externalIMAPMappingsForMessage(ctx, msg.ID)
	if err != nil || len(mappings) == 0 {
		return externalIMAPRemoteMapping{}, false
	}
	primary := a.externalIMAPPrimaryMapping(ctx, msg, mappings)
	chosen := mappings[0]
	if primary != nil {
		chosen = *primary
	}
	if trashRemote, ok := a.externalIMAPRemoteFolderForLocal(ctx, chosen.AccountID, "", "Trash"); !ok || !strings.EqualFold(chosen.RemoteFolder, trashRemote) {
		for _, item := range mappings {
			if strings.EqualFold(item.RemoteFolder, "[Gmail]/所有邮件") {
				chosen = item
				break
			}
		}
	}
	return chosen, true
}

// externalIMAPDeleteManyOnServer 批量彻底删除：同账号同远端文件夹一条连接、
// 一条 STORE + UID EXPUNGE。返回按消息 ID 记的失败原因。
func (a *App) externalIMAPDeleteManyOnServer(ctx context.Context, messages []*MailMessage) map[string]error {
	failed := map[string]error{}
	type group struct {
		accountID string
		folder    string
		items     []struct {
			msg     *MailMessage
			mapping externalIMAPRemoteMapping
		}
	}
	groups := map[string]*group{}
	for _, msg := range messages {
		mapping, ok := a.externalIMAPDeleteMapping(ctx, msg)
		if !ok {
			continue
		}
		key := mapping.AccountID + "\x00" + mapping.RemoteFolder
		item, exists := groups[key]
		if !exists {
			item = &group{accountID: mapping.AccountID, folder: mapping.RemoteFolder}
			groups[key] = item
		}
		item.items = append(item.items, struct {
			msg     *MailMessage
			mapping externalIMAPRemoteMapping
		}{msg: msg, mapping: mapping})
	}
	for _, item := range groups {
		writer, closeFn, err := a.openExternalIMAPWriter(ctx, item.accountID)
		if err != nil {
			for _, entry := range item.items {
				failed[entry.msg.ID] = err
			}
			continue
		}
		uids := make([]uint32, 0, len(item.items))
		for _, entry := range item.items {
			uids = append(uids, entry.mapping.UID)
		}
		err = writer.DeleteMessages(ctx, item.folder, uids)
		closeFn()
		if err != nil {
			for _, entry := range item.items {
				failed[entry.msg.ID] = err
			}
		}
	}
	return failed
}

// externalIMAPDeleteOnServer 真正从远端删掉一封邮件。
func (a *App) externalIMAPDeleteOnServer(ctx context.Context, msg *MailMessage) error {
	chosen, ok := a.externalIMAPDeleteMapping(ctx, msg)
	if !ok {
		return nil
	}
	writer, closeFn, err := a.openExternalIMAPWriter(ctx, chosen.AccountID)
	if err != nil {
		return err
	}
	defer closeFn()
	if err := writer.DeleteMessage(ctx, chosen.RemoteFolder, chosen.UID); err != nil {
		return externalIMAPRemoteError("远端删除 %s 里的邮件失败：%v", chosen.RemoteFolder, err)
	}
	return nil
}

// externalIMAPMirrorFolderIDForRole 找账号镜像出来的本地系统文件夹（垃圾邮件、
// 回收站等），删除时把本地邮件也放进同一处，外部视图才不会「删完消失」。
func (a *App) externalIMAPMirrorFolderIDForRole(ctx context.Context, accountID, role string) (string, bool) {
	if strings.TrimSpace(accountID) == "" {
		return "", false
	}
	remote, ok := a.externalIMAPRemoteFolderForLocal(ctx, accountID, "", role)
	if !ok {
		return "", false
	}
	var localID string
	if err := a.db.QueryRowContext(ctx, `SELECT local_folder_id FROM external_imap_folder_states WHERE account_id=? AND remote_folder=?`, accountID, remote).Scan(&localID); err != nil || strings.TrimSpace(localID) == "" {
		return "", false
	}
	return localID, true
}

func isExternalIMAPRemoteMutationError(err error) bool {
	var target *externalIMAPRemoteMutationError
	return errors.As(err, &target)
}
