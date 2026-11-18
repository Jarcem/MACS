// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// outbox.go V1.5 Connector 本地外发队列（设计 §8）
// 所有事件/结果先写本地 sync_outbox 再发送，塔侧 result_ack 确认后才 MarkSynced；
// 重启后恢复未确认记录重发（塔侧按 attempt_id 幂等核销）。
// dispatch_dedup 以 idempotency_key 去重重复分派：不重复执行，重发既有结果。
package connector

import (
	"database/sql"
	"encoding/json"
	"time"

	"macs.local/tower/agentmsg"

	_ "modernc.org/sqlite"
)

type OutboxEvent struct {
	EventID     string          `json:"event_id"`
	ConnectorID string          `json:"connector_id"`
	EventType   string          `json:"event_type"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   string          `json:"created_at"`
	SyncedAt    string          `json:"synced_at"`
}

type Outbox struct{ db *sql.DB }

func OpenOutbox(path string) (*Outbox, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	o := &Outbox{db: db}
	if err := o.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return o, nil
}

func (o *Outbox) migrate() error {
	_, err := o.db.Exec(`
CREATE TABLE IF NOT EXISTS sync_outbox(
	event_id TEXT PRIMARY KEY, connector_id TEXT NOT NULL DEFAULT '', event_type TEXT NOT NULL,
	payload TEXT NOT NULL, created_at TEXT NOT NULL, synced_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sync_outbox_pending ON sync_outbox(synced_at);
CREATE TABLE IF NOT EXISTS dispatch_dedup(
	idempotency_key TEXT PRIMARY KEY, attempt_id TEXT NOT NULL,
	result_event_id TEXT NOT NULL DEFAULT '', result_json TEXT NOT NULL DEFAULT '',
	received_at TEXT NOT NULL
);`)
	return err
}

func (o *Outbox) Close() error { return o.db.Close() }

func nowStr() string { return time.Now().UTC().Format(time.RFC3339) }

// Append 发送前持久化（崩溃安全：先落库后发送）
func (o *Outbox) Append(ev OutboxEvent) error {
	if ev.EventID == "" {
		ev.EventID = agentmsg.NewID()
	}
	if ev.CreatedAt == "" {
		ev.CreatedAt = nowStr()
	}
	_, err := o.db.Exec(`INSERT INTO sync_outbox(event_id,connector_id,event_type,payload,created_at)
		VALUES(?,?,?,?,?)`, ev.EventID, ev.ConnectorID, ev.EventType, string(ev.Payload), ev.CreatedAt)
	return err
}

// Pending 未确认事件（重启恢复顺序 = 落库顺序）
func (o *Outbox) Pending() ([]OutboxEvent, error) {
	rows, err := o.db.Query(`SELECT event_id,connector_id,event_type,payload,created_at,synced_at
		FROM sync_outbox WHERE synced_at='' ORDER BY created_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxEvent
	for rows.Next() {
		var e OutboxEvent
		var payload string
		if err := rows.Scan(&e.EventID, &e.ConnectorID, &e.EventType, &payload, &e.CreatedAt, &e.SyncedAt); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkSynced 塔侧确认（result_ack）后调用；只清未确认行（幂等）
func (o *Outbox) MarkSynced(eventID string) error {
	_, err := o.db.Exec(`UPDATE sync_outbox SET synced_at=? WHERE event_id=? AND synced_at=''`, nowStr(), eventID)
	return err
}

// RecordDispatch 分派去重登记：重复分派不再执行，重发既有结果
func (o *Outbox) RecordDispatch(idempotencyKey, attemptID, resultEventID, resultJSON string) error {
	_, err := o.db.Exec(`INSERT INTO dispatch_dedup(idempotency_key,attempt_id,result_event_id,result_json,received_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(idempotency_key) DO NOTHING`,
		idempotencyKey, attemptID, resultEventID, resultJSON, nowStr())
	return err
}

// LookupDispatch 查既有承接记录
func (o *Outbox) LookupDispatch(idempotencyKey string) (attemptID, resultEventID, resultJSON string, ok bool, err error) {
	err = o.db.QueryRow(`SELECT attempt_id, result_event_id, result_json FROM dispatch_dedup WHERE idempotency_key=?`,
		idempotencyKey).Scan(&attemptID, &resultEventID, &resultJSON)
	if err == sql.ErrNoRows {
		return "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", false, err
	}
	return attemptID, resultEventID, resultJSON, true, nil
}

// UpdateDispatchResult 承接记录补写结果（执行完成后）
func (o *Outbox) UpdateDispatchResult(idempotencyKey, resultEventID, resultJSON string) error {
	_, err := o.db.Exec(`UPDATE dispatch_dedup SET result_event_id=?, result_json=? WHERE idempotency_key=?`,
		resultEventID, resultJSON, idempotencyKey)
	return err
}
