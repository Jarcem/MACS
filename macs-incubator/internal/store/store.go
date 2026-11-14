// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package store 孵化器本地库（模块 #17 离线缓冲载体，SQLite）
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	// modernc.org/sqlite 用 _pragma 传参，保证 busy_timeout 生效
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return &Store{db: db}, db.Ping()
}

// Close 释放连接（测试与优雅退出用）
func (s *Store) Close() error { return s.db.Close() }

// Exec 暴露底层执行（测试与诊断用）
func (s *Store) Exec(query string, args ...any) (sql.Result, error) { return s.db.Exec(query, args...) }

// Migrate 顺序执行 migrations/*.sql；schema_migrations 跟踪版本（8.1：迁移幂等可重复，
// 迁移器升级兼容存量库——未记录的旧迁移靠 IF NOT EXISTS 重放无害）
func (s *Store) Migrate(dir string) error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".sql" {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, f := range files {
		var applied int
		_ = s.db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version=?`, f).Scan(&applied)
		if applied > 0 {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(string(b)); err != nil {
			return fmt.Errorf("migrate %s: %w", f, err)
		}
		if _, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES(?)`, f); err != nil {
			return err
		}
	}
	return nil
}

// Buffer 断网时缓存上报（telemetry_events_local，synced=0；id 用 UnixNano 保证唯一，立即到期可补传）
func (s *Store) Buffer(eventType, payload string) {
	_, _ = s.db.Exec(`INSERT INTO telemetry_events_local(id,event_type,payload,synced,created_at) VALUES(?,?,?,0,?)`,
		fmt.Sprintf("%d", time.Now().UnixNano()), eventType, payload, time.Now().UTC().Format(time.RFC3339))
}

// Buffered 待补传行
type Buffered struct {
	ID        int64
	EventType string
	Payload   string
}

// DuePending 6.4.5：取退避到期且未同步的缓冲（上限 100 条/批，Q6 14 天内有效）
func (s *Store) DuePending() ([]Buffered, error) {
	rows, err := s.db.Query(`SELECT rowid,event_type,payload FROM telemetry_events_local
		WHERE synced=0 AND next_retry_at <= ? ORDER BY created_at LIMIT 100`, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Buffered
	for rows.Next() {
		var b Buffered
		if err := rows.Scan(&b.ID, &b.EventType, &b.Payload); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// MarkFailed 6.4.5 指数退避：1min→2min→4min…封顶 60min；重试 ≥100 次（≈14 天）后放弃
func (s *Store) MarkFailed(id int64) {
	var retry int
	if err := s.db.QueryRow(`SELECT retry_count FROM telemetry_events_local WHERE rowid=?`, id).Scan(&retry); err != nil {
		return
	}
	delayMin := 1 << uint(retry) // 2^n 分钟
	if delayMin > 60 {
		delayMin = 60
	}
	next := time.Now().Add(time.Duration(delayMin) * time.Minute).UnixMilli()
	_, _ = s.db.Exec(`UPDATE telemetry_events_local SET retry_count=retry_count+1, next_retry_at=? WHERE rowid=?`, next, id)
}

// PurgeExpired Q6：已同步或重试耗尽（≥100 次）且超 14 天的缓冲清理
func (s *Store) PurgeExpired() {
	cutoff := time.Now().UTC().Add(-14 * 24 * time.Hour).Format(time.RFC3339)
	_, _ = s.db.Exec(`DELETE FROM telemetry_events_local WHERE created_at < ? AND (synced=1 OR retry_count >= 100)`, cutoff)
}

// CountBuffered 统计缓冲行数（测试与诊断用）
func (s *Store) CountBuffered() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM telemetry_events_local`).Scan(&n)
	return n, err
}

// RecordDispatch dispatch_inbox 幂等登记（6.2.4：重复下发不重复执行，仅重 ACK）
// 返回 dup=true 表示该 msg_id 已处理过
func (s *Store) RecordDispatch(msgID, msgType, payload string) (bool, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO dispatch_inbox(msg_id,type,payload) VALUES(?,?,?)`,
		msgID, msgType, payload)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 0, nil
}

func (s *Store) MarkSynced(id int64) {
	_, _ = s.db.Exec(`UPDATE telemetry_events_local SET synced=1 WHERE rowid=?`, id)
}
