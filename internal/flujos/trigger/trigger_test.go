package trigger

import (
	"context"
	"testing"
)

func TestNoopResolver(t *testing.T) {
	r := NewNoopResolver()
	ctx := context.Background()

	dec, err := r.Resolve(ctx, "tenant-a", "sess-1", Signal{Text: "hola"})
	if err != nil {
		t.Fatalf("Resolve err: %v", err)
	}
	if dec.Action != Ignore {
		t.Fatalf("Action = %v, quiero Ignore", dec.Action)
	}

	matched, msg, err := r.IsEscape(ctx, "tenant-a", "sess-1", "cancelar")
	if err != nil {
		t.Fatalf("IsEscape err: %v", err)
	}
	if matched || msg != "" {
		t.Fatalf("matched=%v msg=%q, quiero false,''", matched, msg)
	}
}

func TestKindsAndMatchTypes(t *testing.T) {
	if KindKeyword != "keyword" || KindFallback != "fallback" || KindEscape != "escape" || KindLLM != "llm" {
		t.Fatal("constantes Kind desalineadas")
	}
	if MatchExact != "exact" || MatchContains != "contains" {
		t.Fatal("constantes MatchType desalineadas")
	}
}
