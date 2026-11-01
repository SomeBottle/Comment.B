package store_test

import (
	"errors"
	"testing"

	"github.com/SomeBottle/Comment.B/backend/internal/store"
	"github.com/SomeBottle/Comment.B/backend/internal/testutil"
)

// TestCommentCacheReadsAndIsolation 验证重复读取不再打开 JSON，且输入和返回值不能篡改共享缓存。
func TestCommentCacheReadsAndIsolation(t *testing.T) {
	f := testutil.New(t)
	s := f.Store
	root := testutil.Create(t, s, "p", "", nil, "根")
	link := "https://example.com/"
	email := "c160f8cc69a4f0bf2b0362752353d060"
	target := root.ID
	author := testutil.Author("1")
	author.Link = &link
	author.EmailMD5 = &email
	s.Mu.Lock()
	defer s.Mu.Unlock()
	reply, err := s.CreateLocked("blog", "p", root.ID, &target, "回复", author)
	if err != nil {
		t.Fatal(err)
	}
	if s.CommentCacheStatsLocked().DiskReads != 0 {
		t.Fatal("创建后没有使用已持久化评论的缓存")
	}
	// 修改创建参数及返回结构体中的指针，不能改变缓存中的已持久化快照。
	link = "https://changed.example/"
	email = "00000000000000000000000000000000"
	target = reply.ID
	got, err := s.ReadLocked("blog", "p", root.ID, reply.ID)
	if err != nil || *got.ReplyTo != root.ID || *got.Author.Link != "https://example.com/" || *got.Author.EmailMD5 != "c160f8cc69a4f0bf2b0362752353d060" {
		t.Fatal("创建参数污染缓存", err)
	}
	s.ClearCachesLocked()
	got, err = s.ReadLocked("blog", "p", root.ID, reply.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := s.CommentCacheStatsLocked()
	if before.DiskReads != 2 {
		t.Fatalf("冷读取应只打开根和目标两个文件: %+v", before)
	}
	*got.ReplyTo = reply.ID
	*got.Author.Link = "https://mutated.example/"
	*got.Author.EmailMD5 = "11111111111111111111111111111111"
	got.Content = "篡改"
	again, err := s.ReadLocked("blog", "p", root.ID, reply.ID)
	if err != nil {
		t.Fatal(err)
	}
	after := s.CommentCacheStatsLocked()
	if after.DiskReads != before.DiskReads || after.Hits-before.Hits != 2 {
		t.Fatal("重复读取仍访问正文文件")
	}
	if again.Content != "回复" || *again.ReplyTo != root.ID || *again.Author.Link != "https://example.com/" || *again.Author.EmailMD5 != "c160f8cc69a4f0bf2b0362752353d060" {
		t.Fatal("返回值污染了共享缓存")
	}
	if _, err = s.ReadLocked("other", "p", root.ID, reply.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("缓存跨评论框泄露数据", err)
	}
	if _, err = s.ReadLocked("blog", "another", root.ID, reply.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("缓存跨页面泄露数据", err)
	}
}

// TestCommentCacheAdmissionAndDeletion 验证只缓存展示正文、删除后清除缓存且保留其他回复。
func TestCommentCacheAdmissionAndDeletion(t *testing.T) {
	f := testutil.New(t)
	s := f.Store
	var roots []store.Comment
	for i := 0; i < 4; i++ {
		roots = append(roots, testutil.Create(t, s, "p", "", nil, "根"))
	}
	root := roots[0]
	var replies []store.Comment
	for i := 0; i < 8; i++ {
		replies = append(replies, testutil.Create(t, s, "p", root.ID, &root.ID, "二级"))
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.ClearCachesLocked()
	if _, _, err := s.ListLocked("blog", "p", "", nil, false, 2); err != nil {
		t.Fatal(err)
	}
	if stats := s.CommentCacheStatsLocked(); stats.Entries != 2 {
		t.Fatalf("索引冷扫描缓存了页面全部正文: %+v", stats)
	}
	s.ClearCachesLocked()
	if _, _, err := s.ListLocked("blog", "p", root.ID, nil, false, 3); err != nil {
		t.Fatal(err)
	}
	if stats := s.CommentCacheStatsLocked(); stats.Entries != 4 {
		t.Fatalf("二级扫描应只缓存根和 3 条展示正文: %+v", stats)
	}
	before := s.CommentCacheStatsLocked()
	if _, _, err := s.ListLocked("blog", "p", root.ID, nil, false, 3); err != nil {
		t.Fatal(err)
	}
	if s.CommentCacheStatsLocked().DiskReads != before.DiskReads {
		t.Fatal("重复分页仍读取正文文件")
	}
	child, err := s.CreateLocked("blog", "p", root.ID, &replies[0].ID, "回复目标", testutil.Author("1"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteLocked("blog", "p", root.ID, replies[0].ID, "github-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadLocked("blog", "p", root.ID, replies[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("删除后仍返回缓存目标", err)
	}
	if _, err = s.ReadLocked("blog", "p", root.ID, child.ID); err != nil {
		t.Fatal("二级删除清除了其他有效回复", err)
	}
	if _, err = s.ReadLocked("blog", "p", roots[1].ID, roots[1].ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteLocked("blog", "p", root.ID, root.ID, "github-1"); err != nil {
		t.Fatal(err)
	}
	if stats := s.CommentCacheStatsLocked(); stats.Entries != 1 {
		t.Fatalf("根级联删除未清空整组正文或误删邻近根: %+v", stats)
	}
	if _, err = s.ReadLocked("blog", "p", root.ID, child.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("根删除后缓存回复仍可见", err)
	}
	if _, err = s.CreateLocked("blog", "p", root.ID, &child.ID, "已删除根", testutil.Author("1")); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("缓存允许向已删除根回复", err)
	}
}

// TestCommentCacheDisabledAndEviction 验证禁用或预算不足时仍从磁盘正确读取完整评论。
func TestCommentCacheDisabledAndEviction(t *testing.T) {
	f := testutil.New(t)
	root := testutil.Create(t, f.Store, "p", "", nil, "根")
	reply := testutil.Create(t, f.Store, "p", root.ID, &root.ID, "二级")
	f.Store.Close()
	for _, budget := range []int64{0, 1} {
		s, err := store.Open(f.Config.DataDir, 1<<20, budget, 16384)
		if err != nil {
			t.Fatal(err)
		}
		s.Mu.Lock()
		for i := 0; i < 2; i++ {
			if _, err = s.ReadLocked("blog", "p", root.ID, reply.ID); err != nil {
				t.Fatal(err)
			}
		}
		stats := s.CommentCacheStatsLocked()
		s.Mu.Unlock()
		s.Close()
		if stats.Entries != 0 || stats.Bytes != 0 || stats.DiskReads != 4 {
			t.Fatalf("预算 %d 旁路错误: %+v", budget, stats)
		}
	}
}
