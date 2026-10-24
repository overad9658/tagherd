package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// logHandler writes "tagherd: warning: message (key=value)" lines, which read
// better on a terminal than slog's key=value text format.
type logHandler struct {
	mu    *sync.Mutex
	w     io.Writer
	level slog.Level
	attrs []slog.Attr
}

func newLogHandler(w io.Writer, level slog.Level) *logHandler {
	return &logHandler{mu: &sync.Mutex{}, w: w, level: level}
}

func (h *logHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *logHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString("tagherd: ")
	b.WriteString(levelName(r.Level))
	b.WriteString(": ")
	b.WriteString(r.Message)
	var kv []string
	add := func(a slog.Attr) bool {
		if v := a.Value.String(); v != "" {
			kv = append(kv, a.Key+"="+v)
		}
		return true
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(add)
	if len(kv) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(kv, ", "))
	}
	b.WriteByte('\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &c
}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warning"
	case l >= slog.LevelInfo:
		return "info"
	}
	return "debug"
}

// WithGroup is not used by tagherd; groups are flattened.
func (h *logHandler) WithGroup(string) slog.Handler { return h }
