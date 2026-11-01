package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/SomeBottle/Comment.B/backend/internal/store"
	gitworker "github.com/SomeBottle/Comment.B/backend/internal/sync"
	"github.com/SomeBottle/Comment.B/backend/internal/testutil"
)

// TestFrameQuotaConcurrencyAndRestart 验证同节点并发创建不会超额，归属和名称修改可持久恢复。
func TestFrameQuotaConcurrencyAndRestart(t *testing.T) {
	f := testutil.New(t)
	c := f.Config
	c.MaxFramesPerUser = 2
	var wg sync.WaitGroup
	var ids []string
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.Store.Mu.Lock()
			defer f.Store.Mu.Unlock()
			frame, err := f.Store.CreateFrameLocked("github-1", "网站", "简介", c)
			if err == nil {
				ids = append(ids, frame.ID)
			} else if !errors.Is(err, store.ErrFrameLimit) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(ids) != 2 {
		t.Fatal("并发额度错误", len(ids))
	}
	f.Store.Mu.Lock()
	_, err := f.Store.UpdateFrameLocked(ids[0], "github-1", "新名称", "新简介")
	f.Store.Mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	f.Store.Close()
	s, err := store.Open(c.DataDir, c.IndexCacheBudget, c.CommentCacheBudget, c.MaxContentBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Mu.Lock()
	defer s.Mu.Unlock()
	frame, err := s.FrameLocked(ids[0])
	if err != nil || frame.OwnerID != "github-1" || frame.Name != "新名称" {
		t.Fatal("归属或修改未恢复", err)
	}
	if _, err = s.CreateFrameLocked("github-1", "超额网站", "简介", c); !errors.Is(err, store.ErrFrameLimit) {
		t.Fatal("重启后已有框不再占用额度", err)
	}
}

// TestFrameDeletedMarkerRecovery 验证删除哨兵落盘后即使未完成目录清理，重启也会清理而不复活。
func TestFrameDeletedMarkerRecovery(t *testing.T) {
	f := testutil.New(t)
	f.Store.Mu.Lock()
	frame, err := f.Store.CreateFrameLocked("github-1", "网站", "简介", f.Config)
	if err != nil {
		t.Fatal(err)
	}
	root, err := f.Store.CreateLocked(frame.ID, "p", "", nil, "正文", testutil.Author("2"))
	f.Store.Mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	w := gitworker.New(f.Store, f.Repo, f.Config)
	if err = w.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.Store.Mu.Lock()
	err = f.Store.DeleteFrameLocked(frame.ID, "github-1")
	f.Store.Mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// 将回收区目录移回，模拟哨兵提交后目录重命名前退出的中间状态。
	entries, err := os.ReadDir(filepath.Join(f.Config.DataDir, ".git", "commentb-trash"))
	if err != nil || len(entries) != 1 {
		t.Fatal("删除回收区异常", err)
	}
	if err = os.Rename(filepath.Join(f.Config.DataDir, ".git", "commentb-trash", entries[0].Name()), filepath.Join(f.Config.DataDir, "comments", frame.ID)); err != nil {
		t.Fatal(err)
	}
	f.Store.Close()
	s, err := store.Open(f.Config.DataDir, 1<<20, 1<<20, 16384)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = gitworker.New(s, f.Repo, f.Config).Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if _, err = s.ReadLocked(frame.ID, "p", root.ID, root.ID); !errors.Is(err, store.ErrFrameNotFound) {
		t.Fatal("已删除框在重启后复活", err)
	}
	if _, err = os.Stat(filepath.Join(f.Config.DataDir, "comments", frame.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("重启未清理删除框残留", err)
	}
}
