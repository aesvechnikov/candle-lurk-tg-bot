package tg

import "testing"

func TestCleanUsername(t *testing.T) {
	cases := map[string]string{
		"@foo":   "foo",
		"foo":    "foo",
		"  @bar ": "bar",
	}
	for in, want := range cases {
		if got := cleanUsername(in); got != want {
			t.Errorf("cleanUsername(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMessageLink(t *testing.T) {
	if got := messageLink("channel", 42); got != "https://t.me/channel/42" {
		t.Errorf("messageLink = %q", got)
	}
	if got := messageLink("", 42); got != "" {
		t.Errorf("messageLink(empty username) = %q, want empty", got)
	}
}
