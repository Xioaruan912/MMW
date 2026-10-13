package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/mmx-project/mmx/internal/idgen"
)

// 任务状态。
const (
	TaskPending    = "pending"    // 已生成，尚未投递
	TaskDispatched = "dispatched" // 已投递给 Agent，等待回执
	TaskOK         = "ok"
	TaskFailed     = "failed"
	TaskTimeout    = "timeout"
	TaskCancelled  = "cancelled"
)

// PendingTask 是一条持久化的任务记录。
//
// 为什么必须落库：任务原先只存在于内存 map 中，主控重启 / 崩溃会让在途任务静默消失，
// 且 Pull（HTTP 降级）通道根本无从取任务。落库后：
//   - 重启可恢复（未完成的重新派发或标记超时）
//   - Pull 模式与 WS 模式能力对齐
//   - 有完整的历史与回执可查
type PendingTask struct {
	ID           string `json:"id"`
	ServerID     string `json:"server_id"`
	Kind         string `json:"kind"`
	Args         string `json:"args"`
	Nonce        string `json:"nonce"`
	Signature    string `json:"signature"`
	DeadlineMS   int64  `json:"deadline_ms"`
	Actor        string `json:"actor"`
	Status       string `json:"status"`
	Attempts     int    `json:"attempts"`
	CreatedAt    int64  `json:"created_at"`
	DispatchedAt int64  `json:"dispatched_at"`
	FinishedAt   int64  `json:"finished_at"`
	Output       string `json:"output"`
	Error        string `json:"error"`
}

const taskCols = `id,server_id,kind,args,nonce,signature,deadline_ms,actor,status,attempts,
                  created_at,dispatched_at,finished_at,output,error`

func scanTask(sc interface{ Scan(...any) error }) (PendingTask, error) {
	var t PendingTask
	err := sc.Scan(&t.ID, &t.ServerID, &t.Kind, &t.Args, &t.Nonce, &t.Signature,
		&t.DeadlineMS, &t.Actor, &t.Status, &t.Attempts,
		&t.CreatedAt, &t.DispatchedAt, &t.FinishedAt, &t.Output, &t.Error)
	return t, err
}

// CreateTask 落库一条待派发任务。
func (s *Store) CreateTask(ctx context.Context, t PendingTask) error {
	if t.ID == "" {
		t.ID = idgen.New(idgen.PrefixTask)
	}
	t.Status = TaskPending
	t.CreatedAt = nowMS()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pending_tasks(`+taskCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.ServerID, t.Kind, t.Args, t.Nonce, t.Signature, t.DeadlineMS, t.Actor,
		t.Status, 0, t.CreatedAt, 0, 0, "", "")
	return err
}

// MarkTaskDispatched 标记任务已投递。
func (s *Store) MarkTaskDispatched(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE pending_tasks SET status=?, dispatched_at=?, attempts=attempts+1 WHERE id=?`,
		TaskDispatched, nowMS(), id)
	return err
}

// FinishTask 写入任务最终结果。
func (s *Store) FinishTask(ctx context.Context, id, status, output, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE pending_tasks SET status=?, finished_at=?, output=?, error=? WHERE id=?`,
		status, nowMS(), truncateStr(output, 8192), truncateStr(errMsg, 2048), id)
	return err
}

// GetTask 取任务。
func (s *Store) GetTask(ctx context.Context, id string) (PendingTask, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+taskCols+` FROM pending_tasks WHERE id=?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PendingTask{}, ErrNotFound
	}
	return t, err
}

// ClaimPendingTasks 取出某台服务器待投递的任务并标记为已派发。
//
// 供 Pull（HTTP 降级）通道使用：Agent 每轮拉取时把队列里的任务一并取走。
// 超过 ttl 仍未投递的旧任务会被跳过（避免 Agent 长时间离线后一次性灌入大量陈旧指令）。
func (s *Store) ClaimPendingTasks(ctx context.Context, serverID string, limit int, ttl time.Duration) ([]PendingTask, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	cutoff := time.Now().Add(-ttl).UnixMilli()

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskCols+` FROM pending_tasks
          WHERE server_id=? AND status=? AND created_at >= ?
          ORDER BY created_at ASC LIMIT ?`,
		serverID, TaskPending, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PendingTask
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.MarkTaskDispatched(ctx, out[i].ID); err != nil {
			return nil, err
		}
		out[i].Status = TaskDispatched
	}
	return out, nil
}

// ExpireStaleTasks 把超期未完成的任务标记为超时，返回受影响数量。
//
// 主控重启、Agent 掉线、回执丢失都会留下永久的 dispatched 状态。
func (s *Store) ExpireStaleTasks(ctx context.Context) (int64, error) {
	// deadline_ms 是任务自身的超时；再留 3 倍余量给网络与排队
	cutoff := nowMS() - 10*60*1000
	res, err := s.db.ExecContext(ctx,
		`UPDATE pending_tasks SET status=?, finished_at=?, error=?
          WHERE status IN (?,?) AND dispatched_at != 0 AND dispatched_at < ?`,
		TaskTimeout, nowMS(), "任务超时未收到回执（Agent 离线或已重启）",
		TaskDispatched, TaskPending, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ListTasks 列出任务历史。
func (s *Store) ListTasks(ctx context.Context, serverID string, limit int) ([]PendingTask, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT ` + taskCols + ` FROM pending_tasks`
	args := []any{}
	if serverID != "" {
		q += ` WHERE server_id=?`
		args = append(args, serverID)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingTask
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PruneTasks 清理旧任务记录。
func (s *Store) PruneTasks(ctx context.Context, olderThan time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM pending_tasks WHERE created_at < ?`,
		time.Now().Add(-olderThan).UnixMilli())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(截断)"
}
