package store

import (
	"container/list"
	"strings"
	"unsafe"
)

// CommentCacheStats 是正文缓存的占用、命中、失效及实际 JSON 文件打开次数
type CommentCacheStats struct {
	Bytes, Entries                                    int64
	Hits, Misses, Evictions, Invalidations, DiskReads uint64
}

// commentEntry 保存一条已校验评论，回复目标和预览等派生字段不进入缓存
type commentEntry struct {
	path    string // path 是完整的仓库相对文件路径，包含评论框、页面和根的作用域
	comment Comment
	bytes   int64 // bytes 包含正文、作者、路径、指针及 LRU 元数据的保守估算
}

// commentCache 只缓存已读取或已持久化的评论，所有访问由 Store.Mu 保护
// 正文类型位于 store，缓存也放在本包以避免 store 与 cache 相互导入
type commentCache struct {
	budget  int64 // budget 独立于排序索引预算，零表示禁用
	entries map[string]*list.Element
	lru     *list.List
	stats   CommentCacheStats
}

// newCommentCache 创建空缓存，启动时不扫描或预热数据仓库
func newCommentCache(budget int64) *commentCache {
	return &commentCache{budget: budget, entries: make(map[string]*list.Element), lru: list.New()}
}

// copyComment 复制可变指针字段，防止调用方改写返回值时污染缓存或其他请求
// 普通字符串不可变，可以安全共享其正文存储，避免每次命中再复制正文
func copyComment(c Comment) Comment {
	if c.Author.EmailMD5 != nil {
		v := *c.Author.EmailMD5
		c.Author.EmailMD5 = &v
	}
	if c.Author.Link != nil {
		v := *c.Author.Link
		c.Author.Link = &v
	}
	if c.ReplyTo != nil {
		v := *c.ReplyTo
		c.ReplyTo = &v
	}
	return c
}

// commentBytes 对各字符串逐项计费，额外计入映射和链表的保守开销
func commentBytes(p string, c Comment) int64 {
	n := int64(unsafe.Sizeof(commentEntry{})) + 256
	for _, s := range []string{p, c.ID, c.FrameID, c.PageKey, c.Author.ID, c.Author.Name, c.RootID, c.Content} {
		n += int64(len(s))
	}
	for _, p := range []*string{c.Author.EmailMD5, c.Author.Link, c.ReplyTo} {
		if p != nil {
			n += int64(unsafe.Sizeof(*p)) + int64(len(*p))
		}
	}
	return n
}

// get 返回独立的评论快照，命中不触发 Stat、Open 或 JSON 解析
func (c *commentCache) get(p string) (Comment, bool) {
	if e := c.entries[p]; e != nil {
		c.stats.Hits++
		c.lru.MoveToFront(e)
		return copyComment(e.Value.(*commentEntry).comment), true
	}
	c.stats.Misses++
	return Comment{}, false
}

// put 仅收纳完整的有效评论，超出整个预算的单条正文不淘汰已有热点
func (c *commentCache) put(p string, comment Comment) {
	if e := c.entries[p]; e != nil {
		c.remove(e)
	}
	n := commentBytes(p, comment)
	if n > c.budget {
		return
	}
	c.entries[p] = c.lru.PushFront(&commentEntry{path: p, comment: copyComment(comment), bytes: n})
	c.stats.Bytes += n
	for c.stats.Bytes > c.budget {
		c.remove(c.lru.Back())
		c.stats.Evictions++
	}
}

// invalidate 使指定文件失效；不缓存不存在或读取失败的结果
func (c *commentCache) invalidate(p string) {
	if e := c.entries[p]; e != nil {
		c.remove(e)
		c.stats.Invalidations++
	}
}

// invalidateRoot 遍历有界缓存移除整组评论，不扫描磁盘或另建全局回复索引
func (c *commentCache) invalidateRoot(dir string) {
	prefix := dir + "/"
	for p := range c.entries {
		if strings.HasPrefix(p, prefix) {
			c.invalidate(p)
		}
	}
}

// clear 在工作区状态不确定时丢弃正文，保留累计监控计数
func (c *commentCache) clear() {
	for p := range c.entries {
		c.invalidate(p)
	}
}

// remove 同时移除映射、LRU 节点及对应预算
func (c *commentCache) remove(e *list.Element) {
	v := e.Value.(*commentEntry)
	delete(c.entries, v.path)
	c.lru.Remove(e)
	c.stats.Bytes -= v.bytes
}

// CommentCacheStatsLocked 返回正文缓存快照，调用方必须持有 Store.Mu
func (s *Store) CommentCacheStatsLocked() CommentCacheStats {
	stats := s.comments.stats
	stats.Entries = int64(len(s.comments.entries))
	return stats
}

// ClearCachesLocked 同时丢弃排序索引和正文；调用方必须持有 Store.Mu
func (s *Store) ClearCachesLocked() { s.Cache.Clear(); s.comments.clear(); clear(s.frames) }

// readCached 使用完整路径查找已校验正文，并再次检查原始页面作用域
// 写入、删除及 Git 整合都在相同工作区锁内失效缓存，因此命中无需重新检查文件
func (s *Store) readCached(p, frame, page, root, id string) (Comment, error) {
	if c, ok := s.comments.get(p); ok {
		if c.FrameID != frame || c.PageKey != page || c.RootID != root || c.ID != id {
			return Comment{}, ErrCorrupt
		}
		return c, nil
	}
	c, err := s.read(p, frame, page, root, id)
	if err == nil {
		s.comments.put(p, c)
	}
	return c, err
}
