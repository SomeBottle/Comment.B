package sync

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SomeBottle/Comment.B/backend/internal/config"
	gitrepo "github.com/SomeBottle/Comment.B/backend/internal/git"
	"github.com/SomeBottle/Comment.B/backend/internal/store"
	"github.com/SomeBottle/Comment.B/backend/internal/testutil"
)

// cluster 使用一个本地 bare 远端和两个独立克隆，不依赖网络或外部服务。
func cluster(t *testing.T) (*Worker, *Worker, store.Comment, store.Comment) {
	t.Helper()
	seed := testutil.New(t)
	root := testutil.Create(t, seed.Store, "p", "", nil, "根")
	reply := testutil.Create(t, seed.Store, "p", root.ID, &root.ID, "原回复")
	if err := New(seed.Store, seed.Repo, seed.Config).Cycle(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	testutil.Run(t, seed.Repo, "init", "--bare", remote)
	testutil.Run(t, seed.Repo, "remote", "add", "origin", remote)
	testutil.Run(t, seed.Repo, "push", "origin", "HEAD:refs/heads/main")
	nodes := []*Worker{}
	for i := 0; i < 2; i++ {
		dir := filepath.Join(t.TempDir(), "clone")
		testutil.Run(t, seed.Repo, "clone", "--branch", "main", remote, dir)
		c := seed.Config
		c.DataDir = dir
		c.Remote = "origin"
		r := &gitrepo.Repo{Dir: dir, Timeout: seed.Repo.Timeout, AuthorName: "test", AuthorEmail: "test@localhost"}
		if err := r.Validate(context.Background(), c.Branch); err != nil {
			t.Fatal(err)
		}
		s, err := store.Open(dir, c.IndexCacheBudget, c.CommentCacheBudget, c.MaxContentBytes)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		w := New(s, r, c)
		if err = w.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, w)
	}
	return nodes[0], nodes[1], root, reply
}

// cycle 在测试中执行完整同步，失败时显示脱敏错误。
func cycle(t *testing.T, w *Worker) {
	t.Helper()
	if err := w.Cycle(context.Background(), true); err != nil {
		t.Fatal(err)
	}
}

// TestConcurrentCreation 验证不同文件的并发提交通过 rebase 保留，并且没有强制推送。
func TestConcurrentCreation(t *testing.T) {
	a, b, _, _ := cluster(t)
	testutil.Create(t, a.Store, "new", "", nil, "A")
	testutil.Create(t, b.Store, "new", "", nil, "B")
	cycle(t, a)
	cycle(t, b)
	cycle(t, a)
	for _, w := range []*Worker{a, b} {
		w.Store.Mu.Lock()
		items, _, err := w.Store.ListLocked("blog", "new", "", nil, false, 20)
		w.Store.Mu.Unlock()
		if err != nil || len(items) != 2 {
			t.Fatalf("并发创建丢失: %v %d", err, len(items))
		}
	}
}

// TestRootDeleteWinsLateReply 验证迟到回复在 root.json 消失后被修复且永不展示。
func TestRootDeleteWinsLateReply(t *testing.T) {
	a, b, root, _ := cluster(t)
	a.Store.Mu.Lock()
	err := a.Store.DeleteLocked("blog", "p", root.ID, root.ID, "github-1")
	a.Store.Mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	late := testutil.Create(t, b.Store, "p", root.ID, &root.ID, "迟到回复")
	cycle(t, a)
	cycle(t, b)
	cycle(t, a)
	for _, w := range []*Worker{a, b} {
		w.Store.Mu.Lock()
		_, err := w.Store.ReadLocked("blog", "p", root.ID, late.ID)
		w.Store.Mu.Unlock()
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("残留回复对外可见: %v", err)
		}
	}
	if b.Store.Repairs != 1 {
		t.Fatalf("未修复迟到回复: %d", b.Store.Repairs)
	}
	files := testutil.Run(t, b.Repo, "ls-files", "--", "comments")
	if strings.TrimSpace(files) != "" {
		t.Fatalf("Git 仍保留无根数据: %s", files)
	}
}

// TestDeletedReplyRetainsNewChild 验证删除二级目标不会级联删除旧节点新建的其他二级评论。
func TestDeletedReplyRetainsNewChild(t *testing.T) {
	a, b, root, reply := cluster(t)
	a.Store.Mu.Lock()
	err := a.Store.DeleteLocked("blog", "p", root.ID, reply.ID, "github-1")
	a.Store.Mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	child := testutil.Create(t, b.Store, "p", root.ID, &reply.ID, "继续回复")
	cycle(t, a)
	cycle(t, b)
	cycle(t, a)
	a.Store.Mu.Lock()
	defer a.Store.Mu.Unlock()
	got, err := a.Store.ReadLocked("blog", "p", root.ID, child.ID)
	if err != nil || got.ReplyTo == nil || *got.ReplyTo != reply.ID {
		t.Fatal("新二级回复丢失", err)
	}
	if _, err = a.Store.ReadLocked("blog", "p", root.ID, reply.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("被删除目标仍存在")
	}
}

// TestSelectiveInvalidation 验证 Fetch、Commit、Push 不触发重建，仅整合后的相关分区失效。
func TestSelectiveInvalidation(t *testing.T) {
	a, b, root, _ := cluster(t)
	a.Store.Mu.Lock()
	a.Store.ListLocked("blog", "p", "", nil, true, 5)
	a.Store.ListLocked("blog", "p", root.ID, nil, true, 5)
	before := a.Store.Cache.Stats()
	a.Store.Mu.Unlock()
	testutil.Create(t, b.Store, "p", root.ID, &root.ID, "新回复")
	cycle(t, b)
	testutil.Run(t, a.Repo, "fetch", "origin", "main")
	a.Store.Mu.Lock()
	fetchStats := a.Store.Cache.Stats()
	a.Store.Mu.Unlock()
	if fetchStats != before {
		t.Fatal("单纯 Fetch 使缓存失效")
	}
	cycle(t, a)
	a.Store.Mu.Lock()
	after := a.Store.Cache.Stats()
	if after.Invalidations-before.Invalidations != 1 || after.Entries != 1 {
		t.Fatalf("失效了无关分区: %+v", after)
	}
	a.Store.ListLocked("blog", "p", "", nil, true, 5)
	if a.Store.Cache.Stats().Builds != before.Builds {
		t.Fatal("页面一级索引被无谓重建")
	}
	a.Store.ListLocked("blog", "p", root.ID, nil, true, 5)
	rebuilt := a.Store.Cache.Stats()
	a.Store.Mu.Unlock()
	cycle(t, a)
	a.Store.Mu.Lock()
	same := a.Store.Cache.Stats()
	a.Store.Mu.Unlock()
	if same != rebuilt {
		t.Fatal("Commit / Push 使缓存重建")
	}
}

// TestIdempotentDeleteAndConflict 验证重复删除收敛，真实内容冲突暂停自动同步且保留本地文件。
func TestIdempotentDeleteAndConflict(t *testing.T) {
	a, b, root, reply := cluster(t)
	for _, w := range []*Worker{a, b} {
		w.Store.Mu.Lock()
		err := w.Store.DeleteLocked("blog", "p", root.ID, reply.ID, "github-1")
		w.Store.Mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	cycle(t, a)
	cycle(t, b)
	cycle(t, a)
	for i, w := range []*Worker{a, b} {
		h := store.PageHash("p")
		p := filepath.Join(w.Config.DataDir, "comments", "blog", h[:2], h, "roots", root.ID[:2], root.ID, "root.json")
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		content := "node-a"
		if i == 1 {
			content = "node-b"
		}
		data = []byte(strings.Replace(string(data), "根", content, 1))
		if err = os.WriteFile(p, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cycle(t, a)
	if err := b.Cycle(context.Background(), true); err == nil {
		t.Fatal("真正的内容冲突没有停止")
	}
	if !b.status.Paused || b.status.Conflicts != 1 {
		t.Fatalf("冲突状态错误: %+v", b.status)
	}
	if b.Store.Blocked != nil {
		t.Fatal("安全 abort 后本地工作区应仍可读取", b.Store.Blocked)
	}
	if out := testutil.Run(t, b.Repo, "show", "HEAD:comments/blog/"+store.PageHash("p")[:2]+"/"+store.PageHash("p")+"/roots/"+root.ID[:2]+"/"+root.ID+"/root.json"); !strings.Contains(out, "node-b") {
		t.Fatal("冲突丢失本地数据")
	}
}

// TestRestartCommitsDeletion 验证根目录移动后即使没有 HTTP 内存计数，恢复仍会提交删除。
func TestRestartCommitsDeletion(t *testing.T) {
	f := testutil.New(t)
	root := testutil.Create(t, f.Store, "p", "", nil, "根")
	w := New(f.Store, f.Repo, f.Config)
	if err := w.Cycle(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	f.Store.Mu.Lock()
	err := f.Store.DeleteLocked("blog", "p", root.ID, root.ID, "github-1")
	f.Store.Mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	f.Store.Close()
	s, err := store.Open(f.Config.DataDir, 1<<20, 1<<20, 16384)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = New(s, f.Repo, f.Config).Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if files := strings.TrimSpace(testutil.Run(t, f.Repo, "ls-files", "--", "comments")); files != "" {
		t.Fatal("删除未在恢复后提交", files)
	}
}

// TestInterruptedIntegrationRecovery 验证合并已落盘但修复前退出时，重启仍能发现无根迟到回复。
func TestInterruptedIntegrationRecovery(t *testing.T) {
	a, b, root, _ := cluster(t)
	a.Store.Mu.Lock()
	err := a.Store.DeleteLocked("blog", "p", root.ID, root.ID, "github-1")
	a.Store.Mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	late := testutil.Create(t, b.Store, "p", root.ID, &root.ID, "中断时迟到")
	cycle(t, a)
	if _, err = b.Repo.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	base, err := b.Repo.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Repo.BeginIntegration(base); err != nil {
		t.Fatal(err)
	}
	testutil.Run(t, b.Repo, "fetch", "origin", "main")
	testutil.Run(t, b.Repo, "rebase", "origin/main")
	b.Store.Close()
	s, err := store.Open(b.Config.DataDir, 1<<20, 1<<20, 16384)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := New(s, b.Repo, b.Config)
	if err = w.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Mu.Lock()
	_, err = s.ReadLocked("blog", "p", root.ID, late.ID)
	repairs := s.Repairs
	s.Mu.Unlock()
	if !errors.Is(err, store.ErrNotFound) || repairs != 1 {
		t.Fatalf("未恢复中断整合: %v repairs=%d", err, repairs)
	}
	if marker, err := b.Repo.IntegrationBase(); err != nil || marker != "" {
		t.Fatal("整合标记未清除", err)
	}
}

// TestOfflineLocalWrites 验证远端不可用只降低同步状态，仍接受本地写入和本地 Commit。
func TestOfflineLocalWrites(t *testing.T) {
	f := testutil.New(t)
	c := f.Config
	c.Remote = "origin"
	testutil.Run(t, f.Repo, "remote", "add", "origin", filepath.Join(t.TempDir(), "missing.git"))
	w := New(f.Store, f.Repo, c)
	if err := w.Cycle(context.Background(), true); err == nil {
		t.Fatal("不存在的远端同步成功")
	}
	if !w.status.Degraded || w.status.Paused || f.Store.Blocked != nil {
		t.Fatalf("断网错误禁用本地: %+v", w.status)
	}
	testutil.Create(t, f.Store, "offline", "", nil, "断网写入")
	if err := w.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if files := testutil.Run(t, f.Repo, "ls-files", "--", "comments"); !strings.Contains(files, "root.json") {
		t.Fatal("本地写入未提交")
	}
}

// TestBatchSchedule 验证数量阈值、时间阈值，以及远端暂停后仍可生成本地批量提交。
func TestBatchSchedule(t *testing.T) {
	for _, mode := range []string{"threshold", "interval", "paused"} {
		t.Run(mode, func(t *testing.T) {
			f := testutil.New(t)
			c := f.Config
			c.CommitInterval = config.Duration(time.Hour)
			c.CommitMaxChanges = 2
			if mode == "interval" {
				c.CommitInterval = config.Duration(20 * time.Millisecond)
				c.CommitMaxChanges = 100
			}
			w := New(f.Store, f.Repo, c)
			if mode == "paused" {
				w.status.Paused = true
			}
			before, err := f.Repo.Head(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			testutil.Create(t, f.Store, "p", "", nil, "一")
			if mode != "interval" {
				testutil.Create(t, f.Store, "p", "", nil, "二")
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); w.Run(ctx) }()
			defer func() { cancel(); <-done }()
			w.Wake()
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				f.Store.Mu.Lock()
				pending := f.Store.Pending
				f.Store.Mu.Unlock()
				if pending == 0 {
					head, err := f.Repo.Head(context.Background())
					if err != nil || head == before {
						t.Fatal("没有生成批量提交", err)
					}
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("未按调度提交本地修改")
		})
	}
}

// TestCommentCacheSyncInvalidation 验证 Git 整合精确失效正文，根删除同时清空该组缓存回复。
func TestCommentCacheSyncInvalidation(t *testing.T) {
	a, b, root, reply := cluster(t)
	child := testutil.Create(t, a.Store, "p", root.ID, &reply.ID, "仍保留的回复")
	unrelated := testutil.Create(t, a.Store, "other", "", nil, "无关根")
	cycle(t, a)
	cycle(t, b)
	a.Store.Mu.Lock()
	for _, id := range []string{root.ID, reply.ID, child.ID} {
		if _, err := a.Store.ReadLocked("blog", "p", root.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Store.ReadLocked("blog", "other", unrelated.ID, unrelated.ID); err != nil {
		t.Fatal(err)
	}
	before := a.Store.CommentCacheStatsLocked()
	a.Store.Mu.Unlock()
	b.Store.Mu.Lock()
	err := b.Store.DeleteLocked("blog", "p", root.ID, reply.ID, "github-1")
	b.Store.Mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	cycle(t, b)
	testutil.Run(t, a.Repo, "fetch", "origin", "main")
	a.Store.Mu.Lock()
	fetched := a.Store.CommentCacheStatsLocked()
	a.Store.Mu.Unlock()
	if fetched != before {
		t.Fatal("单独 Fetch 意外失效正文缓存")
	}
	cycle(t, a)
	a.Store.Mu.Lock()
	after := a.Store.CommentCacheStatsLocked()
	if after.Invalidations-before.Invalidations != 1 || after.Entries != 3 {
		t.Fatalf("二级删除未精确失效: before=%+v after=%+v", before, after)
	}
	if _, err = a.Store.ReadLocked("blog", "p", root.ID, reply.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("远端删除后仍返回缓存正文", err)
	}
	if _, err = a.Store.ReadLocked("blog", "p", root.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.ReadLocked("blog", "other", unrelated.ID, unrelated.ID); err != nil {
		t.Fatal(err)
	}
	if a.Store.CommentCacheStatsLocked().DiskReads != after.DiskReads {
		t.Fatal("整合后重读了未变化正文")
	}
	stable := a.Store.CommentCacheStatsLocked()
	a.Store.Mu.Unlock()
	cycle(t, a)
	a.Store.Mu.Lock()
	if now := a.Store.CommentCacheStatsLocked(); now.Invalidations != stable.Invalidations || now.Entries != stable.Entries {
		t.Fatal("Commit / Push 意外失效正文缓存")
	}
	a.Store.Mu.Unlock()
	// 模拟远端仓库的根字段更新，整合后不能使用旧根或它下面的缓存回复。
	hash := store.PageHash("p")
	rootPath := filepath.Join("comments", "blog", hash[:2], hash, "roots", root.ID[:2], root.ID, "root.json")
	b.Store.Mu.Lock()
	updated, err := b.Store.ReadLocked("blog", "p", root.ID, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated.Author.Name = "更新后的名字"
	data, err := json.Marshal(updated)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(b.Config.DataDir, rootPath), data, 0600); err != nil {
		t.Fatal(err)
	}
	b.Store.InvalidatePathsLocked([]string{filepath.ToSlash(rootPath)})
	b.Store.Mu.Unlock()
	cycle(t, b)
	cycle(t, a)
	a.Store.Mu.Lock()
	if a.Store.CommentCacheStatsLocked().Entries != 1 {
		t.Fatal("根变更未失效组内正文或误删无关根")
	}
	reloaded, err := a.Store.ReadLocked("blog", "p", root.ID, root.ID)
	if err != nil || reloaded.Author.Name != "更新后的名字" {
		t.Fatal("根正文变更后仍返回旧缓存", err)
	}
	if _, err = a.Store.ReadLocked("blog", "p", root.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	a.Store.Mu.Unlock()
	b.Store.Mu.Lock()
	err = b.Store.DeleteLocked("blog", "p", root.ID, root.ID, "github-1")
	b.Store.Mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	cycle(t, b)
	cycle(t, a)
	a.Store.Mu.Lock()
	defer a.Store.Mu.Unlock()
	if stats := a.Store.CommentCacheStatsLocked(); stats.Entries != 1 {
		t.Fatalf("远端根删除后仍保留组内正文或清空了无关根: %+v", stats)
	}
	if _, err = a.Store.ReadLocked("blog", "p", root.ID, child.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("根删除后仍显示缓存回复", err)
	}
}

// TestFrameMetadataSyncAndLateComments 验证新框归属随 Git 同步，删除框后迟到根和回复都会被移除。
func TestFrameMetadataSyncAndLateComments(t *testing.T) {
	a, b, _, _ := cluster(t)
	a.Store.Mu.Lock()
	frame, err := a.Store.CreateFrameLocked("github-1", "新网站", "简介", a.Config)
	a.Store.Mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	cycle(t, a)
	cycle(t, b)
	b.Store.Mu.Lock()
	synced, err := b.Store.FrameLocked(frame.ID)
	if err != nil || synced.OwnerID != "github-1" {
		t.Fatal("框归属未同步", err)
	}
	root, err := b.Store.CreateLocked(frame.ID, "p", "", nil, "他人评论", testutil.Author("2"))
	b.Store.Mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	cycle(t, b)
	cycle(t, a)
	// B 已缓存元数据，A 的修改同步后必须使其失效并重新读取。
	a.Store.Mu.Lock()
	_, err = a.Store.UpdateFrameLocked(frame.ID, "github-1", "更新网站", "更新简介")
	a.Store.Mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	cycle(t, a)
	cycle(t, b)
	b.Store.Mu.Lock()
	synced, err = b.Store.FrameLocked(frame.ID)
	b.Store.Mu.Unlock()
	if err != nil || synced.Name != "更新网站" || synced.Intro != "更新简介" || synced.OwnerID != "github-1" {
		t.Fatal("框修改同步后仍使用旧元数据", err)
	}
	a.Store.Mu.Lock()
	// 框创建者无需全局管理员角色，就可以删除其他作者的评论。
	if err = a.Store.DeleteLocked(frame.ID, "p", root.ID, root.ID, "github-1"); err != nil {
		t.Fatal("拥有者不能管理本框评论", err)
	}
	if err = a.Store.DeleteFrameLocked(frame.ID, "github-1"); err != nil {
		t.Fatal(err)
	}
	a.Store.Mu.Unlock()
	// B 尚未同步删除，仍在已缓存框下写入迟到的新根和二级评论。
	b.Store.Mu.Lock()
	lateRoot, err := b.Store.CreateLocked(frame.ID, "another", "", nil, "迟到根", testutil.Author("2"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Store.CreateLocked(frame.ID, "p", root.ID, &root.ID, "迟到回复", testutil.Author("2"))
	b.Store.Mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	cycle(t, a)
	cycle(t, b)
	cycle(t, a)
	for _, w := range []*Worker{a, b} {
		w.Store.Mu.Lock()
		if _, err = w.Store.FrameLocked(frame.ID); !errors.Is(err, store.ErrFrameNotFound) {
			t.Fatal("同步后框复活", err)
		}
		if _, err = w.Store.ReadLocked(frame.ID, "another", lateRoot.ID, lateRoot.ID); !errors.Is(err, store.ErrFrameNotFound) {
			t.Fatal("迟到根对外可见", err)
		}
		frames, err := w.Store.ListFramesLocked("github-1")
		w.Store.Mu.Unlock()
		if err != nil || len(frames) != 0 {
			t.Fatal("删除框仍占用额度", err)
		}
		if _, err = os.Stat(filepath.Join(w.Config.DataDir, "comments", frame.ID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("未清理已删除框迟到文件", err)
		}
	}
}
