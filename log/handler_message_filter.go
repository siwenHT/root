package log

import (
	"context"
	"log/slog"
)

type messageFilterHandler struct {
	inner   slog.Handler
	allowed map[string]struct{}
}

// MessageFilterHandler keeps selected routine messages without suppressing
// warnings/errors or changing the underlying logger's level and attributes.
func MessageFilterHandler(inner slog.Handler, messages []string) slog.Handler {
	if len(messages) == 0 {
		return inner
	}
	allowed := make(map[string]struct{}, len(messages))
	for _, message := range messages {
		allowed[message] = struct{}{}
	}
	return &messageFilterHandler{inner: inner, allowed: allowed}
}
func (h *messageFilterHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}
func (h *messageFilterHandler) Handle(ctx context.Context, record slog.Record) error {
	if _, ok := h.allowed[record.Message]; !ok && record.Level < slog.LevelWarn {
		return nil
	}
	return h.inner.Handle(ctx, record)
}
func (h *messageFilterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &messageFilterHandler{inner: h.inner.WithAttrs(attrs), allowed: h.allowed}
}
func (h *messageFilterHandler) WithGroup(name string) slog.Handler {
	return &messageFilterHandler{inner: h.inner.WithGroup(name), allowed: h.allowed}
}
