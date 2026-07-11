package intentcfg

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestMemoryStore_GetNotFound(t *testing.T) {
	s := NewMemoryStore()
	if _, err := s.Get(context.Background(), "tenant-x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get sobre store vacío: err=%v, quería ErrNotFound", err)
	}
}

func TestMemoryStore_UpsertYGet(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	blob := []byte(`{"version":"v1"}`)
	if err := s.Upsert(ctx, "tenant-a", "abc123", blob); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.Get(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Version != "abc123" || !bytes.Equal(got.Blob, blob) {
		t.Fatalf("config recuperada inesperada: %+v", got)
	}
	// El blob devuelto es una COPIA: mutarlo no altera lo almacenado.
	got.Blob[0] = 'X'
	again, err := s.Get(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("Get de nuevo: %v", err)
	}
	if !bytes.Equal(again.Blob, blob) {
		t.Fatalf("el store aliasó el blob: %s", again.Blob)
	}
}

func TestMemoryStore_UpsertReemplaza(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	if err := s.Upsert(ctx, "tenant-a", "v-old", []byte(`{"a":1}`)); err != nil {
		t.Fatalf("Upsert v-old: %v", err)
	}
	if err := s.Upsert(ctx, "tenant-a", "v-new", []byte(`{"b":2}`)); err != nil {
		t.Fatalf("Upsert v-new: %v", err)
	}
	got, err := s.Get(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Version != "v-new" || string(got.Blob) != `{"b":2}` {
		t.Fatalf("Upsert no reemplazó: %+v", got)
	}
}
