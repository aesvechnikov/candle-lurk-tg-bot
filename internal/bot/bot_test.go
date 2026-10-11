package bot

import (
	"errors"
	"strings"
	"testing"
)

func TestMarkdownToHTML(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"**bold**", "<b>bold</b>"},
		{"текст **жирный** тут", "текст <b>жирный</b> тут"},
		{"[text](https://t.me/ch/1)", `<a href="https://t.me/ch/1">text</a>`},
		{"a < b & c > d", "a &lt; b &amp; c &gt; d"},
		{"plain text", "plain text"},
		{"**a** и [b](u)", "<b>a</b> и <a href=\"u\">b</a>"},
	}
	for _, c := range cases {
		if got := markdownToHTML(c.in); got != c.want {
			t.Errorf("markdownToHTML(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSplitMessage(t *testing.T) {
	if got := splitMessage("short", 100); len(got) != 1 || got[0] != "short" {
		t.Fatalf("splitMessage(short) = %v", got)
	}

	long := strings.Repeat("a", 250)
	got := splitMessage(long, 100)
	if len(got) != 3 {
		t.Fatalf("splitMessage(long) returned %d chunks, want 3", len(got))
	}
	for _, c := range got {
		if len(c) > 100 {
			t.Errorf("chunk too long: %d", len(c))
		}
	}
}

func TestFriendlyLoginError(t *testing.T) {
	if got := friendlyLoginError(errors.New("context deadline exceeded")); !strings.Contains(got, "Время") {
		t.Errorf("deadline error not recognized: %q", got)
	}
	if got := friendlyLoginError(errors.New("boom")); !strings.HasPrefix(got, "Ошибка входа") {
		t.Errorf("generic error not prefixed: %q", got)
	}
}
