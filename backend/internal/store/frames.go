package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SomeBottle/Comment.B/backend/internal/config"
	"github.com/SomeBottle/Comment.B/backend/internal/index"
)

// ErrFrameNotFound 区分不存在或已删除的评论框，同时兼容 Store 的不存在语义
var ErrFrameNotFound = fmt.Errorf("%w: 评论框不存在", ErrNotFound)

// ErrFrameLimit 表示当前用户已用完可创建的评论框额度
var ErrFrameLimit = errors.New("已达到评论框创建数量上限")

// Frame 保存独立评论框的归属，拥有者只管理本框，不具有站点级管理员角色
// DeletedAt 保留删除哨兵，防止其他节点的迟到评论让已删除框复活
type Frame struct {
	Version   int    `json:"version"`
	ID        string `json:"id"`
	OwnerID   string `json:"owner_id"`
	Name      string `json:"name"`
	Intro     string `json:"intro"`
	CreatedAt int64  `json:"created_at"`
	DeletedAt int64  `json:"deleted_at,omitempty"`
}

// validateFrameText 沿用旧版网站名称与简介的用途，以字符数限制为 20 / 100
func validateFrameText(name, intro string) error {
	if ValidateContent(name, 80) != nil || ValidateContent(intro, 400) != nil || utf8.RuneCountInString(name) > 20 || utf8.RuneCountInString(intro) > 100 {
		return ErrInvalid
	}
	return nil
}

// ParseFramePath 仅接受 frames/<受限 ID>.json，不能把任意远端路径解释为元数据
func ParseFramePath(p string) (string, bool) {
	v := strings.Split(p, "/")
	if len(v) != 2 || v[0] != "frames" || !strings.HasSuffix(v[1], ".json") {
		return "", false
	}
	id := strings.TrimSuffix(v[1], ".json")
	return id, config.ValidName(id)
}

// readFrame 验证元数据文件与完整身份，缓存命中时不再打开磁盘文件
func (s *Store) readFrame(id string) (Frame, error) {
	if !config.ValidName(id) {
		return Frame{}, ErrInvalid
	}
	if f, ok := s.frames[id]; ok {
		return f, nil
	}
	p := "frames/" + id + ".json"
	if err := s.safePath(p); err != nil {
		return Frame{}, err
	}
	info, err := s.root.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return Frame{}, ErrFrameNotFound
	}
	if err != nil {
		return Frame{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8192 {
		return Frame{}, fmt.Errorf("%w: 评论框元数据类型或大小异常", ErrCorrupt)
	}
	f, err := s.root.Open(p)
	if err != nil {
		return Frame{}, err
	}
	defer f.Close()
	var frame Frame
	d := json.NewDecoder(io.LimitReader(f, 8193))
	d.DisallowUnknownFields()
	if d.Decode(&frame) != nil || d.Decode(new(any)) != io.EOF || frame.Version != 1 || frame.ID != id || !config.ValidUserID(frame.OwnerID) || validateFrameText(frame.Name, frame.Intro) != nil || frame.CreatedAt <= 0 || frame.CreatedAt > 9007199254740991 || frame.DeletedAt < 0 || frame.DeletedAt > 9007199254740991 {
		return Frame{}, fmt.Errorf("%w: 评论框元数据无效 %s", ErrCorrupt, p)
	}
	// frames 采用固定小容量，避免读取新框使全部框身份常驻内存
	if len(s.frames) >= 256 {
		clear(s.frames)
	}
	s.frames[id] = frame
	return frame, nil
}

// FrameLocked 返回有效评论框，未注册目录和删除哨兵都不能用于读取或新增评论
func (s *Store) FrameLocked(id string) (Frame, error) {
	if err := s.CheckLocked(); err != nil {
		return Frame{}, err
	}
	f, err := s.readFrame(id)
	if err != nil {
		return Frame{}, err
	}
	if f.DeletedAt != 0 {
		return Frame{}, ErrFrameNotFound
	}
	return f, nil
}

// ListFramesLocked 只扫描小型框元数据，返回本人当前有效的评论框，不扫描评论目录
func (s *Store) ListFramesLocked(owner string) ([]Frame, error) {
	if err := s.CheckLocked(); err != nil {
		return nil, err
	}
	if !config.ValidUserID(owner) {
		return nil, ErrInvalid
	}
	files, err := s.readDir("frames")
	if err != nil {
		return nil, err
	}
	frames := []Frame{}
	for _, file := range files {
		id, ok := ParseFramePath("frames/" + file.Name())
		if !ok {
			return nil, fmt.Errorf("%w: 非法评论框元数据路径", ErrCorrupt)
		}
		f, err := s.readFrame(id)
		if err != nil {
			return nil, err
		}
		if f.OwnerID == owner && f.DeletedAt == 0 {
			frames = append(frames, f)
		}
	}
	sort.Slice(frames, func(i, j int) bool {
		if frames[i].CreatedAt == frames[j].CreatedAt {
			return frames[i].ID < frames[j].ID
		}
		return frames[i].CreatedAt < frames[j].CreatedAt
	})
	return frames, nil
}

// CreateFrameLocked 在同一工作区锁内检查创建资格与当前数量，再生成不可伪造归属的 ID
// 多节点断网可分别消耗同一额度，本地限制遵循现有最终一致性模型
func (s *Store) CreateFrameLocked(owner, name, intro string, policy config.Config) (Frame, error) {
	if err := s.CheckLocked(); err != nil {
		return Frame{}, err
	}
	if !policy.CanCreateFrame(owner) {
		return Frame{}, ErrForbidden
	}
	if validateFrameText(name, intro) != nil || policy.MaxFramesPerUser < 1 {
		return Frame{}, ErrInvalid
	}
	owned, err := s.ListFramesLocked(owner)
	if err != nil {
		return Frame{}, err
	}
	if len(owned) >= policy.MaxFramesPerUser {
		return Frame{}, ErrFrameLimit
	}
	for attempt := 0; attempt < 10; attempt++ {
		id, err := index.NewID()
		if err != nil {
			return Frame{}, err
		}
		frame := Frame{Version: 1, ID: id.String(), OwnerID: owner, Name: name, Intro: intro, CreatedAt: time.Now().UnixMicro()}
		if err = s.writeJSON("frames/"+frame.ID+".json", frame, false); errors.Is(err, os.ErrExist) {
			continue
		} else if err != nil {
			return Frame{}, err
		}
		s.Pending++
		return frame, nil
	}
	return Frame{}, errors.New("重复随机评论框 ID 碰撞")
}

// UpdateFrameLocked 仅由拥有者修改名称和简介，不能从请求中转移所有权
func (s *Store) UpdateFrameLocked(id, actor, name, intro string) (Frame, error) {
	f, err := s.FrameLocked(id)
	if err != nil {
		return Frame{}, err
	}
	if f.OwnerID != actor {
		return Frame{}, ErrForbidden
	}
	if err = validateFrameText(name, intro); err != nil {
		return Frame{}, err
	}
	f.Name = name
	f.Intro = intro
	if err = s.writeJSON("frames/"+id+".json", f, true); err != nil {
		return Frame{}, err
	}
	delete(s.frames, id)
	s.Pending++
	return f, nil
}

// invalidateFrame 清除框身份及所属正文和排序缓存，其他框的热点不受影响
func (s *Store) invalidateFrame(id string) {
	delete(s.frames, id)
	s.Cache.InvalidateFrame(id)
	s.comments.invalidateRoot("comments/" + id)
}

// removeFrameComments 将整框评论移入私有删除区，不逐条遍历或改写 JSON
func (s *Store) removeFrameComments(id string) error {
	dir := "comments/" + id
	if err := s.safePath(dir); err != nil {
		return err
	}
	if _, err := s.root.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := s.moveToTrash(dir); err != nil {
		return err
	}
	s.Pending++
	return nil
}

// DeleteFrameLocked 先持久化删除哨兵，再移走全部评论；中断后也不会恢复框或泄露残留
func (s *Store) DeleteFrameLocked(id, actor string) error {
	f, err := s.FrameLocked(id)
	if err != nil {
		return err
	}
	if f.OwnerID != actor {
		return ErrForbidden
	}
	f.DeletedAt = time.Now().UnixMicro()
	if err = s.writeJSON("frames/"+id+".json", f, true); err != nil {
		return err
	}
	s.Pending++
	s.invalidateFrame(id)
	if err = s.removeFrameComments(id); err != nil {
		return s.failDurability(err)
	}
	return nil
}

// RepairFramesLocked 验证本轮变更涉及的框，并清理由其他节点迟到写入的已删除框评论
func (s *Store) RepairFramesLocked(paths []string) error {
	seen := map[string]bool{}
	for _, p := range paths {
		id, ok := ParseFramePath(p)
		if !ok {
			k, _, valid := ParseCommentPath(p)
			if !valid {
				return ErrInvalid
			}
			id = k.Frame
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		delete(s.frames, id) // 修复必须重新读取整合后的归属，不能用旧的有效哨兵
		f, err := s.readFrame(id)
		if err != nil {
			return err
		}
		if f.DeletedAt != 0 {
			s.invalidateFrame(id)
			if err = s.removeFrameComments(id); err != nil {
				return err
			}
		}
	}
	return nil
}
