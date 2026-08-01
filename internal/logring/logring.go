// Package logring keeps the daemon's recent log records in memory so an operator
// can read them without finding a file.
//
// It is a ring on purpose. A daemon that runs for weeks must not accumulate log
// records, and the only ones worth showing are the recent ones: an operator
// opening the admin page is asking "what just happened", not "what happened in
// March".
package logring

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// DefaultCapacity is how many records are kept.
const DefaultCapacity = 500

// Record is one log line, flattened for display.
type Record struct {
	At      time.Time `json:"at"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	// Fields are the structured attributes, rendered as text. Values are not
	// typed here because the only consumer displays them.
	Fields map[string]string `json:"fields,omitempty"`
}

// Handler is an slog.Handler that both forwards to another handler and keeps a
// bounded history.
type Handler struct {
	next  slog.Handler
	ring  *ring
	attrs []slog.Attr
	group string
}

type ring struct {
	mu       sync.Mutex
	records  []Record
	next     int
	capacity int
	full     bool
}

// New wraps a handler, keeping the most recent records.
func New(next slog.Handler, capacity int) *Handler {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Handler{next: next, ring: &ring{records: make([]Record, capacity), capacity: capacity}}
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *Handler) Handle(ctx context.Context, record slog.Record) error {
	entry := Record{
		At:      record.Time,
		Level:   record.Level.String(),
		Message: record.Message,
		Fields:  map[string]string{},
	}
	for _, attr := range h.attrs {
		entry.Fields[h.key(attr.Key)] = attr.Value.String()
	}
	record.Attrs(func(attr slog.Attr) bool {
		entry.Fields[h.key(attr.Key)] = attr.Value.String()
		return true
	})
	if len(entry.Fields) == 0 {
		entry.Fields = nil
	}
	h.ring.add(entry)
	return h.next.Handle(ctx, record)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	combined := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	combined = append(combined, h.attrs...)
	combined = append(combined, attrs...)
	return &Handler{next: h.next.WithAttrs(attrs), ring: h.ring, attrs: combined, group: h.group}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	group := name
	if h.group != "" {
		group = h.group + "." + name
	}
	return &Handler{next: h.next.WithGroup(name), ring: h.ring, attrs: h.attrs, group: group}
}

func (h *Handler) key(name string) string {
	if h.group == "" {
		return name
	}
	return h.group + "." + name
}

// Records returns the history, oldest first.
func (h *Handler) Records() []Record { return h.ring.snapshot() }

func (r *ring) add(record Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[r.next] = record
	r.next = (r.next + 1) % r.capacity
	if r.next == 0 {
		r.full = true
	}
}

func (r *ring) snapshot() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()

	count := r.next
	if r.full {
		count = r.capacity
	}
	out := make([]Record, 0, count)
	if r.full {
		out = append(out, r.records[r.next:]...)
	}
	out = append(out, r.records[:r.next]...)
	return out
}

// Text renders a record the way a log line reads.
func (r Record) Text() string {
	var builder strings.Builder
	builder.WriteString(r.At.Format(time.RFC3339))
	builder.WriteString(" ")
	builder.WriteString(r.Level)
	builder.WriteString(" ")
	builder.WriteString(r.Message)
	for key, value := range r.Fields {
		builder.WriteString(" ")
		builder.WriteString(key)
		builder.WriteString("=")
		builder.WriteString(value)
	}
	return builder.String()
}
