// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package store

import (
	"path/filepath"
	"testing"
)

// §12.1 幂等测试：dispatch_inbox 同 msg_id 重复登记 ×100 仅首条生效（6.2.4 重复下发不重复执行）
func TestDispatchInboxDedup(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "inc.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate("../../src/db/migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for i := 0; i < 100; i++ {
		dup, err := st.RecordDispatch("same-msg-id", "dispatch", "{}")
		if err != nil {
			t.Fatalf("record: %v", err)
		}
		if i == 0 && dup {
			t.Fatal("首次登记不应判定重复")
		}
		if i > 0 && !dup {
			t.Fatalf("第 %d 次重复登记应判定 dup", i+1)
		}
	}
}

// 6.4.5 离线缓冲：指数退避与 14 天清理（Q6）
func TestOfflineBufferBackoffAndPurge(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "inc.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate("../../src/db/migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st.Buffer("execution_result", "{}")
	due, _ := st.DuePending()
	if len(due) != 1 {
		t.Fatalf("新缓冲应立即到期，got %d", len(due))
	}
	// 补传失败 → 退避 2^0=1 分钟
	st.MarkFailed(due[0].ID)
	due2, _ := st.DuePending()
	if len(due2) != 0 {
		t.Fatal("退避期内不应到期")
	}
	// 模拟退避到期
	if _, err := st.Exec(`UPDATE telemetry_events_local SET next_retry_at=0`); err != nil {
		t.Fatal(err)
	}
	due3, _ := st.DuePending()
	if len(due3) != 1 {
		t.Fatal("到期后应可补传")
	}
	st.MarkSynced(due3[0].ID)
	st.PurgeExpired() // 14 天内不清
	due4, _ := st.DuePending()
	if len(due4) != 0 {
		t.Fatal("已同步不再补传")
	}
	// 14 天前的已同步行应被清理
	if _, err := st.Exec(`UPDATE telemetry_events_local SET created_at='2020-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	st.PurgeExpired()
	n, err := st.CountBuffered()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("14 天前已同步行应被清理，got %d", n)
	}
}
