package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Aserciones de compilación de lo que promete repository_memory_settings.go.
var (
	_ func(*store.MemoryRepository, context.Context, string) (store.TenantSettings, error)                  = (*store.MemoryRepository).GetTenantSettings
	_ func(*store.MemoryRepository, store.TenantSettings)                                                   = (*store.MemoryRepository).SetTenantSettings
	_ func(*store.MemoryRepository, context.Context, store.Key, time.Time) (store.WelcomeMark, error)       = (*store.MemoryRepository).TouchContact
	_ func(*store.MemoryRepository, context.Context, store.Key, store.WelcomeMark, time.Time) (bool, error) = (*store.MemoryRepository).MarkWelcomed
	_ func(*store.MemoryRepository, store.Key) store.WelcomeMark                                            = (*store.MemoryRepository).Welcome
)

// TestMemoryRepository_SetTenantSettings_SeedsTheRowLiterally: la siembra es un INSERT que nombra
// TODAS las columnas: lo que no se rellena queda en el cero de Go, NO en el valor de plataforma.
// Un TenantSettings a medio construir deja la conversación sin vencimiento y la agregación
// apagada; para sembrar un tenant realista se parte de DefaultTenantSettings.
func TestMemoryRepository_SetTenantSettings_SeedsTheRowLiterally(t *testing.T) {
	repo, _ := newMemoryRepository()
	repo.SetTenantSettings(store.TenantSettings{TenantID: "t1", PageSize: 9})
	got, err := repo.GetTenantSettings(ctx, "t1")
	if err != nil {
		t.Fatalf("GetTenantSettings: %v", err)
	}
	if got.PageSize != 9 || got.ConversationTTL != 0 || got.EventInactivityTTL != 0 || got.AggregationWindow != 0 ||
		got.AggregationMax != 0 || got.WelcomeSilence != 0 || got.WelcomeText != "" {
		t.Errorf("la fila sembrada a medias = %+v, quería ceros donde no se rellenó", got)
	}

	realistic := store.DefaultTenantSettings("t1")
	realistic.ConversationTTL = 15 * time.Minute
	repo.SetTenantSettings(realistic)
	got, err = repo.GetTenantSettings(ctx, "t1")
	if err != nil || got.ConversationTTL != 15*time.Minute || got.AggregationWindow != store.DefaultAggregationWindow {
		t.Errorf("la fila sembrada otra vez = (%+v, %v), quería la nueva entera", got, err)
	}
	other, err := repo.GetTenantSettings(ctx, "t2")
	if err != nil || other.TenantID != "t2" || other.ConversationTTL != store.DefaultConversationTTL {
		t.Errorf("el tenant sin fila = (%+v, %v), quería los valores de plataforma", other, err)
	}
}

// TestMemoryRepository_Welcome_MirrorsTheRow: el mirador devuelve la fila de la bienvenida de una
// conversación —la marca cero si no hay fila— tal como la dejan TouchContact y MarkWelcomed, con
// los instantes que pasa el LLAMANTE (el reloj inyectado no interviene).
func TestMemoryRepository_Welcome_MirrorsTheRow(t *testing.T) {
	repo, clock := newMemoryRepository()
	key := store.Key{TenantID: "t1", SessionID: "s1", ContactID: "573001112233"}
	t1 := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	t2 := t1.Add(90 * time.Second)

	if got := repo.Welcome(key); got != (store.WelcomeMark{}) {
		t.Fatalf("Welcome sin fila = %+v, quería la marca cero", got)
	}
	previous, err := repo.TouchContact(ctx, key, t1)
	if err != nil || previous != (store.WelcomeMark{}) {
		t.Fatalf("primer TouchContact = (%+v, %v), quería la marca cero", previous, err)
	}
	if got := repo.Welcome(key); !got.LastIncomingAt.Equal(t1) || !got.WelcomedAt.IsZero() {
		t.Errorf("Welcome tras el toque = %+v, quería (%v, cero)", got, t1)
	}
	if ok, err := repo.MarkWelcomed(ctx, key, previous, t2); err != nil || !ok {
		t.Fatalf("MarkWelcomed = (%v, %v), quería (true, nil)", ok, err)
	}
	if got := repo.Welcome(key); !got.LastIncomingAt.Equal(t1) || !got.WelcomedAt.Equal(t2) {
		t.Errorf("Welcome tras el sello = %+v, quería (%v, %v)", got, t1, t2)
	}
	if clock.Now().Equal(t1) || clock.Now().Equal(t2) {
		t.Fatal("el reloj del test coincide con los instantes del llamante: el caso no distingue nada")
	}
	other := store.Key{TenantID: "t1", SessionID: "s2", ContactID: "573001112233"}
	if got := repo.Welcome(other); got != (store.WelcomeMark{}) {
		t.Errorf("Welcome de otra sesión = %+v, quería la marca cero", got)
	}
}
