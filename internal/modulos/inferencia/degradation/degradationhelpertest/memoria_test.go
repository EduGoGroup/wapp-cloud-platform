package degradationhelpertest

import (
	"context"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
)

// Memoria cumple el puerto.
var _ degradation.Store = (*Memoria)(nil)

// newTenantID devuelve un UUID v4 nuevo, hecho con la biblioteca estándar.
func newTenantID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("generando un UUID de prueba: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// TestMemoria_Contrato corre la suite del puerto contra el doble. La misma suite corre contra
// degradation.Postgres en F9: lo que pasa aquí es lo que los tests del módulo pueden dar por
// bueno cuando usan Memoria en lugar de Postgres.
func TestMemoria_Contrato(t *testing.T) {
	Contrato(t, func(t *testing.T) Montaje {
		t.Helper()
		store := NewMemoria()
		return Montaje{
			Store: store,
			// La tabla no referencia a public.tenants: un tenant «sembrado» es un UUID nuevo.
			SeedTenant: newTenantID,
			Rows:       func(_ *testing.T, tenantID string) []degradation.Notice { return store.Rows(tenantID) },
			MarkRead: func(t *testing.T, tenantID, id string, at time.Time) {
				t.Helper()
				if !store.MarkRead(tenantID, id, at) {
					t.Fatalf("MarkRead(%s, %s): el aviso no existe", tenantID, id)
				}
			},
		}
	})
}

// TestMemoria_Saves: cuenta cada llamada a Save —nazca el aviso o colapse— y ninguna otra
// operación la cuenta. Es lo que deja afirmar «el store no se tocó» (R4.5.b).
func TestMemoria_Saves(t *testing.T) {
	const tenant = "tenant-contado"
	store := NewMemoria()
	ctx := context.Background()
	if n := store.Saves(); n != 0 {
		t.Fatalf("Saves = %d en un almacén nuevo, quería 0", n)
	}
	n := failure(tenant, degradation.ReasonTimeout, degradation.ViaLocal, baseWindow, baseWindow.Add(time.Minute))
	for i, wantCreated := range []bool{true, false, false} {
		created, err := store.Save(ctx, n)
		if err != nil || created != wantCreated {
			t.Fatalf("Save #%d = (%v, %v), quería (%v, nil)", i, created, err, wantCreated)
		}
	}
	rows := store.Rows(tenant)
	if len(rows) != 1 || rows[0].Occurrences != 3 {
		t.Fatalf("tras tres Save iguales hay %d filas, quería una con tres fallos:%s", len(rows), describe(rows))
	}
	if _, err := store.List(ctx, tenant, degradation.ListFilter{}); err != nil {
		t.Fatalf("List: error inesperado %v", err)
	}
	if !store.MarkRead(tenant, rows[0].ID, readInstant) {
		t.Fatal("MarkRead no encontró el aviso recién escrito")
	}
	if got := store.Saves(); got != 3 {
		t.Errorf("Saves = %d tras tres Save, un List, un Rows y un MarkRead, quería 3", got)
	}
}

// TestMemoria_DoesNotValidateVocabulary: como el puerto, el doble guarda el motivo y la vía que
// le den. Rechazar un motivo sano es trabajo de degradation.Notifier, y su test lo demuestra con
// Saves() == 0: si el doble validara, ese test pasaría por el motivo equivocado.
func TestMemoria_DoesNotValidateVocabulary(t *testing.T) {
	const tenant = "tenant-sin-guarda"
	store := NewMemoria()
	healthy := failure(tenant, "fastlane", "edge", baseWindow, baseWindow.Add(time.Minute))
	if created, err := store.Save(context.Background(), healthy); err != nil || !created {
		t.Fatalf("Save de un motivo fuera del vocabulario = (%v, %v), quería (true, nil)", created, err)
	}
	rows := store.Rows(tenant)
	want := born(healthy)
	if len(rows) != 1 {
		t.Fatalf("quedaron %d filas, quería 1", len(rows))
	}
	want.ID = rows[0].ID
	if !sameNotice(rows[0], want) {
		t.Errorf("fila:%s\nquería:%s", describe(rows), describe([]degradation.Notice{want}))
	}
}

// TestMemoria_RowsAndMarkRead: Rows devuelve copias —mutarlas no toca el almacén—, y MarkRead
// solo marca el aviso de ESE tenant con ESE id, en UTC.
func TestMemoria_RowsAndMarkRead(t *testing.T) {
	const tenant, other = "tenant-propio", "tenant-ajeno"
	store := NewMemoria()
	ctx := context.Background()
	for _, owner := range []string{tenant, other} {
		if _, err := store.Save(ctx, failure(owner, degradation.ReasonTimeout, degradation.ViaAPI, baseWindow, baseWindow)); err != nil {
			t.Fatalf("Save: error inesperado %v", err)
		}
	}
	rows := store.Rows(tenant)
	if len(rows) != 1 {
		t.Fatalf("Rows(%s) devolvió %d filas, quería 1", tenant, len(rows))
	}
	id := rows[0].ID
	rows[0].Occurrences, rows[0].ReadAt = 77, readInstant
	if again := store.Rows(tenant); again[0].Occurrences != 1 || !again[0].ReadAt.IsZero() {
		t.Errorf("mutar lo que devuelve Rows cambió el almacén:%s", describe(again))
	}

	if store.MarkRead(other, id, readInstant) || store.MarkRead(tenant, "otro-id", readInstant) {
		t.Error("MarkRead marcó un aviso con el tenant o el id de otro")
	}
	if !store.Rows(tenant)[0].ReadAt.IsZero() || !store.Rows(other)[0].ReadAt.IsZero() {
		t.Error("un MarkRead fallido dejó un aviso leído")
	}
	if !store.MarkRead(tenant, id, readInstant.In(time.FixedZone("x", 3600))) {
		t.Fatal("MarkRead no encontró el aviso del tenant")
	}
	got := store.Rows(tenant)[0].ReadAt
	if !got.Equal(readInstant) || got.Location() != time.UTC {
		t.Errorf("ReadAt = %s, quería %s en UTC", got, readInstant)
	}
	if !store.Rows(other)[0].ReadAt.IsZero() {
		t.Error("MarkRead marcó también el aviso del otro tenant")
	}
}
