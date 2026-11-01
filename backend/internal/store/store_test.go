package store_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/SomeBottle/Comment.B/backend/internal/store"
	"github.com/SomeBottle/Comment.B/backend/internal/testutil"
)

// rootDir 根据公开存储格式定位测试文件。
func rootDir(dir, page, id string) string {
	h := store.PageHash(page)
	return filepath.Join(dir, "comments", "blog", h[:2], h, "roots", id[:2], id)
}

// TestReplyChainAndDurableDelete 验证任意二级回复链、独立删除、级联删除和重启恢复。
func TestReplyChainAndDurableDelete(t *testing.T) {
	f := testutil.New(t)
	s := f.Store
	a := testutil.Create(t, s, "p", "", nil, "A")
	b := testutil.Create(t, s, "p", a.ID, &a.ID, "B")
	c := testutil.Create(t, s, "p", a.ID, &b.ID, "C")
	d := testutil.Create(t, s, "p", a.ID, &c.ID, "D")
	dir := rootDir(f.Config.DataDir, "p", a.ID)
	for _, p := range []string{"root.json", "replies/" + b.ID + ".json", "replies/" + c.ID + ".json", "replies/" + d.ID + ".json"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Fatal(err)
		}
	}
	if a.CreatedAt < time.Now().Add(-time.Minute).UnixMicro() || a.CreatedAt > 9007199254740991 {
		t.Fatal("时间戳不是兼容 JS 的 Unix 微秒")
	}
	s.Mu.Lock()
	if s.Cache.Stats().Entries != 0 {
		t.Fatal("写入不应主动建立索引")
	}
	if _, _, err := s.ListLocked("blog", "p", a.ID, nil, false, 10); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteLocked("blog", "p", a.ID, c.ID, "github-2"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("允许越权删除")
	}
	if err := s.DeleteLocked("blog", "p", a.ID, c.ID, "github-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadLocked("blog", "p", a.ID, d.ID); err != nil {
		t.Fatal("删除二级 C 不应删除 D", err)
	}
	if _, err := s.ReadLocked("blog", "p", a.ID, c.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("C 仍存在")
	}
	if _, err := s.CreateLocked("blog", "other", a.ID, &d.ID, "跨页", testutil.Author("1")); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("接受跨页引用", err)
	}
	if err := s.DeleteLocked("blog", "p", a.ID, a.ID, "github-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadLocked("blog", "p", a.ID, d.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("根删除后仍可访问二级")
	}
	s.Mu.Unlock()
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("根目录没有立即移走")
	}
	s.Close()
	reopened, err := store.Open(f.Config.DataDir, 1<<20, 1<<20, 16384)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.Mu.Lock()
	defer reopened.Mu.Unlock()
	if _, err = reopened.ReadLocked("blog", "p", a.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("重启恢复了已删除根")
	}
}

// TestColdBuildConcurrency 验证并发冷请求只构建一次当前分区和未缓存写入的惰性行为。
func TestColdBuildConcurrency(t *testing.T) {
	f := testutil.New(t)
	s := f.Store
	for i := 0; i < 25; i++ {
		testutil.Create(t, s, "p", "", nil, "根")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Mu.Lock()
			defer s.Mu.Unlock()
			items, _, err := s.ListLocked("blog", "p", "", nil, true, 5)
			if err != nil || len(items) != 5 {
				t.Errorf("分页失败: %v", err)
			}
		}()
	}
	wg.Wait()
	s.Mu.Lock()
	stats := s.Cache.Stats()
	s.Mu.Unlock()
	if stats.Builds != 1 || stats.Entries != 1 {
		t.Fatalf("冷启动重复构建: %+v", stats)
	}
	testutil.Create(t, s, "p", "", nil, "新增")
	s.Mu.Lock()
	_, more, err := s.ListLocked("blog", "p", "", nil, true, 25)
	s.Mu.Unlock()
	if err != nil || !more {
		t.Fatal("缓存未增量插入", err)
	}
}

// TestCorruptionAndSymlinks 验证损坏文件不被静默跳过、路径逃逸和 URL 限制。
func TestCorruptionAndSymlinks(t *testing.T) {
	f := testutil.New(t)
	s := f.Store
	c := testutil.Create(t, s, "p", "", nil, "正文")
	p := filepath.Join(rootDir(f.Config.DataDir, "p", c.ID), "root.json")
	if err := os.WriteFile(p, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Mu.Lock()
	// 人工替换文件模拟冷读取故障；运行中的文件变更应通过 Store 或 Git Worker。
	s.ClearCachesLocked()
	_, _, err := s.ListLocked("blog", "p", "", nil, true, 5)
	s.Mu.Unlock()
	if !errors.Is(err, store.ErrCorrupt) {
		t.Fatal("损坏文件被静默忽略", err)
	}
	b, _ := json.Marshal(c)
	os.WriteFile(p, b, 0600)
	os.Remove(p)
	outside := filepath.Join(t.TempDir(), "secret.json")
	os.WriteFile(outside, b, 0600)
	if err = os.Symlink(outside, p); err != nil {
		t.Fatal(err)
	}
	s.Mu.Lock()
	_, err = s.ReadLocked("blog", "p", c.ID, c.ID)
	s.Mu.Unlock()
	if !errors.Is(err, store.ErrCorrupt) {
		t.Fatal("跟随了恶意符号链接", err)
	}
	for _, v := range []string{"javascript:alert(1)", "https://user:pass@example.com/", "//example.com", "http:///missing", "https://"} {
		if store.ValidateLink(&v) == nil {
			t.Fatalf("接受危险链接 %q", v)
		}
	}
	s.Mu.Lock()
	_, err = s.CreateLocked("../escape", "p", "", nil, "x", testutil.Author("1"))
	s.Mu.Unlock()
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatal("路径穿越未拒绝")
	}
}

// TestExclusiveClone 验证一个数据克隆不能被两个服务进程同时写入。
func TestExclusiveClone(t *testing.T) {
	f := testutil.New(t)
	other, err := store.Open(f.Config.DataDir, 1<<20, 1<<20, 16384)
	if err == nil {
		other.Close()
		t.Fatal("重复打开同一克隆")
	}
}
