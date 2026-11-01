package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/SomeBottle/Comment.B/backend/internal/config"
	"github.com/SomeBottle/Comment.B/backend/internal/testutil"
)

// roundTripFunc 仅在认证边界替换网络传输，保留真实 state、PKCE、Cookie 和 JSON 解析。
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip 通过固定响应验证仅访问 GitHub 的规定端点。
func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestGitHubOAuth 验证 state、PKCE、稳定数字身份、只使用已验证邮箱及安全 Cookie。
func TestGitHubOAuth(t *testing.T) {
	t.Setenv("TEST_GITHUB_SECRET", "secret")
	c := config.Defaults()
	c.GitHub = config.GitHub{ClientID: "client", ClientSecretEnv: "TEST_GITHUB_SECRET"}
	c.PublicURL = "https://comments.example"
	c.SecureCookies = true
	a, err := New(c, []byte(strings.Repeat("x", 32)))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a.Register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/auth/github/login", nil))
	if w.Code != 302 {
		t.Fatal(w.Code)
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || u.Host != "github.com" || u.Query().Get("scope") != "read:user user:email" {
		t.Fatal("授权端点或权限错误")
	}
	stateCookie := w.Result().Cookies()[0]
	if !stateCookie.HttpOnly || !stateCookie.Secure || stateCookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("OAuth state Cookie 不安全")
	}
	state := u.Query().Get("state")
	challenge := u.Query().Get("code_challenge")
	calls := 0
	a.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body := ""
		switch r.URL.String() {
		case "https://github.com/login/oauth/access_token":
			r.ParseForm()
			h := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(h[:]) != challenge || r.Form.Get("client_secret") != "secret" || r.Form.Get("code") != "code" {
				t.Error("PKCE 或授权码交换错误")
			}
			body = `{"access_token":"private-token","token_type":"bearer"}`
		case "https://api.github.com/user":
			if r.Header.Get("Authorization") != "Bearer private-token" {
				t.Error("缺少访问令牌")
			}
			body = `{"id":123456,"login":"alice","name":"Alice","email":"unverified@example.com"}`
		case "https://api.github.com/user/emails":
			body = `[{"email":"not-verified@example.com","primary":true,"verified":false},{"email":" Alice@Example.COM ","primary":true,"verified":true}]`
		default:
			t.Fatalf("访问了非 GitHub 端点: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	bad := httptest.NewRequest("GET", "/auth/github/callback?code=code&state=wrong", nil)
	bad.AddCookie(stateCookie)
	bw := httptest.NewRecorder()
	mux.ServeHTTP(bw, bad)
	if bw.Code != 400 || calls != 0 {
		t.Fatal("无效 state 仍交换授权码")
	}
	r := httptest.NewRequest("GET", "/auth/github/callback?code=code&state="+state, nil)
	r.AddCookie(stateCookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 303 || calls != 3 {
		t.Fatalf("登录失败 %d %s", w.Code, w.Body)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "__Host-commentb_session" {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || !sessionCookie.Secure || sessionCookie.SameSite != http.SameSiteNoneMode {
		t.Fatal("会话 Cookie 属性错误")
	}
	r = httptest.NewRequest("GET", "/auth/session", nil)
	r.AddCookie(sessionCookie)
	session, err := a.Session(r)
	if err != nil || session.Author.ID != "github-123456" || session.Author.Name != "Alice" || session.Author.EmailMD5 == nil || *session.Author.EmailMD5 != "c160f8cc69a4f0bf2b0362752353d060" {
		t.Fatalf("身份快照错误: %+v %v", session, err)
	}
	payload, err := a.Verify("session", sessionCookie.Value, 8192)
	if err != nil || strings.Contains(string(payload), "private-token") || strings.Contains(string(payload), "@Example") {
		t.Fatal("会话泄露令牌或明文邮箱")
	}
}

// TestSessionTamperingExpiryAndLogout 验证签名用途隔离、会话期限和退出 CSRF。
func TestSessionTamperingExpiryAndLogout(t *testing.T) {
	c := config.Defaults()
	a, err := New(c, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err = a.Issue(w, testutil.Author("1")); err != nil {
		t.Fatal(err)
	}
	cookie := w.Result().Cookies()[0]
	r := httptest.NewRequest("POST", "/auth/logout", nil)
	r.AddCookie(cookie)
	s, err := a.Session(r)
	if err != nil {
		t.Fatal(err)
	}
	if a.CSRF(r, s) {
		t.Fatal("无 CSRF 令牌仍通过")
	}
	r.Header.Set("Origin", c.PublicURL)
	r.Header.Set("X-CSRF-Token", s.CSRF)
	if !a.CSRF(r, s) {
		t.Fatal("合法 CSRF 校验失败")
	}
	if _, err = a.Verify("cursor", cookie.Value, 8192); err == nil {
		t.Fatal("会话可冒充游标")
	}
	bad := *cookie
	bad.Value += "tampered"
	rr := httptest.NewRequest("GET", "/", nil)
	rr.AddCookie(&bad)
	if _, err = a.Session(rr); err == nil {
		t.Fatal("接受篡改会话")
	}
	s.Expires = time.Now().Add(-time.Hour).Unix()
	b, _ := json.Marshal(s)
	bad.Value = a.Sign("session", b)
	rr = httptest.NewRequest("GET", "/", nil)
	rr.AddCookie(&bad)
	if _, err = a.Session(rr); err == nil {
		t.Fatal("接受过期会话")
	}
	mux := http.NewServeMux()
	a.Register(mux)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 204 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("退出没有清除 Cookie")
	}
}
