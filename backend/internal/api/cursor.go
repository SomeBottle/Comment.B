package api

import (
	"encoding/json"
	"errors"

	"github.com/SomeBottle/Comment.B/backend/internal/auth"
	"github.com/SomeBottle/Comment.B/backend/internal/index"
	"github.com/SomeBottle/Comment.B/backend/internal/store"
)

// cursor 绑定分区与排序，不能跨页面、根评论或排序方向复用
type cursor struct {
	Version   int    `json:"v"`
	Frame     string `json:"f"`
	PageHash  string `json:"p"`
	Root      string `json:"r"`
	Sort      string `json:"s"`
	CreatedAt int64  `json:"t"`
	ID        string `json:"i"`
}

// encodeCursor 对分页边界加用途签名，客户端应将结果作为不透明字符串
func encodeCursor(a *auth.Auth, frame, page, root, order string, key index.SortKey) string {
	b, _ := json.Marshal(cursor{1, frame, store.PageHash(page), root, order, key.CreatedAt, key.ID.String()})
	return a.Sign("cursor", b)
}

// decodeCursor 在二分定位之前验证签名、规范 ID 和完整查询范围
func decodeCursor(a *auth.Auth, token, frame, page, root, order string) (*index.SortKey, error) {
	if token == "" {
		return nil, nil
	}
	b, err := a.Verify("cursor", token, 2048)
	if err != nil {
		return nil, err
	}
	var c cursor
	if json.Unmarshal(b, &c) != nil || c.Version != 1 || c.Frame != frame || c.PageHash != store.PageHash(page) || c.Root != root || c.Sort != order || c.CreatedAt <= 0 {
		return nil, errors.New("游标与当前查询不匹配")
	}
	id, err := index.ParseID(c.ID)
	if err != nil {
		return nil, err
	}
	return &index.SortKey{CreatedAt: c.CreatedAt, ID: id}, nil
}
