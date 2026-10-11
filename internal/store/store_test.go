package store

import (
	"path/filepath"
	"testing"
)

func TestChannels(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	const uid = int64(1)

	if err := s.AddChannels(uid, []Channel{
		{Name: "@a", Title: "A", About: "first"},
		{Name: "@b"},
	}); err != nil {
		t.Fatalf("AddChannels: %v", err)
	}

	channels, err := s.ListChannels(uid)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("ListChannels = %d channels, want 2", len(channels))
	}
	if channels[0].Name != "@a" || channels[0].About != "first" {
		t.Errorf("channels[0] = %+v, want @a/first", channels[0])
	}

	// Re-adding @a refreshes its metadata without duplicating.
	if err := s.AddChannels(uid, []Channel{{Name: "@a", About: "updated"}}); err != nil {
		t.Fatalf("AddChannels (refresh): %v", err)
	}
	channels, _ = s.ListChannels(uid)
	if len(channels) != 2 {
		t.Fatalf("after refresh: %d channels, want 2", len(channels))
	}
	for _, c := range channels {
		if c.Name == "@a" && c.About != "updated" {
			t.Errorf("@a about = %q, want %q", c.About, "updated")
		}
	}

	if err := s.RemoveChannel(uid, "@b"); err != nil {
		t.Fatalf("RemoveChannel: %v", err)
	}
	channels, _ = s.ListChannels(uid)
	if len(channels) != 1 || channels[0].Name != "@a" {
		t.Errorf("after remove: %+v", channels)
	}
}
