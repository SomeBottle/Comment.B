package cache

import (
	"testing"

	"github.com/SomeBottle/Comment.B/backend/internal/index"
)

// TestBudgetAndInvalidation 验证 LRU 字节预算、超大分区旁路和失效后不增量复活。
func TestBudgetAndInvalidation(t *testing.T) {
	c := New(600)
	builds := 0
	build := func() (*index.SortedIndex, error) { builds++; return index.New([]index.SortKey{{CreatedAt: 1}}), nil }
	a, b, d := Key{Frame: "a"}, Key{Frame: "b"}, Key{Frame: "d"}
	c.GetOrBuild(a, build)
	c.GetOrBuild(b, build)
	c.GetOrBuild(a, build)
	c.GetOrBuild(d, build)
	if c.entries[a] == nil || c.entries[b] != nil || c.Stats().Bytes > 600 {
		t.Fatal("LRU 淘汰或预算错误")
	}
	c.Invalidate(a)
	c.InsertIfCached(a, index.SortKey{CreatedAt: 9})
	c.GetOrBuild(a, build)
	if builds != 4 || len(c.entries[a].Value.(*entry).idx.Keys) != 1 {
		t.Fatal("失效后错误复活旧索引")
	}
	huge := Key{Frame: "huge"}
	c.GetOrBuild(huge, func() (*index.SortedIndex, error) { return index.New(make([]index.SortKey, 100)), nil })
	if c.entries[huge] != nil || c.Stats().Bytes > 600 {
		t.Fatal("超大分区不应常驻缓存")
	}
	for i := 0; i < 100; i++ {
		c.InsertIfCached(a, index.SortKey{CreatedAt: int64(i + 10)})
	}
	if c.Stats().Bytes > 600 {
		t.Fatal("增量扩容超过预算")
	}
}
