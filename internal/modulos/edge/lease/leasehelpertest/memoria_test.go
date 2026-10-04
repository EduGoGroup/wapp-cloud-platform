package leasehelpertest

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
	"github.com/google/uuid"
)

// Memoria cumple el puerto.
var _ lease.Repository = (*Memoria)(nil)

// TestMemoria_Contrato corre la suite del puerto contra el doble. La misma suite corre contra
// lease.PostgresRepository en F9: lo que pasa aquí es lo que los tests de lease.Manager pueden
// dar por bueno cuando usan Memoria en lugar de Postgres.
func TestMemoria_Contrato(t *testing.T) {
	ContratoRepository(t, func(t *testing.T) Montaje {
		t.Helper()
		return Montaje{
			Repository: NewMemoria(),
			// El doble no sabe de tenants: uno «sembrado» es un UUID nuevo, activo por ausencia.
			SeedTenant: func(*testing.T) string { return uuid.NewString() },
		}
	})
}

// TestMemoria_UnknownTenantCanBeMarked fija la diferencia documentada con Postgres: el doble
// marca como revocado un tenant que nadie sembró (en Postgres el UPDATE no tocaría ninguna
// fila). La suite no lo afirma; los tests de lease.Manager sí se apoyan en ello.
func TestMemoria_UnknownTenantCanBeMarked(t *testing.T) {
	repo := NewMemoria()
	ctx := context.Background()
	if err := repo.MarkTenantRevoked(ctx, "tenant-sin-sembrar"); err != nil {
		t.Fatalf("MarkTenantRevoked: error inesperado %v", err)
	}
	revoked, err := repo.TenantRevoked(ctx, "tenant-sin-sembrar")
	if err != nil || !revoked {
		t.Errorf("TenantRevoked tras marcar un tenant sin sembrar = (%v, %v), quería (true, nil)", revoked, err)
	}
}

// TestMemoria_StampsWithItsClock: issued_at es el reloj de la primera escritura y updated_at el
// de la última, en UTC; una revocación sobre un Edge nunca visto también sella issued_at.
func TestMemoria_StampsWithItsClock(t *testing.T) {
	repo := NewMemoria()
	first := time.Date(2026, 10, 4, 12, 0, 0, 0, time.FixedZone("x", 3600))
	current := first
	repo.now = func() time.Time { return current }
	ctx := context.Background()

	if err := repo.Upsert(ctx, lease.State{TenantID: "t", EdgeID: "e", Counter: 1}); err != nil {
		t.Fatalf("Upsert: error inesperado %v", err)
	}
	current = first.Add(time.Minute)
	if err := repo.Upsert(ctx, lease.State{TenantID: "t", EdgeID: "e", Counter: 2}); err != nil {
		t.Fatalf("Upsert: error inesperado %v", err)
	}
	st, _, _ := repo.Get(ctx, "t", "e")
	if !st.IssuedAt.Equal(first) || st.IssuedAt.Location() != time.UTC {
		t.Errorf("issued_at = %v, quería %v en UTC (el de la primera emisión)", st.IssuedAt, first.UTC())
	}
	if !st.UpdatedAt.Equal(current) {
		t.Errorf("updated_at = %v, quería %v (el de la última escritura)", st.UpdatedAt, current.UTC())
	}

	if err := repo.MarkRevoked(ctx, "t", "nunca-visto", first); err != nil {
		t.Fatalf("MarkRevoked: error inesperado %v", err)
	}
	st, _, _ = repo.Get(ctx, "t", "nunca-visto")
	if !st.IssuedAt.Equal(current) || !st.UpdatedAt.Equal(current) {
		t.Errorf("fila nacida revocada: issued_at = %v, updated_at = %v, quería %v en las dos", st.IssuedAt, st.UpdatedAt, current.UTC())
	}
}
