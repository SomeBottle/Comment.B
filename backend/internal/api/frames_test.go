package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/SomeBottle/Comment.B/backend/internal/store"
	gitworker "github.com/SomeBottle/Comment.B/backend/internal/sync"
	"github.com/SomeBottle/Comment.B/backend/internal/testutil"
)

// resetPolicy 使用给定的创建策略重新装配 Handler，模拟修改配置后重启。
func (f *apiFixture) resetPolicy() {
	f.Handler = New(f.F.Store, f.Auth, gitworker.New(f.F.Store, f.F.Repo, f.F.Config), f.F.Config)
}

// createFrameHTTP 经认证和 CSRF 的真实路由创建框，不允许客户端指定 ID 或拥有者。
func (f *apiFixture) createFrameHTTP(t *testing.T, name string) store.Frame {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "intro": "测试网站"})
	w := f.request("POST", "/api/v1/frames", string(body), true)
	if w.Code != 201 {
		t.Fatalf("创建框失败: %d %s", w.Code, w.Body)
	}
	var frame store.Frame
	if err := json.Unmarshal(w.Body.Bytes(), &frame); err != nil {
		t.Fatal(err)
	}
	if len(frame.ID) != 20 || frame.OwnerID == "" || !strings.Contains(w.Body.String(), `"durability":"local"`) || w.Header().Get("Location") != "/api/v1/frames/"+frame.ID {
		t.Fatal("创建框响应无效", w.Body)
	}
	return frame
}

// TestFrameCreatorPolicyAndLifecycle 验证旧版开放开关、白名单、上限、拥有权及整框生命周期。
func TestFrameCreatorPolicyAndLifecycle(t *testing.T) {
	f := setup(t)
	f.F.Config.OpenToEveryone = false
	f.F.Config.AllowedCreators = []string{"github-1"}
	f.F.Config.MaxFramesPerUser = 2
	f.resetPolicy()
	valid := `{"name":"网站","intro":"简介"}`
	if w := f.request("POST", "/api/v1/frames", valid, false); w.Code != 401 {
		t.Fatal("未登录可创建框")
	}
	f.login(t, "2")
	if w := f.request("POST", "/api/v1/frames", valid, true); w.Code != 403 {
		t.Fatal("白名单外用户可创建框")
	}
	f.login(t, "1")
	for _, bad := range []string{`{"name":"网站","intro":"简介","owner_id":"github-2"}`, `{"name":"网站","intro":"简介","id":"blog"}`, `{"name":"","intro":"简介"}`, `{"name":"网站","intro":""}`, fmt.Sprintf(`{"name":%q,"intro":"简介"}`, strings.Repeat("名", 21))} {
		if w := f.request("POST", "/api/v1/frames", bad, true); w.Code != 400 {
			t.Fatalf("非法创建正文未拒绝: %d %s", w.Code, bad)
		}
	}
	a := f.createFrameHTTP(t, "网站 A")
	b := f.createFrameHTTP(t, "网站 B")
	if a.OwnerID != "github-1" || a.ID == b.ID {
		t.Fatal("归属或生成 ID 错误")
	}
	if w := f.request("POST", "/api/v1/frames", valid, true); w.Code != 409 || !strings.Contains(w.Body.String(), "FRAME_LIMIT_REACHED") {
		t.Fatal("创建超额框未拒绝", w.Body)
	}
	w := f.request("GET", "/api/v1/frames", "", true)
	var owned struct {
		Items     []store.Frame `json:"items"`
		CanCreate bool          `json:"can_create"`
		MaxFrames int           `json:"max_frames"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &owned) != nil || len(owned.Items) != 2 || owned.CanCreate || owned.MaxFrames != 2 {
		t.Fatal("本人列表或额度无效", w.Body)
	}
	if w = f.request("GET", "/api/v1/frames/"+a.ID, "", true); w.Code != 200 {
		t.Fatal("拥有者无法查看详情")
	}
	if w = f.request("GET", "/api/v1/frames/"+a.ID+"/comments?page_key=p", "", false); w.Code != 200 {
		t.Fatal("新框无法公开查询")
	}
	f.login(t, "2")
	if w = f.request("GET", "/api/v1/frames", "", true); w.Code != 200 || strings.Contains(w.Body.String(), a.ID) {
		t.Fatal("本人列表泄露他人框")
	}
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		if w = f.request(method, "/api/v1/frames/"+a.ID, valid, true); w.Code != 403 {
			t.Fatalf("用户 B 可管理 A 的框: %s %d", method, w.Code)
		}
	}
	// 创建资格撤销后，原拥有者仍能管理已创建框。
	f.login(t, "1")
	f.F.Config.AllowedCreators = nil
	f.resetPolicy()
	if w = f.request("PATCH", "/api/v1/frames/"+a.ID, `{"name":"新名称","intro":"新简介"}`, true); w.Code != 200 || !strings.Contains(w.Body.String(), "新名称") {
		t.Fatal("撤销创建资格误撤销管理权", w.Body)
	}
	if w = f.request("DELETE", "/api/v1/frames/"+a.ID, "", true); w.Code != 204 {
		t.Fatal("拥有者无法删除框", w.Body)
	}
	if w = f.request("GET", "/api/v1/frames/"+a.ID+"/comments?page_key=p", "", false); w.Code != 404 || !strings.Contains(w.Body.String(), "FRAME_NOT_FOUND") {
		t.Fatal("删除框仍可访问")
	}
	f.F.Config.AllowedCreators = []string{"github-1"}
	f.resetPolicy()
	f.createFrameHTTP(t, "释放后的额度")
}

// TestFrameScopedModeration 验证 A 管理自己框内所有评论，不能管理 B 的框，评论作者仍可自行删除。
func TestFrameScopedModeration(t *testing.T) {
	f := setup(t)
	a := f.createFrameHTTP(t, "A 的框")
	f.login(t, "2")
	b := f.createFrameHTTP(t, "B 的框")
	post := func(frame string) store.Comment {
		w := f.request("POST", "/api/v1/frames/"+frame+"/comments", `{"page_key":"p","content":"访客评论"}`, true)
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body)
		}
		var c store.Comment
		if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	inA := post(a.ID)
	inB := post(b.ID)
	f.login(t, "1")
	deletePath := func(frame string, c store.Comment) string {
		return "/api/v1/frames/" + frame + "/comments/" + c.ID + "?page_key=p"
	}
	if w := f.request("DELETE", deletePath(b.ID, inB), "", true); w.Code != 403 {
		t.Fatal("A 的管理权越界到 B 的框")
	}
	if w := f.request("DELETE", deletePath(a.ID, inA), "", true); w.Code != 204 {
		t.Fatal("A 无法删除本框其他作者评论", w.Body)
	}
	f.login(t, "2")
	if w := f.request("DELETE", deletePath(b.ID, inB), "", true); w.Code != 204 {
		t.Fatal("作者无法自行删除")
	}
	// 只拥有创建资格不意味着可删除预置 blog 框内其他用户的评论。
	f.login(t, "1")
	c := testutil.Create(t, f.F.Store, "p", "", nil, "他人评论")
	f.login(t, "2")
	if w := f.request("DELETE", deletePath("blog", c), "", true); w.Code != 403 {
		t.Fatal("创建资格被错误当成全局管理员")
	}
	// 删除框会同时清空框的所有页面评论和已预热缓存。
	f.login(t, "1")
	post(a.ID)
	f.getPage(t, "/api/v1/frames/"+a.ID+"/comments?page_key=p")
	if w := f.request("DELETE", "/api/v1/frames/"+a.ID, "", true); w.Code != 204 {
		t.Fatal(w.Body)
	}
	if w := f.request("POST", "/api/v1/frames/"+a.ID+"/comments", `{"page_key":"p","content":"复活"}`, true); w.Code != 404 {
		t.Fatal("已删除框可接受评论")
	}
}
