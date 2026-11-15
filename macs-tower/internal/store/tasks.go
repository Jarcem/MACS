// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// 任务/子任务访问层（M2 垂直切片）
package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
)

// NewID 生成 16 位十六进制 id（任务/子任务）
func NewID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

type Task struct {
	ID          string `json:"id"`
	Goal        string `json:"goal"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Progress    int    `json:"progress"`
	Priority    string `json:"priority"`
	Acceptance  string `json:"acceptance_criteria"`  // JSON 数组：验收项（§2.2）
	AcceptNote  string `json:"acceptance_design_note"`
	DeadlineAt  string `json:"deadline_at"`
	BudgetCents float64 `json:"budget_cents"`
	CarrierIncubator string `json:"carrier_incubator"` // 会话载体亲和：首次执行的孵化器
	CarrierSession   string `json:"carrier_session"`   // 会话 id（同任务后续子 Worker 在同一 session 孵化）
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// taskCols 任务列清单（新增列追加尾部，向前兼容；与 scanTask 一一对应）
const taskCols = `id,goal,title,status,progress,priority,acceptance_criteria,acceptance_design_note,COALESCE(deadline_at,''),budget_cents,COALESCE(carrier_incubator,''),COALESCE(carrier_session,''),created_at,updated_at`

func scanTask(rows interface{ Scan(...any) error }) (Task, error) {
	var t Task
	err := rows.Scan(&t.ID, &t.Goal, &t.Title, &t.Status, &t.Progress, &t.Priority, &t.Acceptance,
		&t.AcceptNote, &t.DeadlineAt, &t.BudgetCents, &t.CarrierIncubator, &t.CarrierSession, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

type Subtask struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	Seq            int    `json:"seq"`
	Title          string `json:"title"`
	Payload        string `json:"payload"`
	DependsOn      string `json:"depends_on"`
	Status         string `json:"status"`
	IncubatorID    string `json:"incubator_id"`
	WorkerID       string `json:"worker_id"`
	Result         string `json:"result"`
	Error          string `json:"error"`
	Kind           string `json:"kind"`
	ReviewOf       string `json:"review_of"`
	AttemptCount   int    `json:"attempt_count"`
	MaxRetries     int    `json:"max_retries"`
	NextRetryAt    string `json:"next_retry_at"`
	Round          int    `json:"review_round"`
	QualityStatus  string `json:"quality_status"`
	Fingerprint    string `json:"fingerprint"`
	TemplateID     string `json:"template_id"`
	RequiredDriver string `json:"required_driver"`
	CreatedAt      string `json:"created_at"`
	StartedAt      string `json:"started_at"`
	FinishedAt     string `json:"finished_at"`
	UpdatedAt      string `json:"updated_at"`
}

// subtaskCols 子任务列清单与扫描顺序一一对应（新增列追加在尾部，向前兼容）
const subtaskCols = `id,task_id,seq,title,payload,depends_on,status,incubator_id,worker_id,result,error,
	kind,review_of,attempt_count,max_retries,next_retry_at,review_round,quality_status,fingerprint,template_id,required_driver,
	created_at,COALESCE(started_at,''),COALESCE(finished_at,''),updated_at`

func scanSubtask(rows interface{ Scan(...any) error }) (Subtask, error) {
	var x Subtask
	err := rows.Scan(&x.ID, &x.TaskID, &x.Seq, &x.Title, &x.Payload, &x.DependsOn, &x.Status,
		&x.IncubatorID, &x.WorkerID, &x.Result, &x.Error,
		&x.Kind, &x.ReviewOf, &x.AttemptCount, &x.MaxRetries, &x.NextRetryAt, &x.Round, &x.QualityStatus,
		&x.Fingerprint, &x.TemplateID, &x.RequiredDriver,
		&x.CreatedAt, &x.StartedAt, &x.FinishedAt, &x.UpdatedAt)
	return x, err
}

// ValidateDag E10 依赖环检测（通用 DFS，创建时拒绝带环任务）
// subs 的 depends_on 为逗号分隔子任务 ID（解析为邻接表后着色遍历）
func ValidateDag(subs []Subtask) error {
	adj := map[string][]string{}
	ids := map[string]bool{}
	for _, s := range subs {
		ids[s.ID] = true
		if s.DependsOn == "" {
			continue
		}
		for _, d := range strings.Split(s.DependsOn, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				adj[s.ID] = append(adj[s.ID], d)
			}
		}
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		switch color[id] {
		case gray:
			return fmt.Errorf("dependency cycle detected at subtask %s (E10)", id)
		case black:
			return nil
		}
		color[id] = gray
		for _, dep := range adj[id] {
			if !ids[dep] {
				continue
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		color[id] = black
		return nil
	}
	for id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

// InsertTaskWithSubtasks 一个事务内落任务与其子任务，并规范化写入
// subtask_dependencies（uk 防重）与 parallel_groups（依赖层数分组，§2.3/§2.4）
func (s *Store) InsertTaskWithSubtasks(t Task, subs []Subtask) error {
	if err := ValidateDag(subs); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO tasks(id,goal,title,status,progress,priority,acceptance_criteria,acceptance_design_note,deadline_at,budget_cents,carrier_incubator,carrier_session,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Goal, t.Title, t.Status, t.Progress, t.Priority, t.Acceptance, t.AcceptNote, t.DeadlineAt,
		t.BudgetCents, t.CarrierIncubator, t.CarrierSession, t.CreatedAt, t.UpdatedAt); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO subtasks(id,task_id,seq,title,payload,depends_on,status,kind,review_of,attempt_count,
		max_retries,review_round,quality_status,fingerprint,template_id,required_driver,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	depStmt, err := tx.Prepare(`INSERT OR IGNORE INTO subtask_dependencies(subtask_id,depends_on_id,created_at) VALUES(?,?,?)`)
	if err != nil {
		return err
	}
	grpStmt, err := tx.Prepare(`INSERT OR IGNORE INTO parallel_groups(id,task_id,depth,subtask_id,created_at) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	defer depStmt.Close()
	defer grpStmt.Close()
	for _, sub := range subs {
		if _, err := stmt.Exec(sub.ID, sub.TaskID, sub.Seq, sub.Title, sub.Payload, sub.DependsOn,
			sub.Status, sub.Kind, sub.ReviewOf, sub.AttemptCount, sub.MaxRetries, sub.Round,
			sub.QualityStatus, sub.Fingerprint, sub.TemplateID, sub.RequiredDriver, sub.CreatedAt, sub.UpdatedAt); err != nil {
			return err
		}
		if sub.DependsOn != "" {
			for _, d := range strings.Split(sub.DependsOn, ",") {
				d = strings.TrimSpace(d)
				if d == "" {
					continue
				}
				if _, err := depStmt.Exec(sub.ID, d, sub.CreatedAt); err != nil {
					return err
				}
			}
		}
	}
	// 依赖层数（并行组）：depth = 1 + max(depth(deps))，同层可并行（§2.4）
	depth := map[string]int{}
	for changed := true; changed; {
		changed = false
		for _, sub := range subs {
			if sub.DependsOn == "" {
				continue
			}
			maxDep := 0
			for _, d := range strings.Split(sub.DependsOn, ",") {
				d = strings.TrimSpace(d)
				if d != "" && depth[d] > maxDep {
					maxDep = depth[d]
				}
			}
			if nd := maxDep + 1; nd > depth[sub.ID] {
				depth[sub.ID] = nd
				changed = true
			}
		}
	}
	for _, sub := range subs {
		if _, err := grpStmt.Exec(NewID(), sub.TaskID, depth[sub.ID], sub.ID, sub.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListTasksFiltered V3.3 查询参数：status 多值 / from / to / q 标题模糊 / 分页 / 排序
func (s *Store) ListTasksFiltered(q TaskQuery) ([]Task, int, error) {
	where := "WHERE 1=1"
	args := []any{}
	if len(q.Statuses) > 0 {
		where += ` AND status IN (` + placeholders(len(q.Statuses)) + `)`
		for _, st := range q.Statuses {
			args = append(args, st)
		}
	}
	if q.From != "" {
		where += ` AND created_at >= ?`
		args = append(args, q.From)
	}
	if q.To != "" {
		where += ` AND created_at <= ?`
		args = append(args, q.To)
	}
	if q.Q != "" {
		where += ` AND (goal LIKE ? OR title LIKE ?)`
		args = append(args, "%"+q.Q+"%", "%"+q.Q+"%")
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM tasks `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	sortCol := "updated_at DESC"
	if q.Sort == "created_at" {
		sortCol = "created_at DESC"
	}
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	rows, err := s.db.Query(`SELECT `+taskCols+` FROM tasks `+
		where+` ORDER BY `+sortCol+` LIMIT ? OFFSET ?`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// placeholders 生成 n 个 '?' 占位符（SQLite/PG 双兼容）
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// TaskQuery 任务列表过滤条件（附录 B.1 V3.3）
type TaskQuery struct {
	Statuses []string
	From, To string
	Q        string
	Limit    int
	Offset   int
	Sort     string
}

func (s *Store) ListTasks(limit int) ([]Task, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	ts, _, err := s.ListTasksFiltered(TaskQuery{Limit: limit})
	return ts, err
}

func (s *Store) GetTask(id string) (Task, error) {
	row := s.db.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id=?`, id)
	return scanTask(row)
}

// CountSubtaskDependencies 依赖规范化行数（诊断/测试）
func (s *Store) CountSubtaskDependencies() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM subtask_dependencies`).Scan(&n)
	return n, err
}

// CountParallelGroups 并行组行数（诊断/测试）
func (s *Store) CountParallelGroups() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM parallel_groups`).Scan(&n)
	return n, err
}

// CountIncubatorResources 资源快照行数（诊断/测试）
func (s *Store) CountIncubatorResources() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM incubator_resources`).Scan(&n)
	return n, err
}

// SetTaskCarrier 会话载体登记（幂等：仅首次生效；载体锁定后同任务子 Worker 在同一 session 孵化）
func (s *Store) SetTaskCarrier(taskID, incubatorID, sessionID string) error {
	_, err := s.db.Exec(`UPDATE tasks SET carrier_incubator=?, carrier_session=?
		WHERE id=? AND (carrier_incubator='' OR carrier_incubator IS NULL)`,
		incubatorID, sessionID, taskID)
	return err
}

// SetTaskStatus 直接推进任务状态（blocked/cancelled 等非聚合态）
func (s *Store) SetTaskStatus(id, status string) error {
	_, err := s.db.Exec(`UPDATE tasks SET status=?, updated_at=? WHERE id=?`, status, now(), id)
	return err
}

func (s *Store) ListSubtasks(taskID string) ([]Subtask, error) {
	rows, err := s.db.Query(`SELECT `+subtaskCols+` FROM subtasks WHERE task_id=? ORDER BY seq`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subtask{}
	for rows.Next() {
		x, err := scanSubtask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) GetSubtask(id string) (Subtask, error) {
	row := s.db.QueryRow(`SELECT `+subtaskCols+` FROM subtasks WHERE id=?`, id)
	return scanSubtask(row)
}

// LeaseTimeoutFor 租约时长（M2.6 §6.4.3.1，V3.5.10 mode 感知口径）：
//   普通子任务       base = timeout_ms>0 ? timeout_ms : 120s；ceiling 10min；grace 30s
//   session_agent    base = timeout_ms>0 ? timeout_ms : 30min；ceiling 120min；grace 120s
//   （长会话若沿用 10min 上限会被 Watchdog 误判到期并重投，造成同 session 重复执行）
func LeaseTimeoutFor(mode string, timeoutMs int64) time.Duration {
	base := time.Duration(timeoutMs) * time.Millisecond
	ceiling, grace := 10*time.Minute, 30*time.Second
	if mode == "session_agent" {
		if base <= 0 {
			base = 30 * time.Minute
		}
		ceiling, grace = 120*time.Minute, 120*time.Second
	} else if base <= 0 {
		base = 120 * time.Second
	}
	if base > ceiling {
		base = ceiling
	}
	return base + grace
}

// ClaimSubtaskLease 原子抢占（M2.6 §6.4.3.1 唯一口径）：单条 UPDATE 完成状态推进、
// attempt 递增、租约设置，并清空上一轮执行产物（旧结果不得误读）。
// 返回本次 attempt_count（dispatch 载荷必须原样带回，孵化器结果按 attempt 条件核销）。
func (s *Store) ClaimSubtaskLease(id, incubatorID string, leaseTimeout time.Duration) (int, error) {
	lease := time.Now().UTC().Add(leaseTimeout).Format(time.RFC3339)
	res, err := s.db.Exec(`UPDATE subtasks
		SET status='dispatched', incubator_id=?, attempt_count=attempt_count+1,
		    lease_expires_at=?, worker_id='', result='', error='',
		    started_at=NULL, finished_at=NULL, updated_at=?
		WHERE id=? AND status='pending'`,
		incubatorID, lease, now(), id)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, fmt.Errorf("subtask %s not pending", id)
	}
	var attempt int
	if err := s.db.QueryRow(`SELECT attempt_count FROM subtasks WHERE id=?`, id).Scan(&attempt); err != nil {
		return 0, err
	}
	return attempt, nil
}

// RecoveredLease 租约回收结果
type RecoveredLease struct {
	SubtaskID string
	TaskID    string
	Action    string // retry / failed
	Attempts  int
}

// LeaseBackoff 租约到期失败后的重试退避（V3.5.10 唯一记账口径）：
// backoff(n) = min(2^(n-1) × 5s, 60s)，n 为已执行 attempt 数
func LeaseBackoff(attempt int) time.Duration {
	d := (1 << uint(attempt-1)) * 5 * time.Second
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	return d
}

// RecoverExpiredLeases 确定性租约回收（M2.6 §6.4.3.1，编排器每 5s 调用；now 由调用方注入，
// 禁止在回收算法内读取系统时间，保证单测可精确重放到期边界）。
// V3.5.10 唯一记账口径：attempt_count 仅由 ClaimSubtask 递增；到期**复用 E01 的
// failed + next_retry_at 退避**（不直接置 pending）——
//   attempt_count < max_retries → failed + next_retry_at = now + backoff(attempt)（RequeueRetryable 到期回池）
//   attempt_count >= max_retries → failed（E01 重试耗尽，无 next_retry_at）
// 回收后调用方必须 RecomputeTask。
func (s *Store) RecoverExpiredLeases(nowTS string) ([]RecoveredLease, error) {
	var out []RecoveredLease
	rows, err := s.db.Query(`SELECT id, task_id, attempt_count FROM subtasks
		WHERE status IN ('dispatched','running') AND lease_expires_at <> '' AND lease_expires_at <= ?`, nowTS)
	if err != nil {
		return nil, err
	}
	type expired struct {
		id, tid string
		att     int
	}
	var expiredList []expired
	taskSet := map[string]bool{}
	for rows.Next() {
		var e expired
		if err := rows.Scan(&e.id, &e.tid, &e.att); err != nil {
			rows.Close()
			return nil, err
		}
		expiredList = append(expiredList, e)
		taskSet[e.tid] = true
	}
	rows.Close()
	for _, e := range expiredList {
		var action string
		// max_retries 含首发：attempt < max_retries → 有重试余量（failed + next_retry_at 退避）
		var maxRetries int
		if err := s.db.QueryRow(`SELECT max_retries FROM subtasks WHERE id=?`, e.id).Scan(&maxRetries); err != nil {
			return nil, err
		}
		if e.att < maxRetries {
			action = "retry"
			next := time.Now().UTC().Add(LeaseBackoff(e.att)).Format(time.RFC3339)
			if _, err := s.db.Exec(`UPDATE subtasks SET status='failed',
				error='lease expired (will retry)', lease_expires_at='', next_retry_at=?, finished_at=NULL, updated_at=?
				WHERE id=?`, next, nowTS, e.id); err != nil {
				return nil, err
			}
		} else {
			action = "failed"
			if _, err := s.db.Exec(`UPDATE subtasks SET status='failed',
				error='lease expired after N attempts', lease_expires_at='', updated_at=?
				WHERE id=?`, nowTS, e.id); err != nil {
				return nil, err
			}
		}
		out = append(out, RecoveredLease{SubtaskID: e.id, TaskID: e.tid, Action: action, Attempts: e.att})
	}
	for tid := range taskSet {
		if _, err := s.RecomputeTask(tid); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// FinishSubtaskAttempt 执行结果条件核销（M2.6 §6.4.3.2 终态保护唯一口径）：
// 仅受理在途且 attempt/incubator 匹配的结果；RowsAffected=0 表示
// 重复结果 / 旧 attempt / 错误孵化器 / 已终态（cancelled 等）→ 统一 stale 忽略，
// 不得改写结果、推进进度或重复落 cost/telemetry 副作用（E07/E17）。
// 返回 changed=true 才允许产生下游副作用。
func (s *Store) FinishSubtaskAttempt(id, status, result, errMsg, workerID, startedAt, fingerprint, incubatorID string, attempt int) (bool, error) {
	if status != "completed" && status != "failed" {
		return false, fmt.Errorf("illegal terminal status %q (only completed/failed)", status)
	}
	res, err := s.db.Exec(`UPDATE subtasks
		SET status=?, result=?, error=?, worker_id=?, fingerprint=?,
		    started_at=COALESCE(NULLIF(?,''), started_at), finished_at=?, updated_at=?
		WHERE id=?
		  AND status IN ('dispatched','running')
		  AND attempt_count=?
		  AND incubator_id=?
		  AND attempt_count>0`,
		status, result, errMsg, workerID, fingerprint, startedAt, now(), now(),
		id, attempt, incubatorID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// FinishReviewAttempt 复核子任务条件核销（V3.5.10 §6.4.3.2 per-kind 守卫）：
//   来源状态 dispatched/running/pending（pending 兼容广播双跑中未 claim 的合法回传）；
//   attempt 门禁 attempt_count=?；pending 来源要求 attempt=0 并收口补记为 1；
//   incubator 为空或与回传方一致。
// 返回 changed=true 才允许产生下游副作用。
func (s *Store) FinishReviewAttempt(id, status, result, errMsg, workerID, startedAt, fingerprint, incubatorID string, attempt int) (bool, error) {
	if status != "completed" && status != "failed" {
		return false, fmt.Errorf("illegal terminal status %q (only completed/failed)", status)
	}
	res, err := s.db.Exec(`UPDATE subtasks
		SET status=?, result=?, error=?, worker_id=?, fingerprint=?,
		    started_at=COALESCE(NULLIF(?,''), started_at), finished_at=?, updated_at=?,
		    attempt_count=CASE WHEN attempt_count=0 THEN 1 ELSE attempt_count END
		WHERE id=?
		  AND status IN ('dispatched','running','pending')
		  AND attempt_count=?
		  AND (incubator_id IS NULL OR incubator_id='' OR incubator_id=?)
		  AND attempt_count>=0`,
		status, result, errMsg, workerID, fingerprint, startedAt, now(), now(),
		id, attempt, incubatorID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ForceCancelSubtasks 强制取消（M2.6 §6.4.3.3）：任务级取消是强制取消，
// 全部在途/待执行子任务置 cancelled（旧 fail-open 口径不适用于用户显式取消）。
// 返回取消前处于 dispatched/running 的在途清单（{id, incubator, attempt}），
// 供塔侧事务提交后单播 task_cancelled。
func (s *Store) ForceCancelSubtasks(taskID string) ([]struct {
	SubtaskID   string
	IncubatorID string
	Attempt     int
}, error) {
	rows, err := s.db.Query(`SELECT id, COALESCE(incubator_id,''), attempt_count FROM subtasks
		WHERE task_id=? AND status IN ('dispatched','running')`, taskID)
	if err != nil {
		return nil, err
	}
	var inflight []struct {
		SubtaskID   string
		IncubatorID string
		Attempt     int
	}
	for rows.Next() {
		var id, inc string
		var att int
		if err := rows.Scan(&id, &inc, &att); err != nil {
			rows.Close()
			return nil, err
		}
		inflight = append(inflight, struct {
			SubtaskID   string
			IncubatorID string
			Attempt     int
		}{id, inc, att})
	}
	rows.Close()
	_, err = s.db.Exec(`UPDATE subtasks SET status='cancelled', quality_status='', lease_expires_at='', updated_at=?
		WHERE task_id=? AND status IN ('pending','ready','frozen','dispatched','running','review_pending','reviewing','in_spot_check')`,
		now(), taskID)
	if err != nil {
		return nil, err
	}
	return inflight, nil
}

// FinishSubtask 终态落库（completed/failed），同时记录执行者、开始时间与产物指纹
func (s *Store) FinishSubtask(id, status, result, errMsg, workerID, startedAt, fingerprint string) error {
	_, err := s.db.Exec(`UPDATE subtasks SET status=?, result=?, error=?, worker_id=?, fingerprint=?,
		started_at=COALESCE(NULLIF(?,''), started_at), finished_at=?, updated_at=? WHERE id=?`,
		status, result, errMsg, workerID, fingerprint, startedAt, now(), now(), id)
	return err
}

// SetSubtaskFingerprint 产物指纹落库（条件核销后塔侧权威回填，6.4.4）
func (s *Store) SetSubtaskFingerprint(id, fp string) error {
	_, err := s.db.Exec(`UPDATE subtasks SET fingerprint=?, updated_at=? WHERE id=?`, fp, now(), id)
	return err
}

// SetSubtaskStatus 质量管线中间态推进（review_pending/reviewing/in_spot_check 等），附合法性校验由调用方负责
// SetSubtaskStatus 质量管线中间态推进（V3.5.10 状态门：status <> 'cancelled'）——
// 取消后的子任务不得被质量管线回滚成 pending 重新调度（E17 口径）
func (s *Store) SetSubtaskStatus(id, status, quality string) error {
	res, err := s.db.Exec(`UPDATE subtasks SET status=?, quality_status=?, updated_at=? WHERE id=? AND status <> 'cancelled'`,
		status, quality, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("subtask %s is cancelled (guard)", id)
	}
	return nil
}

// SetSubtaskRetry E01 失败重试登记：累计次数 + 退避到期时刻
func (s *Store) SetSubtaskRetry(id string, attempt, maxRetries int, nextRetryAt string) error {
	res, err := s.db.Exec(`UPDATE subtasks SET attempt_count=?, max_retries=?, next_retry_at=?, updated_at=?
		WHERE id=? AND status <> 'cancelled'`, attempt, maxRetries, nextRetryAt, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("subtask %s is cancelled (retry guard)", id)
	}
	return nil
}

// RequeueRetryable 将退避到期且仍有重试额度的失败子任务重新置为 pending（E01）
func (s *Store) RequeueRetryable() (int64, error) {
	// V3.5.10：回池时清空上一轮载体与产物（§6.4.3.1 retry 口径）
	res, err := s.db.Exec(`UPDATE subtasks SET status='pending', next_retry_at='', incubator_id='', worker_id='',
		result='', error='', updated_at=?
		WHERE status='failed' AND status <> 'cancelled' AND attempt_count < max_retries AND next_retry_at <> '' AND next_retry_at <= ?`,
		now(), now())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListStaleRunning Worker 超时清理（6.5）：dispatched/running 超过 TTL 的子任务
func (s *Store) ListStaleRunning(ttlBefore string) ([]Subtask, error) {
	rows, err := s.db.Query(`SELECT `+subtaskCols+` FROM subtasks
		WHERE status IN ('dispatched','running') AND started_at <> '' AND started_at < ?`, ttlBefore)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subtask{}
	for rows.Next() {
		x, err := scanSubtask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ListSubtasksByReviewOf 复核幂等护栏：同一原子任务的未完结复核子任务
func (s *Store) ListSubtasksByReviewOf(id string) ([]Subtask, error) {
	rows, err := s.db.Query(`SELECT `+subtaskCols+` FROM subtasks WHERE review_of=? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subtask{}
	for rows.Next() {
		x, err := scanSubtask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// InsertSubtaskStandalone 质量管线追加子任务（复核/抽查补挂，任务事务外单条落库）
func (s *Store) InsertSubtaskStandalone(sub Subtask) error {
	_, err := s.db.Exec(`INSERT INTO subtasks(id,task_id,seq,title,payload,depends_on,status,kind,review_of,attempt_count,
		max_retries,review_round,quality_status,fingerprint,template_id,required_driver,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sub.ID, sub.TaskID, sub.Seq, sub.Title, sub.Payload, sub.DependsOn, sub.Status, sub.Kind, sub.ReviewOf,
		sub.AttemptCount, sub.MaxRetries, sub.Round, sub.QualityStatus, sub.Fingerprint, sub.TemplateID,
		sub.RequiredDriver, sub.CreatedAt, sub.UpdatedAt)
	return err
}

// SetSubtaskReviewRound E04 复核打回轮次推进
func (s *Store) SetSubtaskReviewRound(id string, round int) error {
	_, err := s.db.Exec(`UPDATE subtasks SET review_round=?, updated_at=? WHERE id=?`, round, now(), id)
	return err
}

// CountRunning 每台孵化器当前占用槽位（dispatched+running）
func (s *Store) CountRunning() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT incubator_id, COUNT(1) FROM subtasks
		WHERE status IN ('dispatched','running') AND incubator_id<>'' GROUP BY incubator_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// RecomputeTask 按子任务终态汇总任务状态与进度（进度可推导：不靠 Worker 自报百分比）
// 进度合成公式（UI 设计 §16.1，wᵢ=1）：completed=1 / self_checking·review_pending·reviewing=0.9 /
// in_spot_check=1（复核已过）/ 其余=0；无 telemetry 细粒度数据时即「按完成数」诚实口径的加权细化。
// failed 聚合：存在失败且无待执行余量 → partial_failure（附录 B.4）；仍有可重试/待执行 → running
func (s *Store) RecomputeTask(taskID string) (Task, error) {
	var total, done, failed, pending int
	var nearDone float64 // self_checking/review_pending/reviewing/in_spot_check 加权 0.9（in_spot_check 按复核已过计 1）
	err := s.db.QueryRow(`SELECT COUNT(1),
		COALESCE(SUM(CASE WHEN status='completed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='failed' AND next_retry_at='' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status IN ('pending','ready','dispatched','running','frozen','cancelled') OR (status='failed' AND next_retry_at<>'') THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status IN ('self_checking','review_pending','reviewing') THEN 0.9 WHEN status='in_spot_check' THEN 1 ELSE 0 END),0)
		FROM subtasks WHERE task_id=?`, taskID).Scan(&total, &done, &failed, &pending, &nearDone)
	if err != nil {
		return Task{}, err
	}

	status := "pending"
	progress := 0
	switch {
	case total == 0:
	case pending > 0:
		status = "running"
	case done == total:
		status = "completed"
	case failed == total:
		status = "failed"
	case failed > 0:
		status = "partial_failure"
	}
	if total > 0 {
		// §16.1 加权合成：round(100 × Σ(wᵢ·pᵢ) / Σwᵢ)，wᵢ=1
		progress = int(math.Round(100 * (float64(done) + nearDone) / float64(total)))
	}

	// blocked/cancelled 由审批/取消语义显式管理，聚合不覆盖
	if cur, _ := s.GetTask(taskID); cur.Status == "blocked" || cur.Status == "cancelled" {
		if status == "completed" || status == "partial_failure" {
			_, _ = s.db.Exec(`UPDATE tasks SET status=?, progress=?, updated_at=? WHERE id=?`, status, progress, now(), taskID)
		}
		return cur, nil
	}
	_, err = s.db.Exec(`UPDATE tasks SET status=?, progress=?, updated_at=? WHERE id=?`, status, progress, now(), taskID)
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(taskID)
}

// ListReadySubtasks 待分发且依赖已满足的子任务（跨任务，含复核子任务）
func (s *Store) ListReadySubtasks() ([]Subtask, error) {
	rows, err := s.db.Query(`SELECT `+subtaskCols+` FROM subtasks WHERE status='pending' ORDER BY task_id, seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subtask{}
	for rows.Next() {
		x, err := scanSubtask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
