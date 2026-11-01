package store

import (
	"strings"
	"testing"
)

// TestCommentCacheLRUBudget 验证预算按正文长度计费、热点优先、超大条目旁路及零预算禁用。
func TestCommentCacheLRUBudget(t *testing.T) {
	comment := Comment{ID: "id", Content: strings.Repeat("文", 100)}
	size := commentBytes("a/root.json", comment)
	c := newCommentCache(size * 2)
	c.put("a/root.json", comment)
	c.put("b/root.json", comment)
	c.get("a/root.json")
	c.put("c/root.json", comment)
	if len(c.entries) != 2 || c.entries["a/root.json"] == nil || c.entries["b/root.json"] != nil || c.stats.Bytes > c.budget {
		t.Fatal("LRU 没有优先保留热点或违反预算")
	}
	huge := comment
	huge.Content = strings.Repeat("x", int(c.budget)+1)
	c.put("huge", huge)
	if len(c.entries) != 2 || c.entries["huge"] != nil || c.stats.Evictions != 1 {
		t.Fatal("超大评论不应挤出已有热点")
	}
	c.put("a/root.json", Comment{Content: "小"})
	if c.stats.Bytes >= size*2 {
		t.Fatal("覆盖后没有重新计算预算")
	}
	c.invalidateRoot("a")
	if len(c.entries) != 1 || c.entries["c/root.json"] == nil {
		t.Fatal("根失效误删邻近分区")
	}
	c.clear()
	if c.stats.Bytes != 0 || len(c.entries) != 0 || c.lru.Len() != 0 {
		t.Fatal("清空后残留计费或 LRU 条目")
	}
	disabled := newCommentCache(0)
	disabled.put("x", comment)
	if _, ok := disabled.get("x"); ok || disabled.stats.Bytes != 0 {
		t.Fatal("零预算没有禁用正文缓存")
	}
}
