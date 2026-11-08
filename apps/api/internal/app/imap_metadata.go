package app

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type imapMetadata struct {
	UID    int64
	ModSeq int64
}

func (a *App) migrateIMAPMetadata(ctx context.Context) error {
	if err := a.ensureTableColumn(ctx, "folders", "uid_validity", `ALTER TABLE folders ADD COLUMN uid_validity INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := a.ensureTableColumn(ctx, "folders", "uid_next", `ALTER TABLE folders ADD COLUMN uid_next INTEGER NOT NULL DEFAULT 1`); err != nil {
		return err
	}
	if err := a.ensureTableColumn(ctx, "folders", "highest_modseq", `ALTER TABLE folders ADD COLUMN highest_modseq INTEGER NOT NULL DEFAULT 1`); err != nil {
		return err
	}
	if err := a.ensureTableColumn(ctx, "messages", "imap_uid", `ALTER TABLE messages ADD COLUMN imap_uid INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := a.ensureTableColumn(ctx, "messages", "imap_modseq", `ALTER TABLE messages ADD COLUMN imap_modseq INTEGER NOT NULL DEFAULT 1`); err != nil {
		return err
	}
	if _, err := a.db.ExecContext(ctx, `UPDATE folders SET uid_validity=? WHERE uid_validity=0`, a.newUIDValidity()); err != nil {
		return err
	}
	if _, err := a.db.ExecContext(ctx, `UPDATE folders SET uid_next=1 WHERE uid_next<1`); err != nil {
		return err
	}
	if _, err := a.db.ExecContext(ctx, `UPDATE folders SET highest_modseq=1 WHERE highest_modseq<1`); err != nil {
		return err
	}
	if err := a.backfillIMAPUIDs(ctx); err != nil {
		return err
	}
	_, err := a.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_folder_imap_uid ON messages(folder_id, imap_uid) WHERE folder_id IS NOT NULL AND imap_uid > 0`)
	return err
}

func (a *App) migrateFolderSortOrder(ctx context.Context) error {
	if err := a.ensureTableColumn(ctx, "folders", "sort_order", `ALTER TABLE folders ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	rows, err := a.db.QueryContext(ctx, `SELECT id FROM folders WHERE lower(name) NOT IN ('inbox','sent','drafts','archive','spam','trash') ORDER BY mailbox_id, created_at, name, id`)
	if err != nil {
		return err
	}
	var folderIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		folderIDs = append(folderIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	order := customFolderDefaultSortOrderBase + 1
	for _, id := range folderIDs {
		if _, err := a.db.ExecContext(ctx, `UPDATE folders SET sort_order=? WHERE id=? AND sort_order=0`, order, id); err != nil {
			return err
		}
		order++
	}
	return nil
}

func (a *App) ensureTableColumn(ctx context.Context, table, column, alterSQL string) error {
	rows, err := a.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, alterSQL)
	return err
}

func (a *App) backfillIMAPUIDs(ctx context.Context) error {
	rows, err := a.db.QueryContext(ctx, `SELECT id FROM folders ORDER BY created_at,id`)
	if err != nil {
		return err
	}
	var folderIDs []string
	for rows.Next() {
		var folderID string
		if err := rows.Scan(&folderID); err != nil {
			rows.Close()
			return err
		}
		folderIDs = append(folderIDs, folderID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, folderID := range folderIDs {
		if err := a.backfillFolderIMAPUIDs(ctx, folderID); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) backfillFolderIMAPUIDs(ctx context.Context, folderID string) error {
	rows, err := a.db.QueryContext(ctx, `SELECT id FROM messages WHERE folder_id=? AND imap_uid=0 ORDER BY created_at,id`, folderID)
	if err != nil {
		return err
	}
	var messageIDs []string
	for rows.Next() {
		var messageID string
		if err := rows.Scan(&messageID); err != nil {
			rows.Close()
			return err
		}
		messageIDs = append(messageIDs, messageID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, messageID := range messageIDs {
		meta, err := a.nextIMAPMetadata(ctx, a.db, folderID)
		if err != nil {
			return err
		}
		if _, err := a.db.ExecContext(ctx, `UPDATE messages SET imap_uid=?,imap_modseq=? WHERE id=?`, meta.UID, meta.ModSeq, messageID); err != nil {
			return err
		}
	}
	var maxUID, maxModSeq int64
	if err := a.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(imap_uid),0),COALESCE(MAX(imap_modseq),1) FROM messages WHERE folder_id=?`, folderID).Scan(&maxUID, &maxModSeq); err != nil {
		return err
	}
	query := `UPDATE folders SET uid_next=` + scalarMaxSQL(a.cfg.DBDriver, "uid_next", "?") + `,highest_modseq=` + scalarMaxSQL(a.cfg.DBDriver, "highest_modseq", "?") + ` WHERE id=?`
	_, err = a.db.ExecContext(ctx, query, maxUID+1, maxModSeq, folderID)
	return err
}

func (a *App) newUIDValidity() int64 {
	value := a.now().UTC().Unix()
	if value <= 0 {
		return time.Now().UTC().Unix()
	}
	return value
}

// imapClampSQL 返回该驱动下「不小于某值」的标量写法。
// SQLite 是 MAX(a,b)（两参数版是标量函数），MySQL/Postgres 是 GREATEST(a,b)。
func imapClampSQL(driver, column string, floor int) string {
	if driver == databaseDriverSQLite {
		return fmt.Sprintf("MAX(%s,%d)", column, floor)
	}
	return fmt.Sprintf("GREATEST(%s,%d)", column, floor)
}

// nextIMAPMetadata 领取一个新的 IMAP UID 与 MODSEQ。
//
// 老实现是「读 uid_next → 写回 +1 → 把读到的值写进 messages」三步，外面套一把进程内
// 互斥锁。两个问题：
//  1. 并发路径上仍然可能重复发号（批量删除第一次必然报错）；
//  2. 锁会横在事务中间——事务占着唯一的 SQLite 连接等锁、持锁的协程等连接，互等之后
//     整个进程的数据库连接被永久占死（删文件夹 / 发信事务 + 任意标记已读即可触发）。
//
// 现在 SQLite/Postgres 用单条 UPDATE ... RETURNING，既不发重号也没有锁；MySQL 没有
// RETURNING，用事务内 SELECT ... FOR UPDATE 串行化。
func (a *App) nextIMAPMetadata(ctx context.Context, db dbExecutor, folderID string) (imapMetadata, error) {
	if folderID == "" {
		return imapMetadata{}, nil
	}
	rowDB, ok := db.(dbQueryer)
	if !ok {
		return imapMetadata{}, nil
	}
	if a.cfg.DBDriver == databaseDriverMySQL {
		return a.nextIMAPMetadataMySQL(ctx, db, rowDB, folderID)
	}
	var meta imapMetadata
	query := `UPDATE folders SET uid_next=` + imapClampSQL(a.cfg.DBDriver, "uid_next", 1) + `+1,` +
		` highest_modseq=` + imapClampSQL(a.cfg.DBDriver, "highest_modseq", 0) + `+1` +
		` WHERE id=? RETURNING uid_next-1, highest_modseq`
	if err := rowDB.QueryRowContext(ctx, query, folderID).Scan(&meta.UID, &meta.ModSeq); err != nil {
		return imapMetadata{}, err
	}
	return meta, nil
}

// nextIMAPMetadataMySQL 在没有 RETURNING 的驱动上靠行锁串行化领取。
func (a *App) nextIMAPMetadataMySQL(ctx context.Context, db dbExecutor, rowDB dbQueryer, folderID string) (imapMetadata, error) {
	exec := db
	var ownTx *sql.Tx
	if sqlDB, ok := db.(*sql.DB); ok {
		tx, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			return imapMetadata{}, err
		}
		defer func() { _ = tx.Rollback() }()
		ownTx, exec = tx, tx
	}
	var current, modSeq int64
	if err := rowDB.QueryRowContext(ctx, `SELECT uid_next,highest_modseq FROM folders WHERE id=? FOR UPDATE`, folderID).Scan(&current, &modSeq); err != nil {
		return imapMetadata{}, err
	}
	if current < 1 {
		current = 1
	}
	if modSeq < 0 {
		modSeq = 0
	}
	if _, err := exec.ExecContext(ctx, `UPDATE folders SET uid_next=?,highest_modseq=? WHERE id=?`, current+1, modSeq+1, folderID); err != nil {
		return imapMetadata{}, err
	}
	if ownTx != nil {
		if err := ownTx.Commit(); err != nil {
			return imapMetadata{}, err
		}
	}
	return imapMetadata{UID: current, ModSeq: modSeq + 1}, nil
}

func (a *App) bumpFolderModSeq(ctx context.Context, folderID string) (int64, error) {
	return a.bumpFolderModSeqWithDB(ctx, a.db, folderID)
}

func (a *App) bumpFolderModSeqWithDB(ctx context.Context, db dbExecutor, folderID string) (int64, error) {
	if folderID == "" {
		return 0, nil
	}
	rowDB, ok := db.(dbQueryer)
	if !ok {
		return 0, nil
	}
	if a.cfg.DBDriver == databaseDriverMySQL {
		if _, err := db.ExecContext(ctx, `UPDATE folders SET highest_modseq=GREATEST(highest_modseq,0)+1 WHERE id=?`, folderID); err != nil {
			return 0, err
		}
		var current int64
		if err := rowDB.QueryRowContext(ctx, `SELECT highest_modseq FROM folders WHERE id=?`, folderID).Scan(&current); err != nil {
			if err == sql.ErrNoRows {
				return 0, nil
			}
			return 0, err
		}
		return current, nil
	}
	var next int64
	query := `UPDATE folders SET highest_modseq=` + imapClampSQL(a.cfg.DBDriver, "highest_modseq", 0) + `+1 WHERE id=? RETURNING highest_modseq`
	if err := rowDB.QueryRowContext(ctx, query, folderID).Scan(&next); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	return next, nil
}

func (a *App) touchMessageIMAPModSeq(ctx context.Context, messageID string) error {
	var folderID sql.NullString
	if err := a.db.QueryRowContext(ctx, `SELECT folder_id FROM messages WHERE id=?`, messageID).Scan(&folderID); err != nil {
		return err
	}
	if !folderID.Valid || folderID.String == "" {
		return nil
	}
	modSeq, err := a.bumpFolderModSeq(ctx, folderID.String)
	if err != nil {
		return err
	}
	if modSeq == 0 {
		return nil
	}
	_, err = a.db.ExecContext(ctx, `UPDATE messages SET imap_modseq=? WHERE id=?`, modSeq, messageID)
	return err
}

func (a *App) updateMessageModSeq(ctx context.Context, messageID string, folderID string) (int64, error) {
	if folderID == "" {
		var dbFolderID sql.NullString
		if err := a.db.QueryRowContext(ctx, `SELECT folder_id FROM messages WHERE id=?`, messageID).Scan(&dbFolderID); err != nil {
			return 0, err
		}
		if !dbFolderID.Valid || dbFolderID.String == "" {
			return 0, nil
		}
		folderID = dbFolderID.String
	}
	modSeq, err := a.bumpFolderModSeq(ctx, folderID)
	if err != nil {
		return 0, err
	}
	if modSeq == 0 {
		return 0, nil
	}
	_, err = a.db.ExecContext(ctx, `UPDATE messages SET imap_modseq=? WHERE id=?`, modSeq, messageID)
	return modSeq, err
}
