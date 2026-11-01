package store

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/SomeBottle/Comment.B/backend/internal/cache"
	"github.com/SomeBottle/Comment.B/backend/internal/config"
	"github.com/SomeBottle/Comment.B/backend/internal/index"
)

// Store 的所有 Locked 方法必须持有 Mu，同一锁也覆盖 Git 和缓存构建
type Store struct {
	Mu         sync.Mutex       // Mu 保护整个工作区快照，第一版优先保证正确性
	Cache      *cache.Cache     // Cache 不保存正文，不写入 Git
	comments   *commentCache    // comments 以独立预算缓存已解析正文，所有操作与文件写入共用 Mu
	frames     map[string]Frame // frames 是最多 256 条的元数据缓存，整合变更时精确失效
	Pending    int              // Pending 是自上次提交以来的本地变更次数
	Repairs    uint64           // Repairs 记录已经移除的无根残留目录数
	Blocked    error            // Blocked 非空时禁止读取和修改不确定的文件树
	root       *os.Root
	lock       *os.File
	maxContent int
}

// Open 验证格式并获取独占进程锁，不扫描 comments 仓库
func Open(dir string, indexBudget, commentBudget int64, maxContent int) (*Store, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{root: r, Cache: cache.New(indexBudget), comments: newCommentCache(commentBudget), frames: make(map[string]Frame), maxContent: maxContent}
	if err = s.safePath(".git"); err != nil {
		r.Close()
		return nil, err
	}
	f, err := r.OpenFile(".git/commentb.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		r.Close()
		return nil, err
	}
	s.lock = f
	if err = lockWorkspace(f); err != nil {
		s.Close()
		return nil, err
	}
	for _, p := range []string{".git/commentb-trash", ".git/commentb-tmp", "comments", "frames"} {
		if err = s.mkdirAll(p); err != nil {
			s.Close()
			return nil, err
		}
	}
	if err = s.safePath("format.json"); err != nil {
		s.Close()
		return nil, err
	}
	b, err := r.ReadFile("format.json")
	var format struct {
		Version int `json:"format_version"`
	}
	if err != nil || json.Unmarshal(b, &format) != nil || format.Version != 1 {
		s.Close()
		return nil, errors.New("format.json 缺失或数据版本不是 1；禁止隐式迁移")
	}
	return s, nil
}

// Close 释放文件描述符和进程锁；调用前应停止 HTTP 和 Worker
func (s *Store) Close() error {
	if s.lock != nil {
		s.lock.Close()
	}
	return s.root.Close()
}

// CheckLocked 在 Git 冲突或持久化失败后拒绝发布不确定状态
func (s *Store) CheckLocked() error {
	if s.Blocked != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, s.Blocked)
	}
	return nil
}

// safePath 拒绝任意路径段中的符号链接。os.Root 额外限制越界访问
func (s *Store) safePath(p string) error {
	if p != path.Clean(p) || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || p == ".." || strings.HasPrefix(p, "../") {
		return ErrInvalid
	}
	parts := strings.Split(p, "/")
	for i := range parts {
		info, err := s.root.Lstat(strings.Join(parts[:i+1], "/"))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: 不允许符号链接 %s", ErrCorrupt, p)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("%w: 路径段不是目录 %s", ErrCorrupt, p)
		}
	}
	return nil
}

// syncDir 在确认成功前同步目录元数据。不静默忽略不支持 fsync 的文件系统
func (s *Store) syncDir(p string) error {
	f, err := s.root.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// mkdirAll 逐层创建并同步父目录，保证新路径不会只停留在内存元数据中
func (s *Store) mkdirAll(p string) error {
	if err := s.safePath(p); err != nil {
		return err
	}
	parts := strings.Split(p, "/")
	for i := range parts {
		v := strings.Join(parts[:i+1], "/")
		err := s.root.Mkdir(v, 0700)
		created := err == nil // created 只同步本次新建的路径，避免每次评论写入重复 fsync 全部祖先
		if err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := s.root.Lstat(v)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: 非目录 %s", ErrCorrupt, v)
		}
		if created {
			if err = s.syncDir(path.Dir(v)); err != nil {
				return s.failDurability(err)
			}
		}
	}
	return nil
}

// failDurability 停止对不确定持久化结果继续提供服务，保留现场供恢复
func (s *Store) failDurability(err error) error {
	s.Blocked = err
	s.ClearCachesLocked()
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

// writeJSON 先同步临时文件，评论和新框独占发布；只有框元数据可原子替换。
func (s *Store) writeJSON(p string, value any, replace bool) error {
	if err := s.safePath(p); err != nil {
		return err
	}
	id, err := index.NewID()
	if err != nil {
		return err
	}
	tmp := ".git/commentb-tmp/" + id.String()
	f, err := s.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer s.root.Remove(tmp)
	if err = json.NewEncoder(f).Encode(value); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if replace {
		err = s.root.Rename(tmp, p)
	} else {
		err = s.root.Link(tmp, p)
	}
	if err != nil {
		return err
	}
	if err = s.syncDir(path.Dir(p)); err != nil {
		return s.failDurability(err)
	}
	return nil
}

// rootPath 根据已验证的分区构造唯一目录
func rootPath(frame, hash, root string) string {
	return path.Join("comments", frame, hash[:2], hash, "roots", root[:2], root)
}

// scope 校验所有进入路径的请求字段
func scope(frame, page, root string) error {
	if err := ValidateScope(frame, page); err != nil {
		return err
	}
	if root != "" {
		_, err := index.ParseID(root)
		return err
	}
	return nil
}

// read 验证文件类型、大小、JSON 字段及路径归属，损坏数据不会被静默遗漏
func (s *Store) read(p, frame, page, root, id string) (Comment, error) {
	var c Comment
	if err := s.safePath(p); err != nil {
		return c, err
	}
	// info 在打开前拒绝管道和设备，避免损坏分区使文件读取永久阻塞
	info, err := s.root.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	if !info.Mode().IsRegular() || info.Size() > 512<<10 {
		return c, fmt.Errorf("%w: 文件类型或大小异常 %s", ErrCorrupt, p)
	}
	f, err := s.root.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	defer f.Close()
	s.comments.stats.DiskReads++
	info, err = f.Stat()
	if err != nil {
		return c, err
	}
	if !info.Mode().IsRegular() || info.Size() > 512<<10 {
		return c, fmt.Errorf("%w: 文件类型或大小异常 %s", ErrCorrupt, p)
	}
	d := json.NewDecoder(io.LimitReader(f, 512<<10))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, fmt.Errorf("%w: %s: %v", ErrCorrupt, p, err)
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("%w: 多余 JSON %s", ErrCorrupt, p)
	}
	if c.Version != 1 || c.ID != id || c.FrameID != frame || c.PageKey != page || c.RootID != root || c.CreatedAt <= 0 || c.CreatedAt > 9007199254740991 || ValidateAuthor(c.Author) != nil || ValidateContent(c.Content, 65536) != nil {
		return c, fmt.Errorf("%w: 字段或路径归属无效 %s", ErrCorrupt, p)
	}
	if id == root {
		if c.ReplyTo != nil {
			return c, fmt.Errorf("%w: 一级评论包含 reply_to", ErrCorrupt)
		}
	} else {
		if c.ReplyTo == nil || *c.ReplyTo == id {
			return c, fmt.Errorf("%w: 二级评论缺失有效 reply_to", ErrCorrupt)
		}
		if _, err = index.ParseID(*c.ReplyTo); err != nil {
			return c, fmt.Errorf("%w: reply_to 编码无效", ErrCorrupt)
		}
	}
	return c, nil
}

// ReadLocked 先核查根哨兵再读取目标，两者均可命中正文缓存。祖先二级评论不影响目标生存
func (s *Store) ReadLocked(frame, page, root, id string) (Comment, error) {
	if err := s.CheckLocked(); err != nil {
		return Comment{}, err
	}
	if err := scope(frame, page, root); err != nil {
		return Comment{}, ErrInvalid
	}
	if _, err := s.FrameLocked(frame); err != nil {
		return Comment{}, err
	}
	if _, err := index.ParseID(id); err != nil {
		return Comment{}, ErrInvalid
	}
	dir := rootPath(frame, PageHash(page), root)
	c, err := s.readCached(dir+"/root.json", frame, page, root, root)
	if errors.Is(err, ErrNotFound) {
		if repairErr := s.RepairLocked([]string{dir}); repairErr != nil {
			return Comment{}, repairErr
		}
		return Comment{}, ErrNotFound
	}
	if err != nil || id == root {
		return c, err
	}
	return s.readCached(dir+"/replies/"+id+".json", frame, page, root, id)
}

// CreateLocked 创建根或二级文件，只对已经缓存的分区增量更新
func (s *Store) CreateLocked(frame, page, root string, replyTo *string, content string, author Author) (Comment, error) {
	var c Comment
	if err := s.CheckLocked(); err != nil {
		return c, err
	}
	if scope(frame, page, root) != nil || ValidateContent(content, s.maxContent) != nil || ValidateAuthor(author) != nil {
		return c, ErrInvalid
	}
	if _, err := s.FrameLocked(frame); err != nil {
		return c, err
	}
	if root == "" && replyTo != nil {
		return c, ErrInvalid
	}
	if root != "" {
		if replyTo == nil {
			return c, ErrInvalid
		}
		if _, err := s.ReadLocked(frame, page, root, *replyTo); err != nil {
			return c, err
		}
	}
	for attempt := 0; attempt < 10; attempt++ {
		id, err := index.NewID()
		if err != nil {
			return c, err
		}
		rootID := root
		if rootID == "" {
			rootID = id.String()
		}
		dir := rootPath(frame, PageHash(page), rootID)
		if err = s.mkdirAll(path.Dir(dir)); err != nil {
			return c, err
		}
		p := dir + "/root.json"
		if root == "" {
			if err = s.root.Mkdir(dir, 0700); errors.Is(err, os.ErrExist) {
				continue
			}
			if err != nil {
				return c, err
			}
			if err = s.syncDir(path.Dir(dir)); err != nil {
				return c, s.failDurability(err)
			}
		} else {
			if id.String() == root {
				continue
			}
			if err = s.mkdirAll(dir + "/replies"); err != nil {
				return c, err
			}
			p = dir + "/replies/" + id.String() + ".json"
		}
		c = Comment{Version: 1, ID: id.String(), FrameID: frame, PageKey: page, Author: author, RootID: rootID, ReplyTo: replyTo, Content: content, CreatedAt: time.Now().UnixMicro()}
		if err = s.writeJSON(p, c, false); errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return Comment{}, err
		}
		s.Pending++
		s.Cache.InsertIfCached(IndexKey(frame, page, root), c.SortKey())
		s.comments.put(p, c)
		return c, nil
	}
	return Comment{}, errors.New("重复随机 ID 碰撞")
}

// moveToTrash 以一次目录重命名移除整组评论，再同步源和目标父目录
func (s *Store) moveToTrash(dir string) error {
	if err := s.safePath(dir); err != nil {
		return err
	}
	id, err := index.NewID()
	if err != nil {
		return err
	}
	if err = s.root.Rename(dir, ".git/commentb-trash/"+id.String()); err != nil {
		return err
	}
	s.comments.invalidateRoot(dir)
	if err = s.syncDir(path.Dir(dir)); err != nil {
		return s.failDurability(err)
	}
	if err = s.syncDir(".git/commentb-trash"); err != nil {
		return s.failDurability(err)
	}
	return nil
}

// DeleteLocked 在锁内重新验证完整作者身份。根删除会级联移走所有作者的回复
func (s *Store) DeleteLocked(frame, page, root, id, actor string) error {
	c, err := s.ReadLocked(frame, page, root, id)
	if err != nil {
		return err
	}
	f, err := s.FrameLocked(frame)
	if err != nil {
		return err
	}
	if f.OwnerID != actor && c.Author.ID != actor {
		return ErrForbidden
	}
	dir := rootPath(frame, PageHash(page), root)
	if id == root {
		if err = s.moveToTrash(dir); err != nil {
			return err
		}
		s.Cache.RemoveIfCached(IndexKey(frame, page, ""), c.SortKey())
		s.Cache.Invalidate(IndexKey(frame, page, root))
	} else {
		if err = s.root.Remove(dir + "/replies/" + id + ".json"); err != nil {
			return err
		}
		s.comments.invalidate(dir + "/replies/" + id + ".json")
		if err = s.syncDir(dir + "/replies"); err != nil {
			return s.failDurability(err)
		}
		s.Cache.RemoveIfCached(IndexKey(frame, page, root), c.SortKey())
	}
	s.Pending++
	return nil
}

// readDir 只读取已验证目录，不跟随符号链接
func (s *Store) readDir(p string) ([]os.DirEntry, error) {
	if err := s.safePath(p); err != nil {
		return nil, err
	}
	f, err := s.root.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

// buildRoots 仅扫描当前页面的根文件。不会读取未展示根的二级正文
func (s *Store) buildRoots(frame, page string) (*index.SortedIndex, error) {
	hash := PageHash(page)
	base := path.Join("comments", frame, hash[:2], hash, "roots")
	shards, err := s.readDir(base)
	if err != nil {
		return nil, err
	}
	keys := []index.SortKey{}
	for _, shard := range shards {
		if !shard.IsDir() || len(shard.Name()) != 2 {
			return nil, fmt.Errorf("%w: 根分片目录异常", ErrCorrupt)
		}
		dirs, err := s.readDir(base + "/" + shard.Name())
		if err != nil {
			return nil, err
		}
		for _, d := range dirs {
			id := d.Name()
			if _, err = index.ParseID(id); err != nil || !d.IsDir() || id[:2] != shard.Name() {
				return nil, fmt.Errorf("%w: 根目录异常", ErrCorrupt)
			}
			// 冷扫描只提取排序键，不将整个页面正文装入缓存挤出实际展示的热点。
			dir := rootPath(frame, hash, id)
			c, err := s.read(dir+"/root.json", frame, page, id, id)
			if errors.Is(err, ErrNotFound) {
				if err = s.RepairLocked([]string{dir}); err != nil {
					return nil, err
				}
				continue
			}
			if err != nil {
				return nil, err
			}
			keys = append(keys, c.SortKey())
		}
	}
	return index.New(keys), nil
}

// buildReplies 只扫描指定根的二级排序键，正文不会存入缓存
func (s *Store) buildReplies(frame, page, root string) (*index.SortedIndex, error) {
	base := rootPath(frame, PageHash(page), root) + "/replies"
	files, err := s.readDir(base)
	if err != nil {
		return nil, err
	}
	keys := make([]index.SortKey, 0, len(files))
	for _, f := range files {
		id := strings.TrimSuffix(f.Name(), ".json")
		if _, err = index.ParseID(id); err != nil || f.Name() != id+".json" || f.Type() != 0 || id == root {
			return nil, fmt.Errorf("%w: 二级文件名或类型异常", ErrCorrupt)
		}
		c, err := s.read(base+"/"+f.Name(), frame, page, root, id)
		if err != nil {
			return nil, err
		}
		keys = append(keys, c.SortKey())
	}
	return index.New(keys), nil
}

// ListLocked 分页只读取所选评论正文，冷缓存构建与并发写入共享同一锁
func (s *Store) ListLocked(frame, page, root string, after *index.SortKey, desc bool, limit int) ([]Comment, bool, error) {
	if err := s.CheckLocked(); err != nil {
		return nil, false, err
	}
	if scope(frame, page, root) != nil || limit < 1 || limit > 500 {
		return nil, false, ErrInvalid
	}
	if _, err := s.FrameLocked(frame); err != nil {
		return nil, false, err
	}
	if root != "" {
		if _, err := s.ReadLocked(frame, page, root, root); err != nil {
			return nil, false, err
		}
	}
	k := IndexKey(frame, page, root)
	idx, err := s.Cache.GetOrBuild(k, func() (*index.SortedIndex, error) {
		if root == "" {
			return s.buildRoots(frame, page)
		}
		return s.buildReplies(frame, page, root)
	})
	if err != nil {
		return nil, false, err
	}
	keys, more := idx.Page(after, desc, limit)
	items := make([]Comment, 0, len(keys))
	for _, key := range keys {
		rootID := root
		if rootID == "" {
			rootID = key.ID.String()
		}
		c, err := s.ReadLocked(frame, page, rootID, key.ID.String())
		if err != nil || c.SortKey() != key {
			s.Cache.Invalidate(k)
			s.comments.invalidateRoot(rootPath(frame, PageHash(page), rootID))
			return nil, false, fmt.Errorf("%w: 索引和文件不一致: %v", ErrCorrupt, err)
		}
		items = append(items, c)
	}
	return items, more, nil
}

// ParseCommentPath 严格解析 Git diff 路径，不将远端任意路径用于删除
func ParseCommentPath(p string) (cache.Key, string, bool) {
	v := strings.Split(p, "/")
	if len(v) != 8 && len(v) != 9 {
		return cache.Key{}, "", false
	}
	if v[0] != "comments" || !config.ValidName(v[1]) || len(v[3]) != 64 || v[2] != v[3][:2] || v[4] != "roots" {
		return cache.Key{}, "", false
	}
	b, err := hex.DecodeString(v[3])
	if err != nil || len(b) != 32 || strings.ToLower(v[3]) != v[3] {
		return cache.Key{}, "", false
	}
	if _, err = index.ParseID(v[6]); err != nil || v[5] != v[6][:2] {
		return cache.Key{}, "", false
	}
	k := cache.Key{Frame: v[1], PageHash: v[3], Root: v[6]}
	if len(v) == 8 && v[7] == "root.json" {
		return k, strings.Join(v[:7], "/"), true
	}
	if len(v) == 9 && v[7] == "replies" && strings.HasSuffix(v[8], ".json") {
		if _, err = index.ParseID(strings.TrimSuffix(v[8], ".json")); err == nil {
			return k, strings.Join(v[:7], "/"), true
		}
	}
	return cache.Key{}, "", false
}

// InvalidatePathsLocked 只失效文件树发生变化的分区
func (s *Store) InvalidatePathsLocked(paths []string) {
	for _, p := range paths {
		if id, ok := ParseFramePath(p); ok {
			s.invalidateFrame(id)
			continue
		}
		k, dir, ok := ParseCommentPath(p)
		if !ok {
			s.ClearCachesLocked()
			continue
		}
		s.Cache.Invalidate(k)
		if strings.HasSuffix(p, "/root.json") {
			s.comments.invalidateRoot(dir)
			k.Root = ""
			s.Cache.Invalidate(k)
		} else {
			s.comments.invalidate(p)
		}
	}
}

// RepairLocked 仅修复已识别目录，根损坏需要告警，根缺失才允许清理迟到回复
func (s *Store) RepairLocked(dirs []string) error {
	seen := map[string]bool{}
	for _, dir := range dirs {
		if seen[dir] {
			continue
		}
		seen[dir] = true
		k, parsed, ok := ParseCommentPath(dir + "/root.json")
		if !ok || parsed != dir {
			return ErrInvalid
		}
		if err := s.safePath(dir + "/root.json"); err != nil {
			return err
		}
		if _, err := s.root.Lstat(dir); errors.Is(err, os.ErrNotExist) {
			s.comments.invalidateRoot(dir)
			continue
		} else if err != nil {
			return err
		}
		info, statErr := s.root.Lstat(dir + "/root.json")
		if statErr == nil && (!info.Mode().IsRegular() || info.Size() > 512<<10) {
			return fmt.Errorf("%w: 根文件类型或大小异常 %s", ErrCorrupt, dir)
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		b, err := s.root.ReadFile(dir + "/root.json")
		if err == nil {
			s.comments.stats.DiskReads++
			var c Comment
			if json.Unmarshal(b, &c) != nil || ValidateScope(c.FrameID, c.PageKey) != nil || PageHash(c.PageKey) != k.PageHash {
				return fmt.Errorf("%w: 根文件损坏 %s", ErrCorrupt, dir)
			}
			if _, err = s.read(dir+"/root.json", k.Frame, c.PageKey, k.Root, k.Root); err != nil {
				return err
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = s.moveToTrash(dir); err != nil {
			return err
		}
		s.Pending++
		s.Repairs++
		s.Cache.Invalidate(k)
		k.Root = ""
		s.Cache.Invalidate(k)
	}
	return nil
}

// CleanupLocked 仅在本地提交成功后清理私有回收区。重启时永远不会恢复已删除根
func (s *Store) CleanupLocked() error {
	for _, dir := range []string{".git/commentb-trash", ".git/commentb-tmp"} {
		entries, err := s.readDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err = s.root.RemoveAll(dir + "/" + e.Name()); err != nil {
				return err
			}
		}
		if err = s.syncDir(dir); err != nil {
			return err
		}
	}
	return nil
}
