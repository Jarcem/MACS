// Package store 孵化器本地库（模块 #17 离线缓冲载体，SQLite）
package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

type Buffered struct {
	ID        int64
	EventType string
	Payload   string
}

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

// Migrate 顺序执行 migrations/*.sql（幂等）
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
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(string(b)); err != nil {
			return err
		}
	}
	return nil
}

// Buffer 断网时缓存上报（telemetry_events_local，synced=0）
func (s *Store) Buffer(eventType, payload string) {
	_, _ = s.db.Exec(`INSERT INTO telemetry_events_local(id,event_type,payload,synced,created_at) VALUES(?,?,?,0,?)`,
		time.Now().UnixNano(), eventType, payload, time.Now().UTC().Format(time.RFC3339))
}

// Pending 取待同步缓冲（上限 100 条/批）
func (s *Store) Pending() ([]Buffered, error) {
	rows, err := s.db.Query(`SELECT rowid,event_type,payload FROM telemetry_events_local WHERE synced=0 ORDER BY created_at LIMIT 100`)
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

func (s *Store) MarkSynced(id int64) {
	_, _ = s.db.Exec(`UPDATE telemetry_events_local SET synced=1 WHERE rowid=?`, id)
}
