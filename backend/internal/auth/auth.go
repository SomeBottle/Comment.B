// Package auth 实现 GitHub OAuth2 授权码登录、签名会话和 CSRF 防护，不持久化访问令牌
package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SomeBottle/Comment.B/backend/internal/config"
	"github.com/SomeBottle/Comment.B/backend/internal/store"
)

// Session 只包含展示身份和有效期，撤销全部会话可轮换共享密钥
type Session struct {
	Author  store.Author `json:"author"`
	CSRF    string       `json:"csrf_token"`
	Issued  int64        `json:"issued_at"`
	Expires int64        `json:"expires_at"`
}

// loginState 绑定浏览器、来源、短期随机 state 和 PKCE verifier
type loginState struct {
	State, Verifier, ReturnTo string
	Expires                   int64
}

// Auth 保存本地配置与共享签名密钥，OAuth 网络操作不占工作区锁
type Auth struct {
	Config config.Config
	key    []byte
	client *http.Client
}

// New 验证 OAuth 密钥存在，禁止把认证请求发送到重定向后的未知端点
func New(c config.Config, key []byte) (*Auth, error) {
	if len(key) < 32 {
		return nil, errors.New("会话密钥至少 32 字节")
	}
	if c.GitHub.ClientID != "" && os.Getenv(c.GitHub.ClientSecretEnv) == "" {
		return nil, fmt.Errorf("GitHub 密钥环境变量未设置: %s", c.GitHub.ClientSecretEnv)
	}
	return &Auth{Config: c, key: append([]byte(nil), key...), client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// RandomToken 使用 256 位随机值创建 CSRF、OAuth state 和 PKCE verifier
func RandomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Sign 对用途和负载同时签名，避免 session、state 和 cursor 相互替代
func (a *Auth) Sign(purpose string, payload []byte) string {
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	h := hmac.New(sha256.New, a.key)
	h.Write([]byte(purpose + "\x00" + encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// Verify 验证长度和签名后才解码负载
func (a *Auth) Verify(purpose, token string, max int) ([]byte, error) {
	if len(token) > max {
		return nil, errors.New("令牌过大")
	}
	v := strings.Split(token, ".")
	if len(v) != 2 {
		return nil, errors.New("令牌无效")
	}
	h := hmac.New(sha256.New, a.key)
	h.Write([]byte(purpose + "\x00" + v[0]))
	sig, err := base64.RawURLEncoding.DecodeString(v[1])
	if err != nil || !hmac.Equal(sig, h.Sum(nil)) {
		return nil, errors.New("令牌签名无效")
	}
	return base64.RawURLEncoding.DecodeString(v[0])
}

// cookieName 在 HTTPS 下使用 __Host- 前缀阻止子域覆盖会话
func (a *Auth) cookieName(suffix string) string {
	if a.Config.SecureCookies {
		return "__Host-commentb_" + suffix
	}
	return "commentb_" + suffix
}

// setCookie 会话允许嵌入站点携带 Cookie；修改请求仍必须通过 CSRF 与来源校验
func (a *Auth) setCookie(w http.ResponseWriter, suffix, value string, age int) {
	sameSite := http.SameSiteLaxMode
	if suffix == "session" && a.Config.SecureCookies {
		sameSite = http.SameSiteNoneMode
	}
	http.SetCookie(w, &http.Cookie{Name: a.cookieName(suffix), Value: value, Path: "/", HttpOnly: true, Secure: a.Config.SecureCookies, SameSite: sameSite, MaxAge: age})
}

// Issue 创建签名 Cookie；不包含 OAuth token 或明文邮箱
func (a *Auth) Issue(w http.ResponseWriter, author store.Author) error {
	if err := store.ValidateAuthor(author); err != nil {
		return err
	}
	csrf, err := RandomToken()
	if err != nil {
		return err
	}
	now := time.Now()
	session := Session{Author: author, CSRF: csrf, Issued: now.Unix(), Expires: now.Add(time.Duration(a.Config.SessionTTL)).Unix()}
	b, err := json.Marshal(session)
	if err != nil {
		return err
	}
	a.setCookie(w, "session", a.Sign("session", b), int(time.Duration(a.Config.SessionTTL)/time.Second))
	return nil
}

// Session 验证 Cookie 身份、到期时间和签发上界
func (a *Auth) Session(r *http.Request) (Session, error) {
	var s Session
	c, err := r.Cookie(a.cookieName("session"))
	if err != nil {
		return s, err
	}
	b, err := a.Verify("session", c.Value, 8192)
	if err != nil {
		return s, err
	}
	if json.Unmarshal(b, &s) != nil || s.Expires <= time.Now().Unix() || s.Issued > time.Now().Unix()+60 || s.Issued <= 0 || s.Expires <= s.Issued || s.Expires-s.Issued > int64(time.Duration(a.Config.SessionTTL)/time.Second) || len(s.CSRF) != 43 || store.ValidateAuthor(s.Author) != nil {
		return Session{}, errors.New("会话已失效")
	}
	return s, nil
}

// AllowedOrigin 只接受明确白名单或后端自身来源，拒绝 null 和通配符
func (a *Auth) AllowedOrigin(origin string) bool {
	if origin == a.Config.PublicURL {
		return true
	}
	for _, o := range a.Config.AllowedOrigins {
		if origin == o {
			return true
		}
	}
	return false
}

// CSRF 对所有 Cookie 修改请求同时检查来源和不可预测的会话令牌
func (a *Auth) CSRF(r *http.Request, s Session) bool {
	return a.AllowedOrigin(r.Header.Get("Origin")) && hmac.Equal([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.CSRF))
}

// respond 输出统一 JSON，并防止缓存含会话信息的响应
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Register 注册会话查询、退出和 GitHub OAuth 路由
func (a *Auth) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/session", func(w http.ResponseWriter, r *http.Request) {
		s, err := a.Session(r)
		if err != nil {
			respond(w, 401, map[string]string{"code": "UNAUTHENTICATED", "message": "请先登录"})
			return
		}
		respond(w, 200, s)
	})
	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		s, err := a.Session(r)
		if err != nil {
			respond(w, 401, map[string]string{"code": "UNAUTHENTICATED"})
			return
		}
		if !a.CSRF(r, s) {
			respond(w, 403, map[string]string{"code": "CSRF_FAILED"})
			return
		}
		a.setCookie(w, "session", "", -1)
		w.WriteHeader(204)
	})
	if p := a.Config.GitHub; p.ClientID != "" {
		mux.HandleFunc("GET /auth/"+"github"+"/login", func(w http.ResponseWriter, r *http.Request) { a.login(w, r, p) })
		mux.HandleFunc("GET /auth/"+"github"+"/callback", func(w http.ResponseWriter, r *http.Request) { a.callback(w, r, p) })
	}
}

// login 保存与浏览器绑定的短期 state，使用 S256 PKCE 防止授权码被截获后兑换
func (a *Auth) login(w http.ResponseWriter, r *http.Request, p config.GitHub) {
	state, err := RandomToken()
	if err != nil {
		respond(w, 500, map[string]string{"code": "AUTH_FAILED"})
		return
	}
	verifier, err := RandomToken()
	if err != nil {
		respond(w, 500, map[string]string{"code": "AUTH_FAILED"})
		return
	}
	returnTo := a.Config.PublicURL + "/auth/session"
	if raw := r.URL.Query().Get("return_to"); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || len(raw) > 2048 || u.User != nil || !a.AllowedOrigin(u.Scheme+"://"+u.Host) {
			respond(w, 400, map[string]string{"code": "INVALID_RETURN_TO"})
			return
		}
		returnTo = raw
	}
	b, _ := json.Marshal(loginState{State: state, Verifier: verifier, ReturnTo: returnTo, Expires: time.Now().Add(10 * time.Minute).Unix()})
	a.setCookie(w, "oauth_github", a.Sign("oauth", b), 600)
	u, _ := url.Parse("https://github.com/login/oauth/authorize")
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", a.Config.PublicURL+"/auth/github/callback")
	q.Set("scope", "read:user user:email")
	q.Set("state", state)
	h := sha256.Sum256([]byte(verifier))
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(h[:]))
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// fetchJSON 仅接受成功且大小受限的 JSON，不记录响应中的 token 或邮箱
func (a *Auth) fetchJSON(req *http.Request, v any) error {
	resp, err := a.client.Do(req)
	if err != nil {
		return errors.New("OAuth 来源请求失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("OAuth 来源拒绝请求")
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || (media != "application/json" && !strings.HasSuffix(media, "+json")) {
		return errors.New("OAuth 来源未返回 JSON")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return errors.New("OAuth 响应过大")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err = d.Decode(v); err != nil {
		return errors.New("OAuth JSON 无效")
	}
	return nil
}

// callback 先验证浏览器 state，再兑换授权码并立即丢弃短期 access token
func (a *Auth) callback(w http.ResponseWriter, r *http.Request, p config.GitHub) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	failed := func() {
		respond(w, 400, map[string]string{"code": "AUTH_FAILED", "message": "登录失败，请重试"})
	}
	c, err := r.Cookie(a.cookieName("oauth_github"))
	if err != nil {
		failed()
		return
	}
	b, err := a.Verify("oauth", c.Value, 8192)
	if err != nil {
		failed()
		return
	}
	var state loginState
	if json.Unmarshal(b, &state) != nil || len(state.State) != 43 || len(state.Verifier) != 43 || state.Expires <= time.Now().Unix() || !hmac.Equal([]byte(state.State), []byte(r.URL.Query().Get("state"))) || r.URL.Query().Get("code") == "" || len(r.URL.Query().Get("code")) > 4096 {
		failed()
		return
	}
	a.setCookie(w, "oauth_github", "", -1)
	form := url.Values{"grant_type": {"authorization_code"}, "code": {r.URL.Query().Get("code")}, "redirect_uri": {a.Config.PublicURL + "/auth/github/callback"}, "client_id": {p.ClientID}, "client_secret": {os.Getenv(p.ClientSecretEnv)}, "code_verifier": {state.Verifier}}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		failed()
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if a.fetchJSON(req, &token) != nil || token.AccessToken == "" || (token.TokenType != "" && !strings.EqualFold(token.TokenType, "bearer")) {
		failed()
		return
	}
	req, err = http.NewRequestWithContext(r.Context(), http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		failed()
		return
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/json")
	var user struct {
		ID    json.Number `json:"id"`
		Name  string      `json:"name"`
		Login string      `json:"login"`
	}
	if a.fetchJSON(req, &user) != nil {
		failed()
		return
	}
	id := user.ID.String()
	name := strings.TrimSpace(user.Name)
	if name == "" {
		name = user.Login
	}
	if name == "" {
		name = id
	}
	numericID, err := strconv.ParseUint(id, 10, 64)
	if err != nil || numericID == 0 || strconv.FormatUint(numericID, 10) != id {
		failed()
		return
	}
	author := store.Author{ID: "github-" + id, Name: name}
	email := ""
	{
		req, err = http.NewRequestWithContext(r.Context(), http.MethodGet, "https://api.github.com/user/emails", nil)
		if err != nil {
			failed()
			return
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		req.Header.Set("Accept", "application/json")
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		if a.fetchJSON(req, &emails) != nil {
			failed()
			return
		}
		for _, e := range emails {
			if e.Primary && e.Verified {
				email = e.Email
				break
			}
		}
	}
	if email != "" {
		h := md5.Sum([]byte(strings.ToLower(strings.TrimSpace(email))))
		v := hex.EncodeToString(h[:])
		author.EmailMD5 = &v
	}
	if a.Issue(w, author) != nil {
		failed()
		return
	}
	http.Redirect(w, r, state.ReturnTo, http.StatusSeeOther)
}
