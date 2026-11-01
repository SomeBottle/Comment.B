// Package index 提供不含正文的稳定排序键和双向分页
package index

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"sort"
	"strings"
)

// encoding 是 RFC 4648 无填充 Base32，对外统一使用小写
var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// CommentID 保存 96 位随机标识，避免索引常驻字符串
type CommentID [12]byte

// NewID 使用密码学安全随机源生成标识
func NewID() (CommentID, error) {
	var id CommentID
	_, err := rand.Read(id[:])
	return id, err
}

// ParseID 拒绝大小写变体、错误长度和非零填充位等非规范表示
func ParseID(s string) (CommentID, error) {
	var id CommentID
	if len(s) != 20 || s != strings.ToLower(s) {
		return id, errors.New("评论 ID 必须是 20 字符规范小写 Base32")
	}
	b, err := encoding.DecodeString(strings.ToUpper(s))
	if err != nil || len(b) != len(id) {
		return id, errors.New("评论 ID 无效")
	}
	copy(id[:], b)
	if id.String() != s {
		return CommentID{}, errors.New("评论 ID 编码不规范")
	}
	return id, nil
}

// String 返回固定 20 字符的规范编码
func (id CommentID) String() string { return strings.ToLower(encoding.EncodeToString(id[:])) }

// SortKey 是唯一的分页顺序，不依赖文件名的目录遍历顺序
type SortKey struct {
	CreatedAt int64
	ID        CommentID
}

// Less 按微秒时间、原始 ID 字节升序比较。Base32 字符表的 ASCII 顺序不参与排序
func Less(a, b SortKey) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt < b.CreatedAt
	}
	return bytes.Compare(a.ID[:], b.ID[:]) < 0
}

// SortedIndex 只保存一份升序数组，降序请求反向读取
type SortedIndex struct{ Keys []SortKey }

// New 对冷扫描得到的键排序
func New(keys []SortKey) *SortedIndex {
	sort.Slice(keys, func(i, j int) bool { return Less(keys[i], keys[j]) })
	return &SortedIndex{Keys: keys}
}

// Insert 将键插入已有索引，重复键保持幂等
func (idx *SortedIndex) Insert(k SortKey) {
	i := sort.Search(len(idx.Keys), func(i int) bool { return !Less(idx.Keys[i], k) })
	if i < len(idx.Keys) && idx.Keys[i] == k {
		return
	}
	idx.Keys = append(idx.Keys, SortKey{})
	copy(idx.Keys[i+1:], idx.Keys[i:])
	idx.Keys[i] = k
}

// Remove 仅移除完全匹配的键
func (idx *SortedIndex) Remove(k SortKey) {
	i := sort.Search(len(idx.Keys), func(i int) bool { return !Less(idx.Keys[i], k) })
	if i < len(idx.Keys) && idx.Keys[i] == k {
		copy(idx.Keys[i:], idx.Keys[i+1:])
		idx.Keys = idx.Keys[:len(idx.Keys)-1]
	}
}

// Page 使用严格边界二分查找，游标对应评论已被删除时仍可继续
func (idx *SortedIndex) Page(after *SortKey, descending bool, limit int) ([]SortKey, bool) {
	items := make([]SortKey, 0, limit)
	if descending {
		i := len(idx.Keys) - 1
		if after != nil {
			i = sort.Search(len(idx.Keys), func(i int) bool { return !Less(idx.Keys[i], *after) }) - 1
		}
		for ; i >= 0 && len(items) < limit; i-- {
			items = append(items, idx.Keys[i])
		}
		return items, i >= 0
	}
	i := 0
	if after != nil {
		i = sort.Search(len(idx.Keys), func(i int) bool { return Less(*after, idx.Keys[i]) })
	}
	for ; i < len(idx.Keys) && len(items) < limit; i++ {
		items = append(items, idx.Keys[i])
	}
	return items, i < len(idx.Keys)
}
