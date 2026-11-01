package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/SomeBottle/Comment.B/backend/internal/auth"
	"github.com/SomeBottle/Comment.B/backend/internal/store"
	gitworker "github.com/SomeBottle/Comment.B/backend/internal/sync"
	"github.com/SomeBottle/Comment.B/backend/internal/testutil"
)

// apiFixture 提供直接经过完整 HTTP Handler 的测试环境。
type apiFixture struct {
	F       *testutil.Fixture
	Auth    *auth.Auth
	Handler http.Handler
	Cookie  *http.Cookie
	CSRF    string
}

// setup 配置三种独立分页数量，并签发仅供测试使用的真实签名会话。
func setup(t *testing.T) *apiFixture {
	t.Helper()
	f := testutil.New(t)
	f.Config.FirstLevelPageSize = 2
	f.Config.SecondLevelPreviewSize = 3
	f.Config.SecondLevelPageSize = 4
	a, err := auth.New(f.Config, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	r := &apiFixture{F: f, Auth: a, Handler: New(f.Store, a, gitworker.New(f.Store, f.Repo, f.Config), f.Config)}
	r.login(t, "1")
	return r
}

// login 为指定 GitHub 数字身份创建新的 Cookie 和 CSRF 令牌。
func (f *apiFixture) login(t *testing.T, id string) {
	t.Helper()
	w := httptest.NewRecorder()
	if err := f.Auth.Issue(w, testutil.Author(id)); err != nil {
		t.Fatal(err)
	}
	f.Cookie = w.Result().Cookies()[0]
	r := httptest.NewRequest("GET", "/auth/session", nil)
	r.AddCookie(f.Cookie)
	s, err := f.Auth.Session(r)
	if err != nil {
		t.Fatal(err)
	}
	f.CSRF = s.CSRF
}

// request 经过来源白名单、认证和 CSRF 中间件，避免绕过实际接口行为。
func (f *apiFixture) request(method, path, body string, logged bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", f.F.Config.PublicURL)
	if logged {
		r.AddCookie(f.Cookie)
		r.Header.Set("X-CSRF-Token", f.CSRF)
	}
	w := httptest.NewRecorder()
	f.Handler.ServeHTTP(w, r)
	return w
}

// createHTTP 验证创建响应包含本地持久化语义及服务端生成的字段。
func (f *apiFixture) createHTTP(t *testing.T, root string, reply *string, content string) store.Comment {
	t.Helper()
	body := map[string]any{"page_key": "p", "content": content}
	path := "/api/v1/frames/blog/comments"
	if root != "" {
		path += "/" + root + "/replies"
		body["reply_to"] = reply
	}
	b, _ := json.Marshal(body)
	w := f.request("POST", path, string(b), true)
	if w.Code != 201 {
		t.Fatalf("创建失败 %d %s", w.Code, w.Body)
	}
	var got struct {
		store.Comment
		Durability string `json:"durability"`
	}
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Durability != "local" || got.Author.ID != "github-1" || got.RootID == "" {
		t.Fatal("创建响应无效", w.Body)
	}
	return got.Comment
}

// getPage 解码真实接口响应并断言成功。
func (f *apiFixture) getPage(t *testing.T, path string) Page {
	t.Helper()
	w := f.request("GET", path, "", false)
	if w.Code != 200 {
		t.Fatalf("列表失败 %d %s", w.Code, w.Body)
	}
	var p Page
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPreviewContinuationAndIndependentSort 验证只建立当前页预览、游标续接、上下层独立方向和参数上限。
func TestPreviewContinuationAndIndependentSort(t *testing.T) {
	f := setup(t)
	roots := []store.Comment{}
	for i := 0; i < 4; i++ {
		root := f.createHTTP(t, "", nil, fmt.Sprintf("root-%d", i))
		roots = append(roots, root)
		for j := 0; j < 9; j++ {
			f.createHTTP(t, root.ID, &root.ID, fmt.Sprintf("reply-%d", j))
		}
	}
	path := "/api/v1/frames/blog/comments?page_key=p&sort=asc&reply_sort=desc"
	p := f.getPage(t, path)
	if len(p.Items) != 2 || !p.HasMore || p.NextCursor == nil {
		t.Fatal("一级数量或游标错误")
	}
	f.F.Store.Mu.Lock()
	stats := f.F.Store.Cache.Stats()
	f.F.Store.Mu.Unlock()
	if stats.Builds != 3 {
		t.Fatalf("应只构建页面及当前两个根的索引: %+v", stats)
	}
	for i, item := range p.Items {
		if item.ID != roots[i].ID || item.RepliesPreview == nil || len(item.RepliesPreview.Items) != 3 || !item.RepliesPreview.HasMore {
			t.Fatal("默认预览错误")
		}
		preview := item.RepliesPreview
		if preview.Sort != "desc" || preview.Items[0].Content != "reply-8" {
			t.Fatal("二级方向受一级排序影响")
		}
		next := f.getPage(t, "/api/v1/frames/blog/comments/"+item.ID+"/replies?page_key=p&sort=desc&cursor="+url.QueryEscape(*preview.NextCursor))
		if len(next.Items) != 4 || next.Items[0].Content != "reply-5" || !next.HasMore {
			t.Fatal("加载更多未从预览边界继续")
		}
		last := f.getPage(t, "/api/v1/frames/blog/comments/"+item.ID+"/replies?page_key=p&cursor="+url.QueryEscape(*next.NextCursor))
		if len(last.Items) != 2 || last.HasMore || last.NextCursor != nil {
			t.Fatal("末页状态错误")
		}
		bad := f.request("GET", "/api/v1/frames/blog/comments/"+roots[3-i].ID+"/replies?page_key=p&cursor="+url.QueryEscape(*preview.NextCursor), "", false)
		if bad.Code != 400 {
			t.Fatal("跨根游标未拒绝")
		}
		bad = f.request("GET", "/api/v1/frames/blog/comments/"+item.ID+"/replies?page_key=p&sort=asc&cursor="+url.QueryEscape(*preview.NextCursor), "", false)
		if bad.Code != 400 {
			t.Fatal("切换方向仍接受旧游标")
		}
	}
	second := f.getPage(t, path+"&cursor="+url.QueryEscape(*p.NextCursor))
	if len(second.Items) != 2 || second.Items[0].ID != roots[2].ID || second.HasMore {
		t.Fatal("一级续页错误")
	}
	custom := f.getPage(t, "/api/v1/frames/blog/comments?page_key=p&limit=1&preview_limit=1&reply_sort=asc")
	if len(custom.Items) != 1 || len(custom.Items[0].RepliesPreview.Items) != 1 || custom.Items[0].RepliesPreview.Items[0].Content != "reply-0" {
		t.Fatal("独立数量配置错误")
	}
	for _, q := range []string{"limit=81", "preview_limit=21", "limit=0", "sort=bad", "page_key=p&page_key=other"} {
		w := f.request("GET", "/api/v1/frames/blog/comments?page_key=p&"+q, "", false)
		if w.Code != 400 {
			t.Fatalf("非法参数未拒绝 %s: %d", q, w.Code)
		}
	}
}

// TestReplyTargetAndPermissions 验证目标在其他分页时仍存在，删除 C 后 D 保留并允许继续回复。
func TestReplyTargetAndPermissions(t *testing.T) {
	f := setup(t)
	a := f.createHTTP(t, "", nil, "A")
	b := f.createHTTP(t, a.ID, &a.ID, "B")
	c := f.createHTTP(t, a.ID, &b.ID, "C")
	d := f.createHTTP(t, a.ID, &c.ID, "<script>alert(1)</script>")
	path := "/api/v1/frames/blog/comments/" + a.ID + "/replies?page_key=p&limit=1"
	p := f.getPage(t, path)
	if p.Items[0].ID != d.ID || *p.Items[0].ReplyTargetDeleted || p.Items[0].ReplyTarget.Name != "用户1" {
		t.Fatal("错误地根据分页判断目标删除")
	}
	f.login(t, "2")
	delPath := "/api/v1/frames/blog/comments/" + a.ID + "/replies/" + c.ID + "?page_key=p"
	if w := f.request("DELETE", delPath, "", true); w.Code != 403 {
		t.Fatal("越权删除未拒绝")
	}
	f.login(t, "1")
	if w := f.request("DELETE", delPath, "", true); w.Code != 204 {
		t.Fatal("二级删除失败", w.Body)
	}
	p = f.getPage(t, path)
	if !*p.Items[0].ReplyTargetDeleted || p.Items[0].ReplyTarget != nil {
		t.Fatal("已删除目标展示错误")
	}
	f.createHTTP(t, a.ID, &d.ID, "继续回复 D")
	w := f.request("POST", "/api/v1/frames/blog/comments/"+a.ID+"/replies", `{"page_key":"p","content":"不存在","reply_to":"`+c.ID+`"}`, true)
	if w.Code != 404 || !strings.Contains(w.Body.String(), "COMMENT_NOT_FOUND") {
		t.Fatal("接受了不存在的直接目标")
	}
	if w = f.request("DELETE", "/api/v1/frames/blog/comments/"+a.ID+"?page_key=p", "", true); w.Code != 204 {
		t.Fatal("级联删除失败", w.Body)
	}
	w = f.request("GET", path, "", false)
	if w.Code != 404 {
		t.Fatal("根删除后仍展示回复")
	}
}

// TestAuthValidationAndRateLimit 验证 Cookie 认证、CSRF、危险 URL、伪造作者、正文上限和限流。
func TestAuthValidationAndRateLimit(t *testing.T) {
	f := setup(t)
	path := "/api/v1/frames/blog/comments"
	if w := f.request("POST", path, `{"page_key":"p","content":"x"}`, false); w.Code != 401 {
		t.Fatal("未登录可创建")
	}
	r := httptest.NewRequest("POST", path, strings.NewReader(`{"page_key":"p","content":"x"}`))
	r.AddCookie(f.Cookie)
	r.Header.Set("Origin", f.F.Config.PublicURL)
	w := httptest.NewRecorder()
	f.Handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("缺少 CSRF 未拒绝")
	}
	for _, body := range []string{`{"page_key":"p","content":"x","author":{"id":"github-2"}}`, `{"page_key":"p","content":"x","link":"javascript:alert(1)"}`, `{"page_key":"p","content":"x","link":"https://u:p@example.com"}`, `{"page_key":"","content":"x"}`, `{"page_key":"p","content":"   "}`, `{"page_key":"p","content":"` + strings.Repeat("x", 16385) + `"}`, `{"page_key":"p","content":"x"} {}`} {
		if w = f.request("POST", path, body, true); w.Code != 400 {
			t.Fatalf("无效正文未拒绝: %d", w.Code)
		}
	}
	big := strings.Repeat("x", 120000)
	if w = f.request("POST", path, big, true); w.Code != 413 {
		t.Fatal("超大请求未拒绝", w.Code)
	}
	r = httptest.NewRequest("GET", path+"?page_key=p", nil)
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	f.Handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("CORS 来源未拒绝")
	}
	// 小限额使用新的 Handler，确保窗口按真实认证身份计数。
	f.F.Config.RateLimitPerMinute = 1
	f.Handler = New(f.F.Store, f.Auth, gitworker.New(f.F.Store, f.F.Repo, f.F.Config), f.F.Config)
	f.createHTTP(t, "", nil, "第一次")
	if w = f.request("POST", path, `{"page_key":"p","content":"第二次"}`, true); w.Code != 429 {
		t.Fatal("未执行本地限流")
	}
}

// TestConcurrentHTTP 验证写入、删除、冷索引和预览组合查询在竞态检测下协调工作。
func TestConcurrentHTTP(t *testing.T) {
	f := setup(t)
	root := f.createHTTP(t, "", nil, "根")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			w := f.request("POST", "/api/v1/frames/blog/comments/"+root.ID+"/replies", `{"page_key":"p","content":"并发","reply_to":"`+root.ID+`"}`, true)
			if w.Code != 201 {
				t.Errorf("并发创建失败: %d", w.Code)
			}
		}()
		go func() {
			defer wg.Done()
			w := f.request("GET", "/api/v1/frames/blog/comments?page_key=p", "", false)
			if w.Code != 200 {
				t.Errorf("并发查询失败: %d", w.Code)
			}
		}()
	}
	wg.Wait()
	if p := f.getPage(t, "/api/v1/frames/blog/comments/"+root.ID+"/replies?page_key=p&limit=80"); len(p.Items) != 20 {
		t.Fatal("并发写入丢失")
	}
}

// TestWarmCommentCacheAvoidsFileReads 验证完整预览及回复目标查询重复访问不再打开正文文件。
func TestWarmCommentCacheAvoidsFileReads(t *testing.T) {
	f := setup(t)
	root := f.createHTTP(t, "", nil, "根")
	reply := f.createHTTP(t, root.ID, &root.ID, "目标")
	f.createHTTP(t, root.ID, &reply.ID, "回复")
	f.F.Store.Mu.Lock()
	f.F.Store.ClearCachesLocked()
	f.F.Store.Mu.Unlock()
	path := "/api/v1/frames/blog/comments?page_key=p"
	f.getPage(t, path)
	f.F.Store.Mu.Lock()
	before := f.F.Store.CommentCacheStatsLocked()
	f.F.Store.Mu.Unlock()
	f.getPage(t, path)
	f.F.Store.Mu.Lock()
	after := f.F.Store.CommentCacheStatsLocked()
	f.F.Store.Mu.Unlock()
	if before.DiskReads == 0 || after.DiskReads != before.DiskReads || after.Hits <= before.Hits {
		t.Fatalf("完整预览命中仍读取文件: before=%+v after=%+v", before, after)
	}
	w := f.request("GET", "/metrics", "", false)
	for _, name := range []string{"commentb_comment_cache_bytes", "commentb_comment_cache_entries", "commentb_comment_cache_hits_total", "commentb_comment_file_reads_total"} {
		if !strings.Contains(w.Body.String(), name+" ") {
			t.Fatalf("缺少指标 %s", name)
		}
	}
}
