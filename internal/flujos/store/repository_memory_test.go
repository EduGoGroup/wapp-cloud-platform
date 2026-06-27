package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

func sampleFlow(flowID string) model.Flow {
	return model.Flow{
		FlowID:  flowID,
		Version: 1, // se ignora: la versión la asigna el repositorio.
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {Type: model.NodeTypeMenu, Prompt: "Elige", Options: map[string]string{"1": "fin"}},
			"fin":  {Type: model.NodeTypeMessage, Text: "Listo", Next: nil},
		},
	}
}

func TestMemory_SaveLoadExists(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	key := store.Key{TenantID: "t1", SessionID: "s1", Contact: "573001112233"}

	exists, err := repo.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatal("no debería existir estado todavía")
	}
	if _, found, err := repo.Load(ctx, key); err != nil || found {
		t.Fatalf("Load vacío: found=%v err=%v", found, err)
	}

	st := model.Conversation{
		TenantID:        key.TenantID,
		SessionID:       key.SessionID,
		Contact:         key.Contact,
		FlowID:          "menu-soporte",
		FlowVersion:     3,
		CurrentNode:     "root",
		Vars:            map[string]any{"reprompt": float64(2), "nombre": "Ana"},
		LastWaMessageID: "wamid.AAA",
	}
	if err := repo.Save(ctx, st); err != nil {
		t.Fatalf("Save: %v", err)
	}

	exists, err = repo.Exists(ctx, key)
	if err != nil || !exists {
		t.Fatalf("Exists tras Save: exists=%v err=%v", exists, err)
	}
	got, found, err := repo.Load(ctx, key)
	if err != nil || !found {
		t.Fatalf("Load: found=%v err=%v", found, err)
	}
	if got.FlowVersion != 3 || got.CurrentNode != "root" || got.LastWaMessageID != "wamid.AAA" {
		t.Fatalf("estado leído inesperado: %+v", got)
	}
	if got.Vars["reprompt"] != float64(2) || got.Vars["nombre"] != "Ana" {
		t.Fatalf("vars JSONB ida y vuelta inesperadas: %+v", got.Vars)
	}
}

func TestMemory_LoadClonesVars(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	key := store.Key{TenantID: "t1", SessionID: "s1", Contact: "c1"}
	if err := repo.Save(ctx, model.Conversation{
		TenantID: "t1", SessionID: "s1", Contact: "c1",
		FlowID: "f", FlowVersion: 1, CurrentNode: "root",
		Vars: map[string]any{"reprompt": float64(2)},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, _, err := repo.Load(ctx, key)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Mutar el clon no debe afectar el estado almacenado.
	got.Vars["reprompt"] = float64(99)
	again, _, err := repo.Load(ctx, key)
	if err != nil {
		t.Fatalf("Load 2: %v", err)
	}
	if again.Vars["reprompt"] != float64(2) {
		t.Fatalf("mutar el clon afectó el estado almacenado: %+v", again.Vars)
	}
}

func TestMemory_SaveUpsert(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	st := model.Conversation{
		TenantID: "t1", SessionID: "s1", Contact: "c1",
		FlowID: "f", FlowVersion: 1, CurrentNode: "root", Vars: map[string]any{},
	}
	if err := repo.Save(ctx, st); err != nil {
		t.Fatalf("Save 1: %v", err)
	}
	st.CurrentNode = "fin"
	st.LastWaMessageID = "wamid.B"
	if err := repo.Save(ctx, st); err != nil {
		t.Fatalf("Save 2 (upsert): %v", err)
	}
	got, _, err := repo.Load(ctx, store.Key{TenantID: "t1", SessionID: "s1", Contact: "c1"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.CurrentNode != "fin" || got.LastWaMessageID != "wamid.B" {
		t.Fatalf("upsert no aplicó: %+v", got)
	}
}

func TestMemory_InsertDefinitionIncrementsVersion(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryRepository()

	v1, err := repo.InsertDefinition(ctx, "t1", sampleFlow("menu"))
	if err != nil {
		t.Fatalf("InsertDefinition v1: %v", err)
	}
	if v1 != 1 {
		t.Fatalf("primera versión: got %d, want 1", v1)
	}
	v2, err := repo.InsertDefinition(ctx, "t1", sampleFlow("menu"))
	if err != nil {
		t.Fatalf("InsertDefinition v2: %v", err)
	}
	if v2 != 2 {
		t.Fatalf("segunda versión: got %d, want 2", v2)
	}

	// Otro flow del mismo tenant arranca su propia secuencia.
	vOther, err := repo.InsertDefinition(ctx, "t1", sampleFlow("otro"))
	if err != nil {
		t.Fatalf("InsertDefinition otro: %v", err)
	}
	if vOther != 1 {
		t.Fatalf("versión de otro flow: got %d, want 1", vOther)
	}
}

func TestMemory_LatestDefinition(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryRepository()

	if _, err := repo.LatestDefinition(ctx, "t1", "menu"); !errors.Is(err, store.ErrDefinitionNotFound) {
		t.Fatalf("LatestDefinition sin definiciones: want ErrDefinitionNotFound, got %v", err)
	}

	if _, err := repo.InsertDefinition(ctx, "t1", sampleFlow("menu")); err != nil {
		t.Fatalf("InsertDefinition v1: %v", err)
	}
	if _, err := repo.InsertDefinition(ctx, "t1", sampleFlow("menu")); err != nil {
		t.Fatalf("InsertDefinition v2: %v", err)
	}

	latest, err := repo.LatestDefinition(ctx, "t1", "menu")
	if err != nil {
		t.Fatalf("LatestDefinition: %v", err)
	}
	if latest.Version != 2 {
		t.Fatalf("LatestDefinition devolvió versión %d, want 2 (la mayor)", latest.Version)
	}
	if latest.FlowID != "menu" || latest.Initial != "root" {
		t.Fatalf("definición leída inesperada: %+v", latest)
	}
}
