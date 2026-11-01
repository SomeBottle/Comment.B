// Package sync 串行调度批量提交和最终一致的 Git 整合、修复与推送
package sync

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/SomeBottle/Comment.B/backend/internal/config"
	gitrepo "github.com/SomeBottle/Comment.B/backend/internal/git"
	"github.com/SomeBottle/Comment.B/backend/internal/store"
)

// Status 区分本地可用性和网络同步退化，不包含凭据或评论内容
type Status struct {
	Enabled        bool      `json:"enabled"`
	Degraded       bool      `json:"degraded"`
	Paused         bool      `json:"paused"`
	PendingCommits int       `json:"pending_commits"`
	LastFetch      time.Time `json:"last_fetch"`
	LastPush       time.Time `json:"last_push"`
	Failures       uint64    `json:"failures"`
	Conflicts      uint64    `json:"conflicts"`
	LastError      string    `json:"last_error,omitempty"`
}

// Worker 的状态和所有工作区操作均由 Store.Mu 保护，Notify 只提示调度
type Worker struct {
	Store            *store.Store
	Repo             *gitrepo.Repo
	Config           config.Config
	Notify           chan struct{}
	status           Status
	lastCommit       time.Time     // lastCommit 是最近完成本地批量提交的时间。
	commitRetryAt    time.Time     // commitRetryAt 对磁盘或 Git 索引故障单独退避，不拖延断网时的正常 Commit。
	lastFetchAttempt time.Time     // lastFetchAttempt 与 Push 分别调度，空闲节点仍会 Fetch。
	lastPushAttempt  time.Time     // lastPushAttempt 控制独立的远端发布间隔。
	retryAt          time.Time     // retryAt 仅限制网络重试，不阻止本地评论持久化。
	backoff          time.Duration // backoff 是当前含抖动退避的基础间隔。
}

// New 创建调度器，不发起网络请求
func New(s *store.Store, r *gitrepo.Repo, c config.Config) *Worker {
	return &Worker{Store: s, Repo: r, Config: c, Notify: make(chan struct{}, 1), status: Status{Enabled: c.Remote != ""}, lastCommit: time.Now()}
}

// Wake 在本地修改后非阻塞唤醒阈值检查
func (w *Worker) Wake() {
	select {
	case w.Notify <- struct{}{}:
	default:
	}
}

// StatusLocked 只复制受工作区锁保护的监控状态
func (w *Worker) StatusLocked() Status { return w.status }

// roots 验证 diff 路径并提取本次实际受影响的根目录
func roots(paths []string) ([]string, error) {
	dirs := []string{}
	for _, p := range paths {
		_, dir, ok := store.ParseCommentPath(p)
		if !ok {
			return nil, errors.New("评论仓库出现不受支持的路径或格式修改，请人工检查")
		}
		dirs = append(dirs, dir)
	}
	return dirs, nil
}

// Recover 启动时仅检查待提交路径，不进行全仓索引扫描，也不恢复回收区目录
func (w *Worker) Recover(ctx context.Context) error {
	w.Store.Mu.Lock()
	defer w.Store.Mu.Unlock()
	paths, err := w.Repo.Changed(ctx)
	if err != nil {
		return err
	}
	base, err := w.Repo.IntegrationBase()
	if err != nil {
		return err
	}
	if base != "" {
		head, err := w.Repo.Head(ctx)
		if err != nil {
			return err
		}
		integrated, err := w.Repo.Diff(ctx, base, head)
		if err != nil {
			return err
		}
		paths = append(paths, integrated...)
	}
	dirs, err := roots(paths)
	if err != nil {
		return err
	}
	if err = w.Store.RepairLocked(dirs); err != nil {
		return err
	}
	if err = w.commitLocked(ctx); err != nil {
		return err
	}
	if base != "" {
		return w.Repo.FinishIntegration()
	}
	return nil
}

// commitLocked 仅在成功提交或确认没有净变更后回收删除区
func (w *Worker) commitLocked(ctx context.Context) error {
	created, err := w.Repo.Commit(ctx)
	if err != nil {
		w.commitRetryAt = time.Now().Add(time.Duration(w.Config.RetryBackoff))
		return err
	}
	if created && w.status.Enabled {
		w.status.PendingCommits++
	}
	w.Store.Pending = 0
	w.lastCommit = time.Now()
	w.commitRetryAt = time.Time{}
	return w.Store.CleanupLocked()
}

// failLocked 将网络故障退避重试；内容冲突暂停自动同步并保留本地提交
func (w *Worker) failLocked(err error, conflict bool) {
	w.status.Degraded = true
	w.status.Failures++
	w.status.LastError = err.Error()
	if conflict {
		w.status.Paused = true
		w.status.Conflicts++
	}
	if w.backoff == 0 {
		w.backoff = time.Duration(w.Config.RetryBackoff)
	} else {
		w.backoff *= 2
	}
	if w.backoff > time.Duration(w.Config.RetryMaxBackoff) {
		w.backoff = time.Duration(w.Config.RetryMaxBackoff)
	}
	jitter, _ := rand.Int(rand.Reader, big.NewInt(int64(w.backoff/2)+1))
	delay := w.backoff
	if jitter != nil {
		delay += time.Duration(jitter.Int64())
	}
	if delay > time.Duration(w.Config.RetryMaxBackoff) {
		delay = time.Duration(w.Config.RetryMaxBackoff)
	}
	w.retryAt = time.Now().Add(delay)
	slog.Error("Git 同步退化", "error", err, "paused", w.status.Paused)
}

// Cycle 可手动触发一次完整同步；测试使用真实独立克隆验证收敛
func (w *Worker) Cycle(ctx context.Context, push bool) error {
	w.Store.Mu.Lock()
	defer w.Store.Mu.Unlock()
	if err := w.Store.CheckLocked(); err != nil {
		return err
	}
	if w.status.Paused {
		return errors.New("自动同步已暂停，请解决冲突后重启")
	}
	if err := w.commitLocked(ctx); err != nil {
		w.failLocked(err, false)
		return err
	}
	if w.Config.Remote == "" {
		return nil
	}
	return w.syncLocked(ctx, push)
}

// syncLocked 在锁内整合文件树，修复后才发布失效状态。Push 从不使用强制选项
func (w *Worker) syncLocked(ctx context.Context, push bool) (result error) {
	old, err := w.Repo.Head(ctx)
	if err != nil {
		return err
	}
	// conflict 仅在 Git 报告真实内容冲突时置位；超时等操作错误不冒充内容冲突。
	conflict := false
	defer func() {
		if result != nil {
			w.failLocked(result, conflict)
		}
	}()
	w.lastFetchAttempt = time.Now()
	ref := "refs/remotes/" + w.Config.Remote + "/" + w.Config.Branch
	if _, err = w.Repo.Run(ctx, "fetch", "--no-tags", w.Config.Remote, "refs/heads/"+w.Config.Branch+":"+ref); err != nil {
		return err
	}
	w.status.LastFetch = time.Now()
	remoteHead, err := w.Repo.Run(ctx, "rev-parse", ref)
	if err != nil {
		return err
	}
	remoteSHA := strings.TrimSpace(string(remoteHead))
	// changed 包含远端本轮涉及的路径，即使迟到文件被最终修复抵消也能检查其根。
	changed, err := w.Repo.Diff(ctx, old, remoteSHA)
	if err != nil {
		return err
	}
	dirs, err := roots(changed)
	if err != nil {
		w.status.Paused = true
		return err
	}
	contained, err := w.Repo.Ancestor(ctx, remoteSHA, old)
	if err != nil {
		return err
	}
	if !contained {
		ff, err := w.Repo.Ancestor(ctx, old, remoteSHA)
		if err != nil {
			return err
		}
		if err = w.Repo.BeginIntegration(old); err != nil {
			w.Store.Blocked = err
			return err
		}
		if ff {
			_, err = w.Repo.Run(ctx, "merge", "--ff-only", ref)
		} else {
			_, err = w.Repo.Run(ctx, "rebase", ref)
		}
		if err != nil {
			// 失败后回到原本已提交的树；abort 失败则禁止服务，绝不 reset --hard。
			if !ff {
				abortCtx, cancel := context.WithTimeout(context.Background(), w.Repo.Timeout)
				_, abortErr := w.Repo.Run(abortCtx, "rebase", "--abort")
				cancel()
				if abortErr != nil {
					w.Store.Blocked = abortErr
					w.Store.ClearCachesLocked()
				} else if finishErr := w.Repo.FinishIntegration(); finishErr != nil {
					w.Store.Blocked = finishErr
				}
			} else {
				// Fast-forward 中断可能已经改动文件；保留恢复标记并停止服务。
				w.Store.Blocked = err
				w.Store.ClearCachesLocked()
			}
			var gitErr *gitrepo.Error
			conflict = errors.As(err, &gitErr) && gitErr.Conflict
			return err
		}
	}
	// 无论修复是否成功，整合改变过的文件不能继续使用旧索引
	defer func() {
		// 请求取消也必须完成缓存协调，不能用已经取消的网络上下文跳过检查
		verifyCtx, cancel := context.WithTimeout(context.Background(), w.Repo.Timeout*2)
		defer cancel()
		newHead, e := w.Repo.Head(verifyCtx)
		if e != nil {
			w.Store.ClearCachesLocked()
			w.Store.Blocked = e
			result = e
			return
		}
		paths, e := w.Repo.Diff(verifyCtx, old, newHead)
		if e != nil {
			w.Store.ClearCachesLocked()
			w.Store.Blocked = e
			result = e
			return
		}
		w.Store.InvalidatePathsLocked(paths)
	}()
	if err = w.Store.RepairLocked(dirs); err != nil {
		w.Store.Blocked = err
		return err
	}
	if err = w.commitLocked(ctx); err != nil {
		w.Store.Blocked = err
		return err
	}
	if !contained {
		if err = w.Repo.FinishIntegration(); err != nil {
			w.Store.Blocked = err
			return err
		}
	}
	if w.status.PendingCommits, err = w.Repo.CountAhead(ctx, ref); err != nil {
		return err
	}
	if push {
		w.lastPushAttempt = time.Now()
		if w.status.PendingCommits > 0 {
			if _, err = w.Repo.Run(ctx, "push", w.Config.Remote, "HEAD:refs/heads/"+w.Config.Branch); err != nil {
				return err
			}
			w.status.PendingCommits = 0
		}
		w.status.LastPush = time.Now()
	}
	w.status.Degraded = false
	w.status.LastError = ""
	w.backoff = 0
	w.retryAt = time.Time{}
	return nil
}

// Run 独立安排 Commit、Fetch 和 Push；即使没有本地评论也定时整合远端
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.Notify:
		}
		w.Store.Mu.Lock()
		if w.Store.Blocked != nil {
			w.Store.Mu.Unlock()
			continue
		}
		now := time.Now()
		if !now.Before(w.commitRetryAt) && (w.Store.Pending >= w.Config.CommitMaxChanges || (w.Store.Pending > 0 && now.Sub(w.lastCommit) >= time.Duration(w.Config.CommitInterval))) {
			if err := w.commitLocked(ctx); err != nil {
				w.failLocked(err, false)
				w.Store.Mu.Unlock()
				continue
			}
		}
		if !w.status.Paused && w.Config.Remote != "" && !now.Before(w.retryAt) {
			push := now.Sub(w.lastPushAttempt) >= time.Duration(w.Config.PushInterval)
			fetch := now.Sub(w.lastFetchAttempt) >= time.Duration(w.Config.FetchInterval)
			if push || fetch || !w.retryAt.IsZero() {
				if err := w.commitLocked(ctx); err != nil {
					w.failLocked(err, false)
				} else {
					w.syncLocked(ctx, push || !w.retryAt.IsZero())
				}
			}
		}
		w.Store.Mu.Unlock()
	}
}

// Flush 在关闭 HTTP 后提交本地已确认修改，不依赖远端网络成功
func (w *Worker) Flush(ctx context.Context) error {
	w.Store.Mu.Lock()
	defer w.Store.Mu.Unlock()
	if err := w.Store.CheckLocked(); err != nil {
		return err
	}
	return w.commitLocked(ctx)
}
