package app

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// migrateExternalSchemaV13 补上发件人白名单表。
//
// 白名单是 v1.2.x 加的功能，只写进了 SQLite 的建表语句和 PostgreSQL 的 fresh
// schema；已经跑到 v12 的外部库不会自动多出这张表，启动时校验会直接拒绝，
// 所以这里必须显式迁移。
func migrateExternalSchemaV13(ctx context.Context, conn *sql.Conn, driver string) error {
	var count, version int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(MAX(version),0) FROM schema_migrations").Scan(&count, &version); err != nil {
		return err
	}
	if count >= 13 && version >= 13 {
		return nil
	}
	if count != 12 || version != 12 {
		return fmt.Errorf("unexpected schema before v13: %d/%d", count, version)
	}
	var executor externalSchemaExecutor = conn
	var tx *sql.Tx
	if driver == databaseDriverPostgres {
		var err error
		tx, err = conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		executor = tx
	}
	table := `CREATE TABLE IF NOT EXISTS allowed_senders (
			id VARCHAR(64) PRIMARY KEY,
			user_id VARCHAR(64) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			mailbox_id VARCHAR(64) NOT NULL DEFAULT '',
			email VARCHAR(320) NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			created_at VARCHAR(35) NOT NULL,
			updated_at VARCHAR(35) NOT NULL,
			UNIQUE(user_id,mailbox_id,email)
		)`
	if driver == databaseDriverMySQL {
		table = postgresTableToMySQL(table)
	}
	if _, err := executor.ExecContext(ctx, table); err != nil {
		return err
	}
	if _, err := executor.ExecContext(ctx, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(13,'external_schema_v13_allowed_senders',CURRENT_TIMESTAMP)"); err != nil {
		return err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	// 索引单独建：MySQL 不支持 CREATE INDEX IF NOT EXISTS，
	// 重复建索引就当成已完成。
	index := "CREATE INDEX IF NOT EXISTS idx_allowed_senders_user_mailbox ON allowed_senders(user_id,mailbox_id,email)"
	if driver == databaseDriverMySQL {
		index = "CREATE INDEX idx_allowed_senders_user_mailbox ON allowed_senders(user_id,mailbox_id,email)"
	}
	if _, err := conn.ExecContext(ctx, index); err != nil {
		if driver != databaseDriverMySQL || !strings.Contains(strings.ToLower(err.Error()), "duplicate key name") {
			return err
		}
	}
	return nil
}
