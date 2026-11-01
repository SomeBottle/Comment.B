package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLoadDefaultsAndBoundaries 验证默认参数、相对路径、密钥加载和严格配置解析。
func TestLoadDefaultsAndBoundaries(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{"data_dir":"repo","remote":"","session_ttl":"2h","github":{"client_id":"client","client_secret_env":"SECRET"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != filepath.Join(dir, "repo") || c.FirstLevelPageSize != 5 || c.SecondLevelPreviewSize != 3 || c.SecondLevelPageSize != 10 || c.SessionTTL != Duration(2*time.Hour) || c.CommentCacheBudget != 32<<20 {
		t.Fatal("默认值或相对路径错误")
	}
	t.Setenv(c.SessionKeyEnv, "short")
	if _, err = c.SessionKey(); err == nil {
		t.Fatal("接受短 Session 密钥")
	}
	for _, raw := range []string{`{"unknown":true}`, `{"first_level_page_size":81}`, `{"second_level_preview_size":0}`, `{"public_url":"https://comments.example","secure_cookies":false}`, `{"allowed_origins":["*"]}`, `{"admins":["alice"]}`, `{"admins":["github-001"]}`, `{"frames":["../escape"]}`, `{"git_timeout":20}`, `{"session_ttl":"1ns"}`, `{} {}`} {
		os.WriteFile(p, []byte(raw), 0600)
		if _, err = Load(p); err == nil {
			t.Fatalf("接受无效配置 %s", raw)
		}
	}
	os.WriteFile(p, []byte(`{"comment_cache_budget":-1}`), 0600)
	if _, err = Load(p); err == nil {
		t.Fatal("接受负数正文缓存预算")
	}
	os.WriteFile(p, []byte(`{"comment_cache_budget":0}`), 0600)
	if c, err = Load(p); err != nil || c.CommentCacheBudget != 0 {
		t.Fatal("无法关闭正文缓存", err)
	}
	if _, err = Load("../../config.example.json"); err != nil {
		t.Fatalf("示例配置无效: %v", err)
	}
}

// TestFrameCreatorPolicy 验证旧版开关和白名单语义，不再接受全局 admins 或静态 frames。
func TestFrameCreatorPolicy(t *testing.T) {
	c := Defaults()
	if !c.OpenToEveryone || c.MaxFramesPerUser != 3 || !c.CanCreateFrame("github-1") {
		t.Fatal("开放创建默认值错误")
	}
	c.OpenToEveryone = false
	c.AllowedCreators = []string{"github-1"}
	if !c.CanCreateFrame("github-1") || c.CanCreateFrame("github-2") || c.CanCreateFrame("alice") {
		t.Fatal("白名单授权错误")
	}
	p := filepath.Join(t.TempDir(), "config.json")
	for _, raw := range []string{`{"admins":[]}`, `{"frames":["blog"]}`, `{"allowed_creators":["alice"]}`, `{"allowed_creators":["github-001"]}`, `{"allowed_creators":["github-1","github-1"]}`, `{"max_frames_per_user":0}`, `{"max_frames_per_user":-1}`} {
		if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Fatal("接受无效创建策略", raw)
		}
	}
	if err := os.WriteFile(p, []byte(`{"open_to_everyone":false,"allowed_creators":["github-123456"],"max_frames_per_user":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(p)
	if err != nil || !loaded.CanCreateFrame("github-123456") || loaded.CanCreateFrame("github-1") || loaded.MaxFramesPerUser != 2 {
		t.Fatal("创建策略未正确加载", err)
	}
}
