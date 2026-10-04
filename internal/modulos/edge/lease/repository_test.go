package lease_test

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease/leasehelpertest"
)

// Las dos implementaciones del puerto. Sus promesas las fija la suite
// leasehelpertest.ContratoRepository: contra Memoria en leasehelpertest/memoria_test.go y contra
// PostgresRepository en los procesos de F9.
var (
	_ lease.Repository = (*lease.PostgresRepository)(nil)
	_ lease.Repository = (*leasehelpertest.Memoria)(nil)
)

// TestState_ZeroValueMeansLive fija lo que D-F2-10 deja anotado: el cero de State.Revoked es
// «vigente». No es un descuido que haya que corregir al portar; lo que impide que ese cero
// autorice por accidente es el contrato de Upsert, que NUNCA escribe la revocación.
func TestState_ZeroValueMeansLive(t *testing.T) {
	var zero lease.State
	if zero.Revoked {
		t.Fatal("el State cero dice revocado; su cero es «vigente»")
	}

	// Lo que el contrato de Upsert promete sobre ese campo, visto desde el puerto.
	var repo lease.Repository = leasehelpertest.NewMemoria()
	ctx := context.Background()
	expiry := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := repo.MarkRevoked(ctx, "t", "e", expiry); err != nil {
		t.Fatalf("MarkRevoked: error inesperado %v", err)
	}
	// Un State con Revoked en su cero NO des-revoca…
	if err := repo.Upsert(ctx, lease.State{TenantID: "t", EdgeID: "e", Counter: 2, ExpiresAt: expiry}); err != nil {
		t.Fatalf("Upsert: error inesperado %v", err)
	}
	st, found, err := repo.Get(ctx, "t", "e")
	if err != nil || !found {
		t.Fatalf("Get = (found=%v, err=%v), quería la fila", found, err)
	}
	if !st.Revoked {
		t.Error("Upsert con Revoked=false resucitó un lease revocado")
	}
	// … ni uno con Revoked=true revoca.
	if err := repo.Upsert(ctx, lease.State{TenantID: "t", EdgeID: "otro", Counter: 1, ExpiresAt: expiry, Revoked: true}); err != nil {
		t.Fatalf("Upsert: error inesperado %v", err)
	}
	st, _, _ = repo.Get(ctx, "t", "otro")
	if st.Revoked {
		t.Error("Upsert con Revoked=true revocó: para revocar está MarkRevoked")
	}
}
