package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// 只支持 SQLite。
//
// 之前为了兼容 PostgreSQL / MySQL 维护了一整套方言分支、两套 schema 生成器
// 和一个占位符重写驱动，但两个部署都没人用；这些分支反而不断制造只在某一个
// 库上出现的 bug（MySQL 把 `||` 当逻辑或、事务外 FOR UPDATE 不持锁、
// 非 SQLite 分支漏建热路径索引）。现在 SQL 一律按 SQLite 生成。
const databaseDriverSQLite = "sqlite"

func openDatabase(ctx context.Context, cfg Config) (*sql.DB, error) {
	if strings.TrimSpace(cfg.DBPath) == "" {
		return nil, errors.New("sqlite database path is required")
	}
	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	// SQLite 串行化写入。单连接也让 connection-scoped PRAGMA 保持一致。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	timeout := time.Duration(cfg.DBConnectTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to sqlite database: %w", err)
	}
	return db, nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") ||
		strings.Contains(message, "constraint failed")
}
