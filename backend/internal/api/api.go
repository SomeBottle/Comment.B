// Package api 提供两级独立分页、纯文本评论和运行状态 HTTP 接口
package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SomeBottle/Comment.B/backend/internal/auth"
	"github.com/SomeBottle/Comment.B/backend/internal/config"
	"github.com/SomeBottle/Comment.B/backend/internal/index"
	"github.com/SomeBottle/Comment.B/backend/internal/store"
	gitworker "github.com/SomeBottle/Comment.B/backend/internal/sync"
)

// Page 是一级列表、二级预览和后续二级分页的统一响应结构
type Page struct {
	Items      []Item  `json:"items"`
	Sort       string  `json:"sort"`
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

// Target 只展示回复对象名称，目标不需要出现在当前分页中
type Target struct {
	Name string `json:"name"`
}

// Item 嵌入持久化评论，查询附加字段不写回数据文件
type Item struct {
	store.Comment
	RepliesPreview     *Page   `json:"replies_preview,omitempty"`
	ReplyTargetDeleted *bool   `json:"reply_target_deleted,omitempty"`
	ReplyTarget        *Target `json:"reply_target,omitempty"`
}

// bucket 只按已认证身份计数，不保存请求 IP 或令牌
type bucket struct {
	Start time.Time
	Count int
}

// Handler 直接协调具体的 Store、Auth 和 Worker，不添加纯转发业务层
type Handler struct {
	Store   *store.Store
	Auth    *auth.Auth
	Worker  *gitworker.Worker
	Config  config.Config
	rateMu  sync.Mutex
	buckets map[string]bucket
}

// New 返回带来源白名单和安全响应头的 HTTP Handler
func New(s *store.Store, a *auth.Auth, w *gitworker.Worker, c config.Config) http.Handler {
	h := &Handler{Store: s, Auth: a, Worker: w, Config: c, buckets: make(map[string]bucket)}
	mux := http.NewServeMux()
	a.Register(mux)
	mux.HandleFunc("GET /api/v1/frames/{frame_id}/comments", h.listRoots)
	mux.HandleFunc("GET /api/v1/frames/{frame_id}/comments/{root_id}/replies", h.listReplies)
	mux.HandleFunc("POST /api/v1/frames/{frame_id}/comments", h.create)
	mux.HandleFunc("POST /api/v1/frames/{frame_id}/comments/{root_id}/replies", h.create)
	mux.HandleFunc("DELETE /api/v1/frames/{frame_id}/comments/{root_id}", h.delete)
	mux.HandleFunc("DELETE /api/v1/frames/{frame_id}/comments/{root_id}/replies/{comment_id}", h.delete)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", h.ready)
	mux.HandleFunc("GET /metrics", h.metrics)
	return h.middleware(mux)
}

// middleware 对预检和实际请求一致检查 CORS，Cookie 修改另有 CSRF 校验
func (h *Handler) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Add("Vary", "Origin")
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !h.Auth.AllowedOrigin(origin) {
				writeError(w, 403, "ORIGIN_FORBIDDEN", "来源未获允许")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		if r.Method == http.MethodOptions {
			if origin == "" {
				writeError(w, 403, "ORIGIN_FORBIDDEN", "需要来源")
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-CSRF-Token")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeJSON 使用标准 JSON 转义，正文永远不会以 HTML 响应
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError 保持前端可识别的错误码和中文消息
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

// storeError 不将文件路径或系统错误泄露到公开响应，损坏分区明确告警
func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, 404, "COMMENT_NOT_FOUND", "评论不存在")
	case errors.Is(err, store.ErrForbidden):
		writeError(w, 403, "FORBIDDEN", "没有删除权限")
	case errors.Is(err, store.ErrInvalid):
		writeError(w, 400, "INVALID_REQUEST", "请求参数无效")
	default:
		slog.Error("评论工作区操作失败", "error", err)
		writeError(w, 503, "STORE_UNAVAILABLE", "数据暂不可用")
	}
}

// frame 校验显式配置的评论框白名单，阻止任意目录膨胀
func (h *Handler) frame(r *http.Request) (string, error) {
	f := r.PathValue("frame_id")
	if !config.ValidName(f) {
		return "", store.ErrInvalid
	}
	for _, known := range h.Config.Frames {
		if f == known {
			return f, nil
		}
	}
	return "", store.ErrNotFound
}

// query 拒绝重复参数和损坏的 URL 编码，避免不同中间件对同一请求解释不同
func query(r *http.Request) (url.Values, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	for _, v := range q {
		if len(v) != 1 {
			return nil, store.ErrInvalid
		}
	}
	return q, nil
}

// order 只允许明确的稳定时间顺序
func order(s string) (string, error) {
	if s == "" {
		return "desc", nil
	}
	if s != "asc" && s != "desc" {
		return "", store.ErrInvalid
	}
	return s, nil
}

// limit 将三种独立默认数量分别限制在配置上限内
func limit(s string, defaultValue, max int) (int, error) {
	if s == "" {
		return defaultValue, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 1 || v > max {
		return 0, store.ErrInvalid
	}
	return v, nil
}

// listRoots 在同一工作区快照内返回一级分页和当前页根的二级预览
func (h *Handler) listRoots(w http.ResponseWriter, r *http.Request) {
	f, err := h.frame(r)
	if err != nil {
		storeError(w, err)
		return
	}
	q, err := query(r)
	if err != nil {
		storeError(w, store.ErrInvalid)
		return
	}
	page := q.Get("page_key")
	sort, err := order(q.Get("sort"))
	if err != nil {
		storeError(w, err)
		return
	}
	replySort, err := order(q.Get("reply_sort"))
	if err != nil {
		storeError(w, err)
		return
	}
	n, err := limit(q.Get("limit"), h.Config.FirstLevelPageSize, h.Config.MaxPageLimit)
	if err != nil {
		storeError(w, err)
		return
	}
	preview, err := limit(q.Get("preview_limit"), h.Config.SecondLevelPreviewSize, h.Config.MaxPreviewLimit)
	if err != nil {
		storeError(w, err)
		return
	}
	after, err := decodeCursor(h.Auth, q.Get("cursor"), f, page, "", sort)
	if err != nil {
		writeError(w, 400, "INVALID_CURSOR", "游标无效或查询范围已改变")
		return
	}
	h.Store.Mu.Lock()
	result, err := h.pageLocked(f, page, "", sort, after, n)
	if err == nil {
		for i := range result.Items {
			p, e := h.pageLocked(f, page, result.Items[i].ID, replySort, nil, preview)
			if e != nil {
				err = e
				break
			}
			result.Items[i].RepliesPreview = &p
		}
	}
	h.Store.Mu.Unlock()
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

// listReplies 接续预览边界，根不存在时不返回任何残留回复
func (h *Handler) listReplies(w http.ResponseWriter, r *http.Request) {
	f, err := h.frame(r)
	if err != nil {
		storeError(w, err)
		return
	}
	q, err := query(r)
	if err != nil {
		storeError(w, store.ErrInvalid)
		return
	}
	page := q.Get("page_key")
	root := r.PathValue("root_id")
	sort, err := order(q.Get("sort"))
	if err != nil {
		storeError(w, err)
		return
	}
	n, err := limit(q.Get("limit"), h.Config.SecondLevelPageSize, h.Config.MaxPageLimit)
	if err != nil {
		storeError(w, err)
		return
	}
	after, err := decodeCursor(h.Auth, q.Get("cursor"), f, page, root, sort)
	if err != nil {
		writeError(w, 400, "INVALID_CURSOR", "游标无效或查询范围已改变")
		return
	}
	h.Store.Mu.Lock()
	result, err := h.pageLocked(f, page, root, sort, after, n)
	h.Store.Mu.Unlock()
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

// pageLocked 使用实际文件判断回复目标是否删除，不能以当前分页结果代替存在性检查
func (h *Handler) pageLocked(frame, page, root, sort string, after *index.SortKey, n int) (Page, error) {
	comments, more, err := h.Store.ListLocked(frame, page, root, after, sort == "desc", n)
	result := Page{Items: make([]Item, 0, len(comments)), Sort: sort, HasMore: more}
	if err != nil {
		return result, err
	}
	for _, c := range comments {
		item := Item{Comment: c}
		if c.ReplyTo != nil {
			target, e := h.Store.ReadLocked(frame, page, root, *c.ReplyTo)
			deleted := errors.Is(e, store.ErrNotFound)
			if e != nil && !deleted {
				return result, e
			}
			item.ReplyTargetDeleted = &deleted
			if !deleted {
				item.ReplyTarget = &Target{Name: target.Author.Name}
			}
		}
		result.Items = append(result.Items, item)
	}
	if more && len(comments) > 0 {
		c := encodeCursor(h.Auth, frame, page, root, sort, comments[len(comments)-1].SortKey())
		result.NextCursor = &c
	}
	return result, nil
}

// mutation 验证会话和 CSRF；正文中的作者字段永远不作为身份来源
func (h *Handler) mutation(w http.ResponseWriter, r *http.Request) (auth.Session, bool) {
	s, err := h.Auth.Session(r)
	if err != nil {
		writeError(w, 401, "UNAUTHENTICATED", "请先登录")
		return s, false
	}
	if !h.Auth.CSRF(r, s) {
		writeError(w, 403, "CSRF_FAILED", "CSRF 校验失败")
		return s, false
	}
	return s, true
}

// allowCreate 使用有界身份窗口进行节点本地限流，不声称多节点全局限流
func (h *Handler) allowCreate(id string) bool {
	h.rateMu.Lock()
	defer h.rateMu.Unlock()
	now := time.Now()
	b := h.buckets[id]
	if now.Sub(b.Start) >= time.Minute {
		if len(h.buckets) >= 10000 {
			for k, v := range h.buckets {
				if now.Sub(v.Start) >= time.Minute {
					delete(h.buckets, k)
				}
			}
			if len(h.buckets) >= 10000 {
				return false
			}
		}
		b = bucket{Start: now}
	}
	if b.Count >= h.Config.RateLimitPerMinute {
		return false
	}
	b.Count++
	h.buckets[id] = b
	return true
}

// createBody 只接受设计公开的可写字段
type createBody struct {
	PageKey string  `json:"page_key"`
	Content string  `json:"content"`
	ReplyTo *string `json:"reply_to"`
	Link    *string `json:"link"`
}

// create 先限制正文、认证和输入，再在锁内生成服务端身份快照并持久化
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	f, err := h.frame(r)
	if err != nil {
		storeError(w, err)
		return
	}
	s, ok := h.mutation(w, r)
	if !ok {
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, 415, "UNSUPPORTED_MEDIA_TYPE", "需要 application/json")
		return
	}
	maxBody := int64(h.Config.MaxContentBytes*6 + 16384)
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, 413, "BODY_TOO_LARGE", "请求正文过大")
		return
	}
	if err != nil || !utf8.Valid(b) {
		storeError(w, store.ErrInvalid)
		return
	}
	var body createBody
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF || store.ValidateScope(f, body.PageKey) != nil || store.ValidateContent(body.Content, h.Config.MaxContentBytes) != nil || store.ValidateLink(body.Link) != nil {
		storeError(w, store.ErrInvalid)
		return
	}
	root := r.PathValue("root_id")
	if (root == "") != (body.ReplyTo == nil) {
		storeError(w, store.ErrInvalid)
		return
	}
	if !h.allowCreate(s.Author.ID) {
		w.Header().Set("Retry-After", "60")
		writeError(w, 429, "RATE_LIMITED", "评论过于频繁，请稍后重试")
		return
	}
	author := s.Author
	author.Link = body.Link
	h.Store.Mu.Lock()
	c, err := h.Store.CreateLocked(f, body.PageKey, root, body.ReplyTo, body.Content, author)
	h.Store.Mu.Unlock()
	if err != nil {
		storeError(w, err)
		return
	}
	h.Worker.Wake()
	writeJSON(w, 201, struct {
		store.Comment
		Durability string `json:"durability"`
	}{c, "local"})
}

// delete 在 Store 的同一锁内完成归属核查、授权和持久化删除
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	f, err := h.frame(r)
	if err != nil {
		storeError(w, err)
		return
	}
	s, ok := h.mutation(w, r)
	if !ok {
		return
	}
	q, err := query(r)
	if err != nil {
		storeError(w, store.ErrInvalid)
		return
	}
	root := r.PathValue("root_id")
	id := r.PathValue("comment_id")
	if id == "" {
		id = root
	}
	if r.PathValue("comment_id") != "" && id == root {
		storeError(w, store.ErrInvalid)
		return
	}
	h.Store.Mu.Lock()
	err = h.Store.DeleteLocked(f, q.Get("page_key"), root, id, s.Author.ID, h.Auth.IsAdmin(s.Author.ID))
	h.Store.Mu.Unlock()
	if err != nil {
		storeError(w, err)
		return
	}
	h.Worker.Wake()
	w.WriteHeader(204)
}

// ready 网络退化时仍返回本地就绪；文件树不可用时返回 503
func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	h.Store.Mu.Lock()
	available := h.Store.CheckLocked() == nil
	status := h.Worker.StatusLocked()
	h.Store.Mu.Unlock()
	code := 200
	if !available {
		code = 503
	}
	writeJSON(w, code, struct {
		LocalReady bool             `json:"local_ready"`
		Sync       gitworker.Status `json:"sync"`
	}{available, status})
}

// metrics 输出无需依赖的 Prometheus 文本计数，不附带用户或页面标签
func (h *Handler) metrics(w http.ResponseWriter, r *http.Request) {
	h.Store.Mu.Lock()
	c := h.Store.Cache.Stats()
	comments := h.Store.CommentCacheStatsLocked()
	s := h.Worker.StatusLocked()
	pending := h.Store.Pending
	repairs := h.Store.Repairs
	ready := h.Store.Blocked == nil
	h.Store.Mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	values := []struct {
		Name  string
		Value any
	}{
		{"commentb_index_cache_bytes", c.Bytes}, {"commentb_index_cache_entries", c.Entries}, {"commentb_index_cache_hits_total", c.Hits}, {"commentb_index_cache_misses_total", c.Misses}, {"commentb_index_builds_total", c.Builds}, {"commentb_index_invalidations_total", c.Invalidations}, {"commentb_index_evictions_total", c.Evictions},
		{"commentb_comment_cache_bytes", comments.Bytes}, {"commentb_comment_cache_entries", comments.Entries}, {"commentb_comment_cache_hits_total", comments.Hits}, {"commentb_comment_cache_misses_total", comments.Misses}, {"commentb_comment_cache_evictions_total", comments.Evictions}, {"commentb_comment_cache_invalidations_total", comments.Invalidations}, {"commentb_comment_file_reads_total", comments.DiskReads},
		{"commentb_pending_changes", pending}, {"commentb_pending_commits", s.PendingCommits}, {"commentb_sync_failures_total", s.Failures}, {"commentb_sync_conflicts_total", s.Conflicts}, {"commentb_orphan_repairs_total", repairs}, {"commentb_last_fetch_timestamp_seconds", timestamp(s.LastFetch)}, {"commentb_last_push_timestamp_seconds", timestamp(s.LastPush)},
	}
	for _, v := range values {
		fmt.Fprintf(w, "%s %v\n", v.Name, v.Value)
	}
	fmt.Fprintf(w, "commentb_local_ready %d\ncommentb_sync_degraded %d\n", boolean(ready), boolean(s.Degraded))
}

// timestamp 将尚未成功同步的零时间表示为 0
func timestamp(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// boolean 将运行状态转换为 Prometheus 数值
func boolean(v bool) int {
	if v {
		return 1
	}
	return 0
}
