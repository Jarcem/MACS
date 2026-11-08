// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package store 云端库访问层（M1：SQLite 默认，PG 经 DATABASE_URL 切换见 V3.4 决策）
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

type Incubator struct {
	ID, Name, Status string
	Capacity         int
	Platforms        string
	Drivers          string
	ListenPort       int
	LastIP           string
	LastHeartbeat    string
	RegisteredAt     string
}

func Open(path string) (*Store, error) {
	// modernc.org/sqlite 用 _pragma 传参（mattn 的 _busy_timeout 不生效，会直接 SQLITE_BUSY）
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // 单写连接串行化，避免并发写锁冲突
	db.SetMaxIdleConns(1)
	return &Store{db: db}, db.Ping()
}

// Migrate 按文件名顺序幂等执行 migrations/*.sql（8.1：迁移必须幂等可重复）
func (s *Store) Migrate(dir string) error {
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

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// UpsertIncubator Q1 幂等：同名注册视为心跳刷新，更新配置并返回既有 id
func (s *Store) UpsertIncubator(i Incubator) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM incubators WHERE name=?`, i.Name).Scan(&id)
	if err == sql.ErrNoRows {
		id = i.ID
		_, err = s.db.Exec(`INSERT INTO incubators(id,name,status,capacity,platforms,drivers,listen_port,last_ip,last_heartbeat,registered_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`,
			i.ID, i.Name, "online", i.Capacity, i.Platforms, i.Drivers, i.ListenPort, i.LastIP, now(), now())
		return id, err
	}
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(`UPDATE incubators SET status='online', capacity=?, platforms=?, drivers=?, listen_port=?, last_ip=?, last_heartbeat=? WHERE id=?`,
		i.Capacity, i.Platforms, i.Drivers, i.ListenPort, i.LastIP, now(), id)
	return id, err
}

func (s *Store) TouchIncubator(id string) {
	_, _ = s.db.Exec(`UPDATE incubators SET last_heartbeat=?, status='online' WHERE id=?`, now(), id)
}

func (s *Store) MarkStaleOffline(staleBefore time.Time) {
	_, _ = s.db.Exec(`UPDATE incubators SET status='offline' WHERE last_heartbeat < ? AND status='online'`,
		staleBefore.UTC().Format(time.RFC3339))
}

func (s *Store) ListIncubators(onlineOnly bool) ([]Incubator, error) {
	q := `SELECT id,name,status,capacity,platforms,drivers,listen_port,last_ip,COALESCE(last_heartbeat,''),registered_at FROM incubators`
	if onlineOnly {
		q += ` WHERE status='online'`
	}
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Incubator
	for rows.Next() {
		var i Incubator
		if err := rows.Scan(&i.ID, &i.Name, &i.Status, &i.Capacity, &i.Platforms, &i.Drivers, &i.ListenPort, &i.LastIP, &i.LastHeartbeat, &i.RegisteredAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Store) InsertDispatchLog(id, subtaskID, incubatorID, msgID string) error {
	_, err := s.db.Exec(`INSERT INTO dispatch_log(id,subtask_id,incubator_id,msg_id,ack_status,dispatched_at) VALUES(?,?,?,?,'pending',?)`,
		id, subtaskID, incubatorID, msgID, now())
	return err
}

func (s *Store) SetDispatchAck(msgID, status string) {
	_, _ = s.db.Exec(`UPDATE dispatch_log SET ack_status=?, acked_at=? WHERE msg_id=?`, status, now(), msgID)
}

type DispatchRow struct {
	ID           string `json:"id"`
	SubtaskID    string `json:"subtask_id"`
	IncubatorID  string `json:"incubator_id"`
	MsgID        string `json:"msg_id"`
	AckStatus    string `json:"ack_status"`
	DispatchedAt string `json:"dispatched_at"`
	AckedAt      string `json:"acked_at"`
}

// ListDispatchLog 监控端调度视图（近 N 条倒序）
func (s *Store) ListDispatchLog(limit int) ([]DispatchRow, error) {
	rows, err := s.db.Query(`SELECT id,subtask_id,COALESCE(incubator_id,''),msg_id,ack_status,dispatched_at,COALESCE(acked_at,'')
		FROM dispatch_log ORDER BY dispatched_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DispatchRow{}
	for rows.Next() {
		var r DispatchRow
		if err := rows.Scan(&r.ID, &r.SubtaskID, &r.IncubatorID, &r.MsgID, &r.AckStatus, &r.DispatchedAt, &r.AckedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
