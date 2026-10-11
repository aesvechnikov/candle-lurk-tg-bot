package config

import (
	"strings"
	"testing"
)

func TestSplitList(t *testing.T) {
	got := splitList("alice, @bob, , carol")
	want := []string{"alice", "bob", "carol"}
	if len(got) != len(want) {
		t.Fatalf("splitList = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("splitList[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if got := splitList(""); len(got) != 0 {
		t.Errorf("splitList(\"\") = %v, want empty", got)
	}
}

func TestParseSessionKey(t *testing.T) {
	valid := strings.Repeat("ab", 32) // 64 hex chars
	key, err := parseSessionKey(valid)
	if err != nil {
		t.Fatalf("parseSessionKey(valid) error: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("parseSessionKey len = %d, want 32", len(key))
	}

	if _, err := parseSessionKey("abc"); err == nil {
		t.Error("parseSessionKey(short) should fail")
	}
	if _, err := parseSessionKey(strings.Repeat("zz", 32)); err == nil {
		t.Error("parseSessionKey(non-hex) should fail")
	}
}
