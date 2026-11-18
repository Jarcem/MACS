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

// Close 释放连接（测试与优雅退出用）
func (s *Store) Close() error { return s.db.Close() }

// Exec 暴露底层执行（测试 DDL 装配用）
func (s *Store) Exec(query string, args ...any) (sql.Result, error) { return s.db.Exec(query, args...) }

// Begin 事务入口（agentdir 标注等需要多写一致性的场景）
func (s *Store) Begin() (*sql.Tx, error) { return s.db.Begin() }

type Incubator struct {
	ID, Name, Status string
	Capacity         int
	Platforms        string
	Drivers          string
	ListenPort       int
	LastIP           string
	LastHeartbeat    string
	RegisteredAt     string
	ActiveWorkers    int
	LastLoad         float64
	Tags             string
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

// sqlNoRows 包内别名（unified.go / quality.go 幂等 upsert 判定用）
func sqlNoRows() error { return sql.ErrNoRows }

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

// TouchWithLoad 心跳刷新 + 资源画像（6.4.3 handleReport：capacity/load 落库供负载均衡评分）
func (s *Store) TouchWithLoad(id string, capacity int, activeWorkers int, load float64) {
	_, _ = s.db.Exec(`UPDATE incubators SET last_heartbeat=?, status='online', capacity=?, active_workers=?, last_load=? WHERE id=?`,
		now(), capacity, activeWorkers, load, id)
}

func (s *Store) MarkStaleOffline(staleBefore time.Time) {
	_, _ = s.db.Exec(`UPDATE incubators SET status='offline' WHERE last_heartbeat < ? AND status IN ('online','overloaded')`,
		staleBefore.UTC().Format(time.RFC3339))
}

// SetIncubatorStatus 再平衡（6.4.7 rebalanceTick）：overloaded ↔ online
func (s *Store) SetIncubatorStatus(id, status string) {
	_, _ = s.db.Exec(`UPDATE incubators SET status=? WHERE id=?`, status, id)
}

func (s *Store) ListIncubators(onlineOnly bool) ([]Incubator, error) {
	q := `SELECT id,name,status,capacity,platforms,drivers,listen_port,last_ip,COALESCE(last_heartbeat,''),registered_at,
		active_workers,last_load,tags FROM incubators`
	if onlineOnly {
		q += ` WHERE status IN ('online','overloaded')`
	}
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Incubator
	for rows.Next() {
		var i Incubator
		if err := rows.Scan(&i.ID, &i.Name, &i.Status, &i.Capacity, &i.Platforms, &i.Drivers, &i.ListenPort, &i.LastIP,
			&i.LastHeartbeat, &i.RegisteredAt, &i.ActiveWorkers, &i.LastLoad, &i.Tags); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Store) GetIncubator(id string) (Incubator, error) {
	var i Incubator
	err := s.db.QueryRow(`SELECT id,name,status,capacity,platforms,drivers,listen_port,last_ip,COALESCE(last_heartbeat,''),registered_at,
		active_workers,last_load,tags FROM incubators WHERE id=?`, id).
		Scan(&i.ID, &i.Name, &i.Status, &i.Capacity, &i.Platforms, &i.Drivers, &i.ListenPort, &i.LastIP,
			&i.LastHeartbeat, &i.RegisteredAt, &i.ActiveWorkers, &i.LastLoad, &i.Tags)
	return i, err
}

// InsertDispatchLog 6.4.8：每次下发一行（重传更新同一行，msg_id 唯一）
func (s *Store) InsertDispatchLog(id, subtaskID, incubatorID, msgID, mode string) error {
	ts := now()
	_, err := s.db.Exec(`INSERT INTO dispatch_log(id,subtask_id,incubator_id,msg_id,ack_status,dispatched_at,last_sent_at,attempt_count,dispatch_mode)
		VALUES(?,?,?,?,'pending',?,?,1,?)`, id, subtaskID, incubatorID, msgID, ts, ts, mode)
	return err
}

// BumpDispatchRetransmit 重传登记（固定退避 200/500/1000ms，最多 3 次）
func (s *Store) BumpDispatchRetransmit(msgID string) {
	_, _ = s.db.Exec(`UPDATE dispatch_log SET attempt_count=attempt_count+1, last_sent_at=?, ack_status='retransmitted' WHERE msg_id=?`,
		now(), msgID)
}

// SetDispatchAck 更新下发状态。终态保护（6.4.8 首确认口径）：
//   - acked/failed 为终态，duplicate（后到 ACK）不得降级——dispatch_log 保留首确认
//   - duplicate 单独走 SetDispatchDuplicate（仅非终态时记账）
func (s *Store) SetDispatchAck(msgID, status string) {
	if status == "duplicate" {
		s.SetDispatchDuplicate(msgID)
		return
	}
	_, _ = s.db.Exec(`UPDATE dispatch_log SET ack_status=?, acked_at=? WHERE msg_id=?`, status, now(), msgID)
}

// SetDispatchDuplicate 后到 ACK 记账：不覆盖 acked/failed 终态（E16 首确认口径）
func (s *Store) SetDispatchDuplicate(msgID string) {
	_, _ = s.db.Exec(`UPDATE dispatch_log SET ack_status='duplicate'
		WHERE msg_id=? AND ack_status NOT IN ('acked','failed')`, msgID)
}

// StuckDispatchRow 塔重启后卡在下发中的行（E13 兜底）
type StuckDispatchRow struct {
	MsgID     string
	SubtaskID string
}

// ListStuckDispatchRows 6.5 ackRetransmitTick 兜底：卡在 sent/retransmitted 且超过确认窗口的行
func (s *Store) ListStuckDispatchRows(olderThan string) ([]StuckDispatchRow, error) {
	rows, err := s.db.Query(`SELECT msg_id, subtask_id FROM dispatch_log
		WHERE ack_status IN ('sent','retransmitted') AND last_sent_at <> '' AND last_sent_at < ?`, olderThan)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StuckDispatchRow
	for rows.Next() {
		var r StuckDispatchRow
		if err := rows.Scan(&r.MsgID, &r.SubtaskID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
