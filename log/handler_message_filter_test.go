package log

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestMessageFilterPreservesCriticalRecordsAndContext(t *testing.T) {
	var out bytes.Buffer
	base := slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := MessageFilterHandler(base, []string{"Chain head was updated"})
	l := slog.New(h.WithGroup("node").WithAttrs([]slog.Attr{slog.String("origin", "test")}))
	l.Info("noisy peer record")
	l.Info("Chain head was updated", "number", 1)
	l.Warn("unlisted warning")
	l.Error("unlisted error")
	s := out.String()
	if strings.Contains(s, "noisy peer") || !strings.Contains(s, "Chain head was updated") || !strings.Contains(s, "node.origin=test") || !strings.Contains(s, "node.number=1") || !strings.Contains(s, "unlisted warning") || !strings.Contains(s, "unlisted error") {
		t.Fatal(s)
	}
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("changed configured level")
	}
	out.Reset()
	slog.New(MessageFilterHandler(base, nil)).Info("unfiltered")
	if !strings.Contains(out.String(), "unfiltered") {
		t.Fatal("empty filter suppressed records")
	}
}
