// Package cluster 模块 #4：孵化器集群管理
// 心跳刷新（register/report 均视为心跳）+ 60s 巡检：180s 无心跳判离线（cmdterm 同口径 §6.4.14）
package cluster

import (
	"time"

	"macs.local/tower/internal/store"
)

const staleAfter = 180 * time.Second

type Manager struct {
	store *store.Store
	stop  chan struct{}
}

func New(st *store.Store) *Manager {
	return &Manager{store: st, stop: make(chan struct{})}
}

// Touch 任意上行携带 incubator_id 时刷新心跳
func (m *Manager) Touch(incubatorID string) {
	if incubatorID != "" {
		m.store.TouchIncubator(incubatorID)
	}
}

// StartSweeper 每 60s 将超期在线孵化器置为 offline
func (m *Manager) StartSweeper() {
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				m.store.MarkStaleOffline(time.Now().Add(-staleAfter))
			case <-m.stop:
				return
			}
		}
	}()
}

func (m *Manager) Close() { close(m.stop) }

func (m *Manager) List(onlineOnly bool) ([]store.Incubator, error) {
	return m.store.ListIncubators(onlineOnly)
}
