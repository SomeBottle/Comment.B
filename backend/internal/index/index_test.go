package index

import (
	"reflect"
	"testing"
)

// TestCanonicalID 验证 96 位随机标识及非零填充位拒绝策略。
func TestCanonicalID(t *testing.T) {
	for i := 0; i < 100; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatal(err)
		}
		s := id.String()
		got, err := ParseID(s)
		if err != nil || got != id || len(s) != 20 {
			t.Fatalf("ID roundtrip: %s %v", s, err)
		}
		bad := s[:19] + "b"
		if _, err = ParseID(bad); err == nil {
			t.Fatal("接受了非规范填充位")
		}
	}
	for _, s := range []string{"", "../../bad", "VVdy232mnv7pxm7rmxiq", "vvdy232mnv7pxm7rmxiqq", "11111111111111111111"} {
		if _, err := ParseID(s); err == nil {
			t.Fatalf("接受了无效 ID %q", s)
		}
	}
}

// TestStablePagination 验证同时间键、乱序插入、删除游标对象及双向分页。
func TestStablePagination(t *testing.T) {
	idx := New(nil)
	keys := []SortKey{}
	for i := 0; i < 30; i++ {
		id := CommentID{}
		id[0] = byte(i * 7)
		keys = append(keys, SortKey{CreatedAt: int64(i/5 + 1), ID: id})
	}
	for i := len(keys) - 1; i >= 0; i-- {
		idx.Insert(keys[i])
		idx.Insert(keys[i])
	}
	if len(idx.Keys) != len(keys) {
		t.Fatal("重复插入改变大小")
	}
	for _, desc := range []bool{false, true} {
		got := []SortKey{}
		var after *SortKey
		for {
			p, more := idx.Page(after, desc, 3)
			got = append(got, p...)
			if !more {
				break
			}
			last := p[len(p)-1]
			after = &last
		}
		want := append([]SortKey(nil), idx.Keys...)
		if desc {
			for i, j := 0, len(want)-1; i < j; i, j = i+1, j-1 {
				want[i], want[j] = want[j], want[i]
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("稳定分页顺序错误")
		}
	}
	boundary := idx.Keys[10]
	idx.Remove(boundary)
	p, _ := idx.Page(&boundary, false, 100)
	for _, k := range p {
		if !Less(boundary, k) {
			t.Fatal("删除游标对象后边界错误")
		}
	}
	idx.Remove(keys[0])
	idx.Insert(SortKey{CreatedAt: 100, ID: CommentID{1}})
	if idx.Keys[len(idx.Keys)-1].CreatedAt != 100 {
		t.Fatal("尾部追加失败")
	}
}
