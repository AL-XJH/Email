package app

import "strings"

// SQL 生成只面向 SQLite。
//
// 这些函数的第一参数曾经是数据库方言，用来在 SQLite / MySQL / PostgreSQL 之间
// 切换（MySQL 的 INSERT IGNORE、Postgres 的 ON CONFLICT 等）。PostgreSQL/MySQL
// 支持已删除，参数保留只是为了不动几十个调用点；生成的永远是 SQLite 语法。

func insertIgnoreSQL(_, insert, _ string) string {
	return strings.Replace(insert, "INSERT INTO", "INSERT OR IGNORE INTO", 1)
}

func upsertSQL(_, insert, conflictTarget, update, _ string) string {
	return insert + " ON CONFLICT " + conflictTarget + " DO UPDATE SET " + update
}

// scalarMaxSQL 在 SQLite 里是 MAX(a,b)，不再需要 GREATEST。
func scalarMaxSQL(_, left, right string) string {
	return "MAX(" + left + "," + right + ")"
}

func permissionGroupRenameSQL(_ string) string {
	return `UPDATE permission_groups SET name=name || ' (' || id || ')' WHERE name=? AND id<>?`
}

func permissionGroupSystemColumnSQL(_ string) string {
	return "system"
}

func keyColumnSQL(_ string) string {
	return "key"
}

func apiTokenLastUsedUpdateSQL(_ string) string {
	// SQLite 把时间存成文本，直接字符串比较对 RFC3339 不可靠，统一用 julianday。
	return `UPDATE api_tokens SET last_used_at=?
		WHERE id=? AND (last_used_at IS NULL OR julianday(last_used_at) < julianday(?))`
}

// lockUserQuotaRowSQL 用一个空写把事务提升为写事务，串行化「先计数再插入」。
// SQLite 没有行级锁和 FOR UPDATE，这是它唯一的做法。
func lockUserQuotaRowSQL(_ string) string {
	return `UPDATE users SET updated_at=updated_at WHERE id=?`
}
