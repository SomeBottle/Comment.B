package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"

	"github.com/SomeBottle/Comment.B/backend/internal/store"
)

// frameBody 只允许客户端填写名称和简介，ID 与拥有者始终由服务端决定。
type frameBody struct {
	Name  string `json:"name"`
	Intro string `json:"intro"`
}

// frameJSON 严格解码有界 JSON，阻止请求通过 owner_id 或 id 冒充其他用户的框。
func frameJSON(w http.ResponseWriter, r *http.Request, body *frameBody) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, 415, "UNSUPPORTED_MEDIA_TYPE", "需要 application/json")
		return false
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		writeError(w, 413, "BODY_TOO_LARGE", "请求正文过大")
		return false
	}
	if err != nil || !utf8.Valid(b) {
		storeError(w, store.ErrInvalid)
		return false
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(body) != nil || d.Decode(new(any)) != io.EOF {
		storeError(w, store.ErrInvalid)
		return false
	}
	return true
}

// createFrame 对应旧版 create：检查创建资格与额度，服务端生成框 ID 并记录本人归属。
func (h *Handler) createFrame(w http.ResponseWriter, r *http.Request) {
	s, ok := h.mutation(w, r)
	if !ok {
		return
	}
	var body frameBody
	if !frameJSON(w, r, &body) {
		return
	}
	h.Store.Mu.Lock()
	frame, err := h.Store.CreateFrameLocked(s.Author.ID, body.Name, body.Intro, h.Config)
	h.Store.Mu.Unlock()
	if err != nil {
		storeError(w, err)
		return
	}
	h.Worker.Wake()
	w.Header().Set("Location", "/api/v1/frames/"+frame.ID)
	writeJSON(w, 201, struct {
		store.Frame
		Durability string `json:"durability"`
	}{frame, "local"})
}

// listFrames 对应旧版 getowned：仅列出当前用户的框，并提供当前创建资格和数量上限。
func (h *Handler) listFrames(w http.ResponseWriter, r *http.Request) {
	s, err := h.Auth.Session(r)
	if err != nil {
		writeError(w, 401, "UNAUTHENTICATED", "请先登录")
		return
	}
	h.Store.Mu.Lock()
	frames, err := h.Store.ListFramesLocked(s.Author.ID)
	h.Store.Mu.Unlock()
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, struct {
		Items     []store.Frame `json:"items"`
		CanCreate bool          `json:"can_create"`
		MaxFrames int           `json:"max_frames"`
	}{frames, h.Config.CanCreateFrame(s.Author.ID) && len(frames) < h.Config.MaxFramesPerUser, h.Config.MaxFramesPerUser})
}

// getFrame 对应旧版 getdetail，管理详情只对拥有者开放，普通用户可公开查询框内评论。
func (h *Handler) getFrame(w http.ResponseWriter, r *http.Request) {
	s, err := h.Auth.Session(r)
	if err != nil {
		writeError(w, 401, "UNAUTHENTICATED", "请先登录")
		return
	}
	h.Store.Mu.Lock()
	frame, err := h.Store.FrameLocked(r.PathValue("frame_id"))
	h.Store.Mu.Unlock()
	if err == nil && frame.OwnerID != s.Author.ID {
		err = store.ErrForbidden
	}
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, frame)
}

// updateFrame 对应旧版 edit，仅拥有者可更新名称与简介，已撤销创建资格也保留管理权。
func (h *Handler) updateFrame(w http.ResponseWriter, r *http.Request) {
	s, ok := h.mutation(w, r)
	if !ok {
		return
	}
	var body frameBody
	if !frameJSON(w, r, &body) {
		return
	}
	h.Store.Mu.Lock()
	frame, err := h.Store.UpdateFrameLocked(r.PathValue("frame_id"), s.Author.ID, body.Name, body.Intro)
	h.Store.Mu.Unlock()
	if err != nil {
		storeError(w, err)
		return
	}
	h.Worker.Wake()
	writeJSON(w, 200, frame)
}

// deleteFrame 对应旧版 del，拥有者可以删除整个框及所有页面评论，并释放本地额度。
func (h *Handler) deleteFrame(w http.ResponseWriter, r *http.Request) {
	s, ok := h.mutation(w, r)
	if !ok {
		return
	}
	h.Store.Mu.Lock()
	err := h.Store.DeleteFrameLocked(r.PathValue("frame_id"), s.Author.ID)
	h.Store.Mu.Unlock()
	if err != nil {
		storeError(w, err)
		return
	}
	h.Worker.Wake()
	w.WriteHeader(204)
}
