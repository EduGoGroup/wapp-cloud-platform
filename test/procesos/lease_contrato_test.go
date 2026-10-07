//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease/leasehelpertest"
)

// Este fichero corre la suite de contrato de lease.Repository
// (leasehelpertest.ContratoRepository) contra lease.PostgresRepository sobre una base clonada de
// la plantilla migrada (T3.30 = T9.24, F3-05). Como contact_contrato_test.go, no es un proceso de
// caja negra: es la excepción de R9.4.d que decidió D-F1-8, y por eso solo importa la suite y el
// paquete del adaptador, del que solo usa el constructor (candado ProcessImports, regla 3).
//
// Es la que prueba de verdad la mitad servidora de la doble llave (ADR-0007) contra las dos tablas
// reales: que el ON CONFLICT de Upsert no resucita una fila revocada de public.leases, que
// MarkRevoked deja rastro aunque el Edge no tuviera fila, y que el corte por tenant
// (public.tenants.revoked_at, migración 0058) y el corte por Edge no se escriben el uno al otro.
// El doble en memoria solo lo imita.
//
// Lo que la suite NO afirma, y aquí tampoco (hallazgo 13 de F3): MarkTenantRevoked sobre un tenant
// que no existe. Postgres no toca ninguna fila y el doble lo deja marcado; por eso SeedTenant
// siembra siempre una fila real.

const (
	// leaseContractTimeout acota cada sentencia de siembra: la base es local al contenedor.
	leaseContractTimeout = 15 * time.Second
)

// leaseContractCases cuenta los Montajes pedidos en esta corrida: ContratoRepository llama a nuevo
// una vez por caso (en serie) y cada uno necesita una base con nombre propio.
var leaseContractCases atomic.Int64

// leaseContractTenants numera los tenants sembrados: la suite pide hasta tres por caso y el slug de
// public.tenants es único dentro de una base.
var leaseContractTenants atomic.Int64

// TestLeaseContrato_Postgres corre las promesas del puerto lease.Repository, las mismas que pasan
// contra leasehelpertest.Memoria, contra el adaptador Postgres. Cada caso recibe su base y un
// repositorio nuevo; los tenants los siembra la suite cuando los necesita.
func TestLeaseContrato_Postgres(t *testing.T) {
	leasehelpertest.ContratoRepository(t, leaseContractNewMontaje)
}

// leaseContractNewMontaje devuelve el Montaje limpio de un caso: clona una base con nuevaBase (que
// la borra en el Cleanup del subtest), la abre con el arnés y construye el PostgresRepository sobre
// ese *sql.DB, que comparte con la siembra de tenants. La base clonada no trae filas de
// public.leases: la plantilla solo está migrada.
func leaseContractNewMontaje(t *testing.T) leasehelpertest.Montaje {
	t.Helper()
	proceso := fmt.Sprintf("lease_contrato_%02d", leaseContractCases.Add(1))
	db := nuevaBase(t, proceso).Abrir(t)

	return leasehelpertest.Montaje{
		Repository: lease.NewPostgresRepository(db),
		SeedTenant: func(t *testing.T) string {
			t.Helper()
			return leaseContractSeedTenant(t, db)
		},
	}
}

// leaseContractSeedTenant inserta un tenant en public.tenants y devuelve su id. Es un tenant que
// EXISTE y está activo, como promete Montaje.SeedTenant: solo rellena las columnas NOT NULL sin
// valor por defecto (0001_tenants.sql), así que revoked_at queda NULL. El slug lleva un número de
// serie para que dos siembras del mismo caso no choquen. Falla el test si no puede.
func leaseContractSeedTenant(t *testing.T, db *sql.DB) string {
	t.Helper()
	slug := fmt.Sprintf("lease-contrato-%03d", leaseContractTenants.Add(1))
	ctx, cancel := context.WithTimeout(t.Context(), leaseContractTimeout)
	defer cancel()
	var id string
	err := db.QueryRowContext(ctx,
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`,
		slug, "Tenant "+slug,
	).Scan(&id)
	if err != nil {
		t.Fatalf("sembrar el tenant %q: %v", slug, err)
	}
	return id
}
