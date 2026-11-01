// Package testutil 创建真实临时文件和 Git 仓库，测试不依赖远端服务或 Mock 存储。
package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/SomeBottle/Comment.B/backend/internal/config"
	gitrepo "github.com/SomeBottle/Comment.B/backend/internal/git"
	"github.com/SomeBottle/Comment.B/backend/internal/store"
)

// Fixture 包含一个已初始化、已独占打开的本地评论仓库。
type Fixture struct {
	Config config.Config
	Repo   *gitrepo.Repo
	Store  *store.Store
}

// New 创建不自动同步的仓库，测试结束自动关闭文件句柄。
func New(t testing.TB) *Fixture {
	t.Helper()
	c := config.Defaults()
	c.DataDir = t.TempDir()
	c.Remote = ""
	c.RateLimitPerMinute = 10000
	r := &gitrepo.Repo{Dir: c.DataDir, Timeout: 5 * time.Second, AuthorName: "test", AuthorEmail: "test@localhost"}
	if err := r.Init(context.Background(), c.Branch); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(c.DataDir, c.IndexCacheBudget, c.CommentCacheBudget, c.MaxContentBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &Fixture{c, r, s}
}

// Run 执行测试用 Git 命令，失败时立刻停止当前用例。
func Run(t testing.TB, r *gitrepo.Repo, args ...string) string {
	t.Helper()
	out, err := r.Run(context.Background(), args...)
	if err != nil {
		t.Fatalf("%v: %v: %s", args, err, out)
	}
	return string(out)
}

// Author 返回不含明文邮箱的测试身份。
func Author(id string) store.Author { return store.Author{ID: "github-" + id, Name: "用户" + id} }

// Create 在工作区锁内创建评论。
func Create(t testing.TB, s *store.Store, page, root string, reply *string, content string) store.Comment {
	t.Helper()
	s.Mu.Lock()
	defer s.Mu.Unlock()
	c, err := s.CreateLocked("blog", page, root, reply, content, Author("1"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
