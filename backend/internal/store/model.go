// Package store 以独立 JSON 文件保存评论，并串行协调文件、索引及 Git 工作区
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SomeBottle/Comment.B/backend/internal/cache"
	"github.com/SomeBottle/Comment.B/backend/internal/config"
	"github.com/SomeBottle/Comment.B/backend/internal/index"
)

// ErrNotFound 表示根或直接目标当前不存在
var ErrNotFound = errors.New("评论不存在")

// ErrInvalid 表示输入未通过约束校验
var ErrInvalid = errors.New("输入无效")

// ErrCorrupt 表示已存在文件损坏，不能作为完整分区静默跳过
var ErrCorrupt = errors.New("评论数据损坏")

// ErrForbidden 表示已认证身份没有删除权限
var ErrForbidden = errors.New("没有删除权限")

// ErrUnavailable 表示工作区处于必须人工处理的故障状态
var ErrUnavailable = errors.New("工作区暂不可用")

// Author 是创建时的身份快照，授权只使用 ID
type Author struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	EmailMD5 *string `json:"email_md5"`
	Link     *string `json:"link"`
}

// Comment 对应一个不可编辑的 JSON 文件
type Comment struct {
	Version   int     `json:"version"`
	ID        string  `json:"id"`
	FrameID   string  `json:"frame_id"`
	PageKey   string  `json:"page_key"`
	Author    Author  `json:"author"`
	RootID    string  `json:"root_id"`
	ReplyTo   *string `json:"reply_to"`
	Content   string  `json:"content"`
	CreatedAt int64   `json:"created_at"`
}

// ValidateScope 限制页面标识大小。页面只经哈希进入文件路径
func ValidateScope(frame, page string) error {
	if !config.ValidName(frame) || len(page) == 0 || len(page) > 2048 || !utf8.ValidString(page) || strings.TrimSpace(page) == "" {
		return ErrInvalid
	}
	for _, r := range page {
		if unicode.IsControl(r) {
			return ErrInvalid
		}
	}
	return nil
}

// ValidateLink 仅允许带主机、无用户信息的绝对 HTTP(S) 网站
func ValidateLink(link *string) error {
	if link == nil {
		return nil
	}
	if len(*link) > 2048 || !utf8.ValidString(*link) {
		return ErrInvalid
	}
	u, err := url.Parse(*link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return fmt.Errorf("%w: 个人网站无效", ErrInvalid)
	}
	for _, r := range *link {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ErrInvalid
		}
	}
	return nil
}

// ValidateContent 拒绝超大、空白或含非法控制字符的正文，内容始终作为纯文本返回
func ValidateContent(content string, max int) error {
	if len(content) > max || strings.TrimSpace(content) == "" || !utf8.ValidString(content) {
		return ErrInvalid
	}
	for _, r := range content {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return ErrInvalid
		}
	}
	return nil
}

// ValidateAuthor 校验可信身份快照，避免把损坏的远端字段作为授权依据
func ValidateAuthor(a Author) error {
	if len(a.ID) == 0 || len(a.ID) > 256 || len(a.Name) == 0 || len(a.Name) > 256 || !utf8.ValidString(a.ID+a.Name) {
		return ErrInvalid
	}
	for _, r := range a.ID + a.Name {
		if unicode.IsControl(r) {
			return ErrInvalid
		}
	}
	if !strings.Contains(a.ID, "-") {
		return ErrInvalid
	}
	if a.EmailMD5 != nil {
		b, err := hex.DecodeString(*a.EmailMD5)
		if err != nil || len(b) != 16 || *a.EmailMD5 != strings.ToLower(*a.EmailMD5) {
			return ErrInvalid
		}
	}
	return ValidateLink(a.Link)
}

// PageHash 返回 UTF-8 页面标识的 SHA-256 小写十六进制
func PageHash(page string) string { h := sha256.Sum256([]byte(page)); return hex.EncodeToString(h[:]) }

// IndexKey 生成根列表或二级列表分区键
func IndexKey(frame, page, root string) cache.Key {
	return cache.Key{Frame: frame, PageHash: PageHash(page), Root: root}
}

// SortKey 提取已经验证过的评论排序键
func (c Comment) SortKey() index.SortKey {
	id, _ := index.ParseID(c.ID)
	return index.SortKey{CreatedAt: c.CreatedAt, ID: id}
}
