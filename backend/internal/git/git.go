// Package git 通过带超时的原生 Git 执行固定操作，不经过 Shell
package git

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Error 区分退出码、超时与内容冲突；原始输出不进入日志，避免泄露远端凭据
type Error struct {
	Op                string
	Code              int
	Timeout, Conflict bool
	output            string
}

// Error 返回可公开记录的脱敏摘要
func (e *Error) Error() string {
	if e.Timeout {
		return "git " + e.Op + ": 超时或取消"
	}
	return fmt.Sprintf("git %s: 退出码 %d", e.Op, e.Code)
}

// Repo 保存工作目录、命令超时和机器人提交身份
type Repo struct {
	Dir                     string
	Timeout                 time.Duration
	AuthorName, AuthorEmail string
}

// Run 执行独立参数，并禁用交互、自动签名和 hooks，保持后台执行可预测
func (r *Repo) Run(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	op := "command"
	if len(args) > 0 {
		op = args[0]
	}
	base := []string{"-c", "user.name=" + r.AuthorName, "-c", "user.email=" + r.AuthorEmail, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "-c", "core.autocrlf=false", "-c", "rebase.autoStash=false"}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		e := &Error{Op: op, Code: -1, Timeout: ctx.Err() != nil, output: string(out)}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			e.Code = exit.ExitCode()
		}
		e.Conflict = strings.Contains(e.output, "CONFLICT")
		return out, e
	}
	return out, nil
}

// ExitCode 判断已知的非零退出状态，不把命令故障当作普通差异
func ExitCode(err error, code int) bool {
	var e *Error
	return errors.As(err, &e) && !e.Timeout && e.Code == code
}

// Init 显式创建新的空数据仓库，已存在 format 或 .git 时拒绝覆盖
func (r *Repo) Init(ctx context.Context, branch string) error {
	if err := os.MkdirAll(r.Dir, 0700); err != nil {
		return err
	}
	for _, p := range []string{".git", "format.json", "comments"} {
		if _, err := os.Lstat(filepath.Join(r.Dir, p)); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("初始化要求空数据目录: %s", p)
		}
	}
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("初始化要求空数据目录")
	}
	if _, err = r.Run(ctx, "init", "--initial-branch="+branch); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(r.Dir, "format.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.WriteString("{\"format_version\":1}\n"); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	d, err := os.Open(r.Dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	d.Close()
	if err != nil {
		return err
	}
	if _, err = r.Run(ctx, "add", "--", "format.json"); err != nil {
		return err
	}
	_, err = r.Run(ctx, "commit", "-m", "Initialize Comment.B format v1")
	return err
}

// Validate 要求独立克隆、预期分支和完整 HEAD，未完成操作保留给管理员处理
func (r *Repo) Validate(ctx context.Context, branch string) error {
	info, err := os.Lstat(filepath.Join(r.Dir, ".git"))
	if err != nil || !info.IsDir() {
		return errors.New("data_dir 必须是独立 Git 克隆，不支持嵌套仓库或 worktree")
	}
	out, err := r.Run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	want, err := filepath.EvalSymlinks(r.Dir)
	if err != nil {
		return err
	}
	want, err = filepath.Abs(want)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != want {
		return errors.New("Git 顶层目录与 data_dir 不一致")
	}
	out, err = r.Run(ctx, "symbolic-ref", "--short", "HEAD")
	if err != nil || strings.TrimSpace(string(out)) != branch {
		return errors.New("数据仓库必须位于配置的分支")
	}
	for _, p := range []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "index.lock"} {
		if _, err = os.Lstat(filepath.Join(r.Dir, ".git", p)); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("发现未完成的 Git 操作 .git/%s；保留现场，请先人工恢复", p)
		}
	}
	if _, err = r.Head(ctx); err != nil {
		return err
	}
	_, err = r.Run(ctx, "ls-files", "--error-unmatch", "--", "format.json")
	return err
}

// Head 返回提交 SHA，供整合前后比较实际文件树
func (r *Repo) Head(ctx context.Context) (string, error) {
	out, err := r.Run(ctx, "rev-parse", "HEAD")
	return strings.TrimSpace(string(out)), err
}

// Changed 返回包括未跟踪文件的恢复路径，并拒绝评论目录外的修改
func (r *Repo) Changed(ctx context.Context) ([]string, error) {
	out, err := r.Run(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	parts := strings.Split(string(out), "\x00")
	paths := []string{}
	for i := 0; i < len(parts); i++ {
		v := parts[i]
		if v == "" {
			continue
		}
		if len(v) < 4 {
			return nil, errors.New("无法解析 Git 状态")
		}
		p := v[3:]
		if !dataPath(p) {
			return nil, errors.New("数据仓库存在 comments 或 frames 之外的修改，请人工检查")
		}
		paths = append(paths, p)
		if strings.ContainsAny(v[:2], "RC") {
			i++
			if i >= len(parts) || !dataPath(parts[i]) {
				return nil, errors.New("不允许跨评论目录的重命名")
			}
			paths = append(paths, parts[i])
		}
	}
	return paths, nil
}

// Commit 暂存净变更；退出码 1 仅在 diff --quiet 时表示确有差异
func (r *Repo) Commit(ctx context.Context) (bool, error) {
	if _, err := r.Run(ctx, "add", "-A", "--", "comments", "frames"); err != nil {
		return false, err
	}
	_, err := r.Run(ctx, "diff", "--cached", "--quiet", "--exit-code")
	if err == nil {
		return false, nil
	}
	if !ExitCode(err, 1) {
		return false, err
	}
	_, err = r.Run(ctx, "commit", "-m", "comments: batch update")
	return err == nil, err
}

// dataPath 只允许评论和框元数据进入后台批量提交，配置与密钥不进入数据仓库。
func dataPath(p string) bool {
	return strings.HasPrefix(p, "comments/") || strings.HasPrefix(p, "frames/")
}

// Diff 返回文件树的净差异；禁用 rename 检测使两侧路径都能映射到分区
func (r *Repo) Diff(ctx context.Context, old, new string) ([]string, error) {
	out, err := r.Run(ctx, "diff", "--name-only", "--no-renames", "-z", old, new, "--")
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// Ancestor 判断提交包含关系，区分正常不包含和执行失败
func (r *Repo) Ancestor(ctx context.Context, a, b string) (bool, error) {
	_, err := r.Run(ctx, "merge-base", "--is-ancestor", a, b)
	if ExitCode(err, 1) {
		return false, nil
	}
	return err == nil, err
}

// CountAhead 计算尚未出现在远端跟踪分支中的提交数
func (r *Repo) CountAhead(ctx context.Context, ref string) (int, error) {
	out, err := r.Run(ctx, "rev-list", "--count", ref+"..HEAD")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// IntegrationBase 返回中断整合前的 HEAD；这是崩溃恢复标记，不是评论事件日志
func (r *Repo) IntegrationBase() (string, error) {
	b, err := os.ReadFile(filepath.Join(r.Dir, ".git", "commentb-sync-head"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(b))
	raw, err := hex.DecodeString(sha)
	if err != nil || (len(raw) != 20 && len(raw) != 32) || strings.ToLower(sha) != sha {
		return "", errors.New("Git 整合恢复标记损坏，请人工检查")
	}
	return sha, nil
}

// BeginIntegration 在修改文件树前持久化旧 HEAD，重启仅扫描这次整合涉及的目录
func (r *Repo) BeginIntegration(sha string) error {
	f, err := os.OpenFile(filepath.Join(r.Dir, ".git", "commentb-sync-head"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.WriteString(sha + "\n"); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return r.syncMetadata()
}

// FinishIntegration 仅在修复和提交完成后移除恢复标记
func (r *Repo) FinishIntegration() error {
	if err := os.Remove(filepath.Join(r.Dir, ".git", "commentb-sync-head")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return r.syncMetadata()
}

// syncMetadata 保证恢复标记的创建或删除已经同步到文件系统
func (r *Repo) syncMetadata() error {
	f, err := os.Open(filepath.Join(r.Dir, ".git"))
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
