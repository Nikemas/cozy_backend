package reqid

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestContextRoundTrip(t *testing.T) {
	ctx := NewContext(context.Background(), "abc-123")
	if got := FromContext(ctx); got != "abc-123" {
		t.Fatalf("FromContext = %q, want abc-123", got)
	}
	if got := FromContext(context.Background()); got != "" {
		t.Fatalf("FromContext(empty) = %q, want empty", got)
	}
}

func TestNewIsValidAndUnique(t *testing.T) {
	a, b := New(), New()
	if !Valid(a) || len(a) != 32 {
		t.Fatalf("New() = %q, want a valid 32-char id", a)
	}
	if a == b {
		t.Fatal("New() returned the same id twice")
	}
}

func TestValid(t *testing.T) {
	cases := map[string]bool{
		"":                        false,
		"abc-DEF_1.2:3":           true,
		"has space":               false,
		"line\nbreak":             false,
		strings.Repeat("a", 128):  true,
		strings.Repeat("a", 129):  false,
		"<script>":                false,
		"0f8fad5b-d9cb-469f-a165": true,
	}
	for in, want := range cases {
		if got := Valid(in); got != want {
			t.Errorf("Valid(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestLogHandlerAddsRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewLogHandler(slog.NewTextHandler(&buf, nil))).With("component", "test")

	logger.InfoContext(NewContext(context.Background(), "rid-42"), "hello")
	if !strings.Contains(buf.String(), "request_id=rid-42") || !strings.Contains(buf.String(), "component=test") {
		t.Fatalf("log line = %q, want request_id and component attrs", buf.String())
	}

	buf.Reset()
	logger.Info("no ctx")
	if strings.Contains(buf.String(), "request_id") {
		t.Fatalf("log line = %q, want no request_id without a context id", buf.String())
	}
}
