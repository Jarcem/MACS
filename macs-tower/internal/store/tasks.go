// 任务/子任务访问层（M2 垂直切片）
package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewID 生成 16 位十六进制 id（任务/子任务）
func NewID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

type Task struct {
	ID        string `json:"id"`
	Goal      string `json:"goal"`
	Status    string `json:"status"`
	Progress  int    `json:"progress"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type Subtask struct {
	ID          string `json:"id"`
	TaskID      string `json:"task_id"`
	Seq         int    `json:"seq"`
	Title       string `json:"title"`
	Payload     string `json:"payload"`
	DependsOn   string `json:"depends_on"`
	Status      string `json:"status"`
	IncubatorID string `json:"incubator_id"`
	WorkerID    string `json:"worker_id"`
	Result      string `json:"result"`
	Error       string `json:"error"`
	CreatedAt   string `json:"created_at"`
	StartedAt   string `json:"started_at"`
	FinishedAt  string `json:"finished_at"`
	UpdatedAt   string `json:"updated_at"`
}

// InsertTaskWithSubtasks 一个事务内落任务与其子任务
func (s *Store) InsertTaskWithSubtasks(t Task, subs []Subtask) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO tasks(id,goal,status,progress,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		t.ID, t.Goal, t.Status, t.Progress, t.CreatedAt, t.UpdatedAt); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO subtasks(id,task_id,seq,title,payload,depends_on,status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, sub := range subs {
		if _, err := stmt.Exec(sub.ID, sub.TaskID, sub.Seq, sub.Title, sub.Payload, sub.DependsOn,
			sub.Status, sub.CreatedAt, sub.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListTasks(limit int) ([]Task, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id,goal,status,progress,created_at,updated_at FROM tasks ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Goal, &t.Status, &t.Progress, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetTask(id string) (Task, error) {
	var t Task
	err := s.db.QueryRow(`SELECT id,goal,status,progress,created_at,updated_at FROM tasks WHERE id=?`, id).
		Scan(&t.ID, &t.Goal, &t.Status, &t.Progress, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func (s *Store) ListSubtasks(taskID string) ([]Subtask, error) {
	rows, err := s.db.Query(`SELECT id,task_id,seq,title,payload,depends_on,status,incubator_id,worker_id,result,error,
		created_at,COALESCE(started_at,''),COALESCE(finished_at,''),updated_at
		FROM subtasks WHERE task_id=? ORDER BY seq`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subtask{}
	for rows.Next() {
		var x Subtask
		if err := rows.Scan(&x.ID, &x.TaskID, &x.Seq, &x.Title, &x.Payload, &x.DependsOn, &x.Status,
			&x.IncubatorID, &x.WorkerID, &x.Result, &x.Error, &x.CreatedAt, &x.StartedAt, &x.FinishedAt, &x.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) GetSubtask(id string) (Subtask, error) {
	var x Subtask
	err := s.db.QueryRow(`SELECT id,task_id,seq,title,payload,depends_on,status,incubator_id,worker_id,result,error,
		created_at,COALESCE(started_at,''),COALESCE(finished_at,''),updated_at
		FROM subtasks WHERE id=?`, id).
		Scan(&x.ID, &x.TaskID, &x.Seq, &x.Title, &x.Payload, &x.DependsOn, &x.Status,
			&x.IncubatorID, &x.WorkerID, &x.Result, &x.Error, &x.CreatedAt, &x.StartedAt, &x.FinishedAt, &x.UpdatedAt)
	return x, err
}

// ClaimSubtask 抢占分发：仅 pending 可被抢占，避免重复下发
func (s *Store) ClaimSubtask(id, incubatorID string) error {
	res, err := s.db.Exec(`UPDATE subtasks SET status='dispatched', incubator_id=?, updated_at=? WHERE id=? AND status='pending'`,
		incubatorID, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("subtask %s not pending", id)
	}
	return nil
}

// FinishSubtask 终态落库（completed/failed），同时记录执行者与开始时间
func (s *Store) FinishSubtask(id, status, result, errMsg, workerID, startedAt string) error {
	_, err := s.db.Exec(`UPDATE subtasks SET status=?, result=?, error=?, worker_id=?,
		started_at=COALESCE(NULLIF(?,''), started_at), finished_at=?, updated_at=? WHERE id=?`,
		status, result, errMsg, workerID, startedAt, now(), now(), id)
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

// RecomputeTask 按子任务终态汇总任务状态与进度
func (s *Store) RecomputeTask(taskID string) (Task, error) {
	var total, done, failed int
	err := s.db.QueryRow(`SELECT COUNT(1),
		COALESCE(SUM(CASE WHEN status='completed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0)
		FROM subtasks WHERE task_id=?`, taskID).Scan(&total, &done, &failed)
	if err != nil {
		return Task{}, err
	}

	status := "pending"
	progress := 0
	switch {
	case total == 0:
	case failed > 0:
		status = "failed"
	case done == total:
		status = "completed"
	default:
		status = "running"
	}
	if total > 0 {
		progress = done * 100 / total
	}

	_, err = s.db.Exec(`UPDATE tasks SET status=?, progress=?, updated_at=? WHERE id=?`, status, progress, now(), taskID)
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(taskID)
}

// ListReadySubtasks 待分发且依赖已满足的子任务（跨任务）
func (s *Store) ListReadySubtasks() ([]Subtask, error) {
	rows, err := s.db.Query(`SELECT id,task_id,seq,title,payload,depends_on,status,incubator_id,worker_id,result,error,
		created_at,COALESCE(started_at,''),COALESCE(finished_at,'' ),updated_at
		FROM subtasks WHERE status='pending' ORDER BY task_id, seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subtask{}
	for rows.Next() {
		var x Subtask
		if err := rows.Scan(&x.ID, &x.TaskID, &x.Seq, &x.Title, &x.Payload, &x.DependsOn, &x.Status,
			&x.IncubatorID, &x.WorkerID, &x.Result, &x.Error, &x.CreatedAt, &x.StartedAt, &x.FinishedAt, &x.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
