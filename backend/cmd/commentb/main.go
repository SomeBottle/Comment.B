// Command commentb 启动无数据库的 Git-backed 评论服务。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SomeBottle/Comment.B/backend/internal/api"
	"github.com/SomeBottle/Comment.B/backend/internal/auth"
	"github.com/SomeBottle/Comment.B/backend/internal/config"
	gitrepo "github.com/SomeBottle/Comment.B/backend/internal/git"
	"github.com/SomeBottle/Comment.B/backend/internal/store"
	gitworker "github.com/SomeBottle/Comment.B/backend/internal/sync"
)

// main 统一报告启动和关闭错误，不输出密钥或 Git 原始网络响应。
func main() {
	if err := run(); err != nil {
		slog.Error("Comment.B 退出", "error", err)
		os.Exit(1)
	}
}

// run 验证独立仓库并管理 HTTP、Git Worker 和本地持久化的生命周期。
func run() error {
	configPath := flag.String("config", "config.json", "配置文件路径")
	initialize := flag.Bool("init", false, "仅初始化新的空数据仓库后退出")
	flag.Parse()
	c, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	r := &gitrepo.Repo{Dir: c.DataDir, Timeout: time.Duration(c.GitTimeout), AuthorName: c.GitAuthorName, AuthorEmail: c.GitAuthorEmail}
	if *initialize {
		return r.Init(context.Background(), c.Branch)
	}
	if err = r.Validate(context.Background(), c.Branch); err != nil {
		return err
	}
	key, err := c.SessionKey()
	if err != nil {
		return err
	}
	a, err := auth.New(c, key)
	if err != nil {
		return err
	}
	s, err := store.Open(c.DataDir, c.IndexCacheBudget, c.CommentCacheBudget, c.MaxContentBytes)
	if err != nil {
		return err
	}
	defer s.Close()
	w := gitworker.New(s, r, c)
	if err = w.Recover(context.Background()); err != nil {
		return fmt.Errorf("启动恢复失败: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); w.Run(ctx) }()
	srv := &http.Server{Addr: c.Listen, Handler: api.New(s, a, w, c), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: time.Duration(c.GitTimeout)*4 + 30*time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	errCh := make(chan error, 1)
	go func() { slog.Info("Comment.B 已启动", "listen", c.Listen); errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case err = <-errCh:
		stop()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	shutdownErr := srv.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		srv.Close()
	}
	<-workerDone
	flushCtx, flushCancel := context.WithTimeout(context.Background(), time.Duration(c.GitTimeout)*3)
	defer flushCancel()
	flushErr := w.Flush(flushCtx)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if shutdownErr != nil {
		return shutdownErr
	}
	return flushErr
}
