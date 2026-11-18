// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package connector

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"macs.local/tower/agentmsg"
)

// V1.5 Task 5：outbox 先落库后发送，重启可恢复（设计 §8）
func TestOutboxPersistsBeforeSendAndSurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "outbox.db")
	out, err := OpenOutbox(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ev := OutboxEvent{EventID: "e1", ConnectorID: "c1", EventType: "result",
		Payload: json.RawMessage(`{"status":"completed"}`)}
	if err := out.Append(ev); err != nil {
		t.Fatal(err)
	}
	out.Close()

	// 重启后恢复未确认记录
	out2, err := OpenOutbox(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer out2.Close()
	pending, err := out2.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].EventID != "e1" {
		t.Fatalf("outbox not persisted: %+v", pending)
	}

	// MarkSynced 后不再 pending
	if err := out2.MarkSynced("e1"); err != nil {
		t.Fatal(err)
	}
	pending, _ = out2.Pending()
	if len(pending) != 0 {
		t.Fatalf("marked event still pending: %+v", pending)
	}
}

// dispatch_dedup：同 idempotency_key 记录承接与结果
func TestDispatchDedupRecords(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "outbox.db")
	out, err := OpenOutbox(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	if _, _, _, ok, _ := out.LookupDispatch("k1"); ok {
		t.Fatal("unknown key must miss")
	}
	if err := out.RecordDispatch("k1", "att-1", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := out.UpdateDispatchResult("k1", "ev-1", `{"message_type":"result"}`); err != nil {
		t.Fatal(err)
	}
	att, ev, res, ok, err := out.LookupDispatch("k1")
	if err != nil || !ok || att != "att-1" || ev != "ev-1" || res == "" {
		t.Fatalf("lookup: %q %q ok=%v err=%v", att, ev, ok, err)
	}
	// 重复 RecordDispatch 不覆盖（DO NOTHING）
	if err := out.RecordDispatch("k1", "att-2", "", ""); err != nil {
		t.Fatal(err)
	}
	att2, _, _, _, _ := out.LookupDispatch("k1")
	if att2 != "att-1" {
		t.Fatalf("re-record overwrote: %s", att2)
	}
}

// 信封往返与 NewID 唯一性（协议契约冒烟）
func TestEnvelopeRoundTrip(t *testing.T) {
	env := agentmsg.Envelope{
		MessageType: agentmsg.MsgDispatch, ConnectorID: "c1", AgentID: "a1",
		InputRefs: []agentmsg.ArtifactRef{{URI: "s3://bucket/o", Digest: "sha256:" + strings.Repeat("a", 64), Kind: "commit"}},
	}
	b, err := agentmsg.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	back, err := agentmsg.Unmarshal(b)
	if err != nil || back.MessageID == "" || back.MessageType != agentmsg.MsgDispatch || len(back.InputRefs) != 1 {
		t.Fatalf("round trip: %+v err=%v", back, err)
	}
	if agentmsg.NewID() == agentmsg.NewID() {
		t.Fatal("NewID collision")
	}
}
