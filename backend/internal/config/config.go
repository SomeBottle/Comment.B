// Package config 加载数据仓库之外的配置；密钥仅从环境变量读取
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Duration 使用可读的 Go 时长字符串，例如 "30s"
type Duration time.Duration

// UnmarshalJSON 拒绝没有单位的数值，避免误配纳秒
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err == nil {
		*d = Duration(v)
	}
	return err
}

// GitHub 配置唯一支持的 GitHub OAuth 登录来源
type GitHub struct {
	ClientID        string `json:"client_id"`
	ClientSecretEnv string `json:"client_secret_env"`
}

// Config 包含单节点运行参数；数据仓库中的 JSON 不包含这些配置
type Config struct {
	Listen                 string   `json:"listen"`
	DataDir                string   `json:"data_dir"`
	PublicURL              string   `json:"public_url"`
	Frames                 []string `json:"frames"`
	AllowedOrigins         []string `json:"allowed_origins"`
	Admins                 []string `json:"admins"`
	SessionKeyEnv          string   `json:"session_key_env"`
	SessionTTL             Duration `json:"session_ttl"`
	SecureCookies          bool     `json:"secure_cookies"`
	GitHub                 GitHub   `json:"github"`
	FirstLevelPageSize     int      `json:"first_level_page_size"`
	SecondLevelPreviewSize int      `json:"second_level_preview_size"`
	SecondLevelPageSize    int      `json:"second_level_page_size"`
	MaxPageLimit           int      `json:"max_page_limit"`
	MaxPreviewLimit        int      `json:"max_preview_limit"`
	MaxContentBytes        int      `json:"max_content_bytes"`
	IndexCacheBudget       int64    `json:"index_cache_budget"`
	CommentCacheBudget     int64    `json:"comment_cache_budget"` // CommentCacheBudget 是已解析正文的独立字节预算，零表示禁用。
	RateLimitPerMinute     int      `json:"rate_limit_per_minute"`
	CommitInterval         Duration `json:"commit_interval"`
	CommitMaxChanges       int      `json:"commit_max_changes"`
	FetchInterval          Duration `json:"fetch_interval"`
	PushInterval           Duration `json:"push_interval"`
	GitTimeout             Duration `json:"git_timeout"`
	RetryBackoff           Duration `json:"retry_backoff"`
	RetryMaxBackoff        Duration `json:"retry_max_backoff"`
	Remote                 string   `json:"remote"`
	Branch                 string   `json:"branch"`
	GitAuthorName          string   `json:"git_author_name"`
	GitAuthorEmail         string   `json:"git_author_email"`
}

// Defaults 采用设计第 6.1 节的建议值；没有来源的安装仍可公开读取
func Defaults() Config {
	return Config{Listen: "127.0.0.1:8080", DataDir: "./data", PublicURL: "http://localhost:8080", Frames: []string{"blog"},
		SessionKeyEnv: "COMMENTB_SESSION_KEY", SessionTTL: Duration(24 * time.Hour),
		FirstLevelPageSize: 5, SecondLevelPreviewSize: 3, SecondLevelPageSize: 10, MaxPageLimit: 80, MaxPreviewLimit: 20,
		MaxContentBytes: 16384, IndexCacheBudget: 64 << 20, CommentCacheBudget: 32 << 20, RateLimitPerMinute: 10,
		CommitInterval: Duration(30 * time.Second), CommitMaxChanges: 20, FetchInterval: Duration(30 * time.Second), PushInterval: Duration(120 * time.Second),
		GitTimeout: Duration(20 * time.Second), RetryBackoff: Duration(time.Second), RetryMaxBackoff: Duration(time.Minute),
		Remote: "origin", Branch: "main", GitAuthorName: "Comment.B", GitAuthorEmail: "commentb@localhost"}
}

// Load 严格解析配置并应用默认值，相对数据路径以配置文件目录为基准
func Load(path string) (Config, error) {
	c := Defaults()
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return c, errors.New("配置只能包含一个 JSON 对象")
	}
	if !filepath.IsAbs(c.DataDir) {
		c.DataDir = filepath.Join(filepath.Dir(path), c.DataDir)
	}
	if err = c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

// namePattern 限制评论框、来源和远端代号，避免路径和 Git 选项注入
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// ValidName 校验可进入路径的短代号。
func ValidName(s string) bool { return namePattern.MatchString(s) }

// Origin 校验没有用户信息、路径、查询或片段的 HTTP(S) 来源
func Origin(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil && u.Opaque == "" && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

// Validate 在启动前拒绝不一致的边界和不安全来源配置
func (c Config) Validate() error {
	if c.Listen == "" || c.DataDir == "" || !Origin(c.PublicURL) {
		return errors.New("listen、data_dir 或 public_url 无效（public_url 不带末尾斜杠）")
	}
	if strings.HasPrefix(c.PublicURL, "https:") && !c.SecureCookies {
		return errors.New("HTTPS 必须启用 secure_cookies")
	}
	if c.SessionKeyEnv == "" || c.SessionTTL < Duration(time.Second) || c.SessionTTL > Duration(30*24*time.Hour) {
		return errors.New("会话配置无效")
	}
	if len(c.Frames) == 0 {
		return errors.New("frames 不能为空")
	}
	seen := map[string]bool{}
	for _, f := range c.Frames {
		if !ValidName(f) || seen[f] {
			return fmt.Errorf("评论框无效或重复: %q", f)
		}
		seen[f] = true
	}
	for _, o := range c.AllowedOrigins {
		if !Origin(o) {
			return fmt.Errorf("CORS 来源无效: %q", o)
		}
	}
	for _, id := range c.Admins {
		raw := strings.TrimPrefix(id, "github-")
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || n == 0 || id != "github-"+strconv.FormatUint(n, 10) {
			return errors.New("管理员必须是 github-<数字用户 ID>，不能使用昵称")
		}
	}
	if c.FirstLevelPageSize < 1 || c.SecondLevelPageSize < 1 || c.SecondLevelPreviewSize < 1 || c.MaxPageLimit > 500 || c.MaxPreviewLimit > 100 || c.FirstLevelPageSize > c.MaxPageLimit || c.SecondLevelPageSize > c.MaxPageLimit || c.SecondLevelPreviewSize > c.MaxPreviewLimit {
		return errors.New("分页数量必须为正数且不超过上限（最大 500 / 100）")
	}
	if c.MaxContentBytes < 1 || c.MaxContentBytes > 65536 || c.IndexCacheBudget < 0 || c.CommentCacheBudget < 0 || c.RateLimitPerMinute < 1 {
		return errors.New("正文、缓存或限流配置无效")
	}
	for _, d := range []Duration{c.CommitInterval, c.FetchInterval, c.PushInterval, c.GitTimeout, c.RetryBackoff, c.RetryMaxBackoff} {
		if d < Duration(time.Millisecond) {
			return errors.New("Git 时长至少 1ms")
		}
	}
	if c.CommitMaxChanges < 1 || c.RetryBackoff > c.RetryMaxBackoff || (c.Remote != "" && !ValidName(c.Remote)) || !ValidName(c.Branch) {
		return errors.New("Git 阈值、远端或分支无效（分支使用简单代号）")
	}
	if strings.ContainsAny(c.GitAuthorName+c.GitAuthorEmail, "\r\n\x00") || c.GitAuthorName == "" || c.GitAuthorEmail == "" {
		return errors.New("Git 提交者无效")
	}
	if c.GitHub.ClientID != "" && c.GitHub.ClientSecretEnv == "" {
		return errors.New("GitHub client_secret_env 不能为空")
	}
	return nil
}

// SessionKey 返回共享的高熵密钥；不将其打印到日志或写入评论仓库
func (c Config) SessionKey() ([]byte, error) {
	k := []byte(os.Getenv(c.SessionKeyEnv))
	if len(k) < 32 {
		return nil, fmt.Errorf("环境变量 %s 至少需要 32 字节随机密钥", c.SessionKeyEnv)
	}
	return k, nil
}
