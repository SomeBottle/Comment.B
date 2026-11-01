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
