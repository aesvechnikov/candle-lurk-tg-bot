package tg

import (
	"context"
	"fmt"
	"log"
	"strings"

	gotdlog "github.com/gotd/log"
)

// gotdLogger bridges gotd's structured logger to the stdlib log package, so
// connection/auth events end up in stderr (captured by journald). Only
// Info-level and above is recorded to keep the journal small.
type gotdLogger struct{}

func (gotdLogger) Enabled(_ context.Context, level gotdlog.Level) bool {
	return level >= gotdlog.LevelInfo
}

func (gotdLogger) Log(_ context.Context, level gotdlog.Level, msg string, attrs ...gotdlog.Attr) {
	if level < gotdlog.LevelInfo {
		return
	}
	parts := make([]string, 0, len(attrs))
	for _, a := range attrs {
		parts = append(parts, fmt.Sprintf("%s=%v", a.Key, a.Value))
	}
	log.Printf("[gotd:%v] %s %s", level, msg, strings.Join(parts, " "))
}
