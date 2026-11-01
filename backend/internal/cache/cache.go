// Package cache 管理可丢弃的分区排序索引；所有访问由 Store.Mu 串行化
package cache

import (
	"container/list"
	"unsafe"

	"github.com/SomeBottle/Comment.B/backend/internal/index"
)

// Key 精确标识页面一级列表或某一级评论的二级列表
type Key struct{ Frame, PageHash, Root string }

// Stats 是缓存估算占用和累计计数的快照
type Stats struct {
	Bytes, Entries                                 int64
	Hits, Misses, Builds, Invalidations, Evictions uint64
}

// entry 只包含索引元数据，不保存评论正文
type entry struct {
	key   Key
	idx   *index.SortedIndex
	bytes int64
}

// Cache 使用按估算字节数限制的 LRU，不缓存超出总预算的单个分区
type Cache struct {
	budget  int64
	entries map[Key]*list.Element
	lru     *list.List
	stats   Stats
}

// New 创建空缓存，启动时不访问数据目录
func New(budget int64) *Cache {
	return &Cache{budget: budget, entries: make(map[Key]*list.Element), lru: list.New()}
}

// estimate 计入数组容量、键字符串及每项保守元数据开销
func estimate(k Key, idx *index.SortedIndex) int64 {
	return int64(cap(idx.Keys))*int64(unsafe.Sizeof(index.SortKey{})) + int64(len(k.Frame)+len(k.PageHash)+len(k.Root)) + 256
}

// GetOrBuild 在工作区锁内执行构建，同一分区的并发冷请求不会重复构建快照
func (c *Cache) GetOrBuild(k Key, build func() (*index.SortedIndex, error)) (*index.SortedIndex, error) {
	if e := c.entries[k]; e != nil {
		c.stats.Hits++
		c.lru.MoveToFront(e)
		return e.Value.(*entry).idx, nil
	}
	c.stats.Misses++
	idx, err := build()
	if err != nil {
		return nil, err
	}
	c.stats.Builds++
	n := estimate(k, idx)
	if n <= c.budget {
		c.entries[k] = c.lru.PushFront(&entry{k, idx, n})
		c.stats.Bytes += n
		c.trim()
	}
	return idx, nil
}

// InsertIfCached 仅更新仍有效的缓存；失效分区已被移除，不会被意外复活
func (c *Cache) InsertIfCached(k Key, key index.SortKey) {
	if e := c.entries[k]; e != nil {
		v := e.Value.(*entry)
		v.idx.Insert(key)
		c.stats.Bytes -= v.bytes
		v.bytes = estimate(k, v.idx)
		c.stats.Bytes += v.bytes
		c.trim()
	}
}

// RemoveIfCached 删除排序键；保留的数组容量仍计入预算
func (c *Cache) RemoveIfCached(k Key, key index.SortKey) {
	if e := c.entries[k]; e != nil {
		e.Value.(*entry).idx.Remove(key)
	}
}

// Invalidate 丢弃分区，下一次访问才从文件重建
func (c *Cache) Invalidate(k Key) {
	if e := c.entries[k]; e != nil {
		c.remove(e)
		c.stats.Invalidations++
	}
}

// Clear 在工作区处于不确定状态时丢弃全部非权威索引
func (c *Cache) Clear() {
	for k := range c.entries {
		c.Invalidate(k)
	}
}

// remove 同步更新 LRU、映射和预算
func (c *Cache) remove(e *list.Element) {
	v := e.Value.(*entry)
	c.stats.Bytes -= v.bytes
	delete(c.entries, v.key)
	c.lru.Remove(e)
}

// trim 淘汰最久未访问的分区，直到回到预算内
func (c *Cache) trim() {
	for c.stats.Bytes > c.budget {
		c.remove(c.lru.Back())
		c.stats.Evictions++
	}
}

// Stats 返回不包含正文的监控快照
func (c *Cache) Stats() Stats { v := c.stats; v.Entries = int64(len(c.entries)); return v }
