//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll/enrollhelpertest"
)

// Este fichero corre las dos suites de contrato de enroll contra sus adaptadores Postgres sobre una
// base clonada de la plantilla migrada (T3.30 = T9.24, F3-05):
//
//   - enrollhelpertest.ContratoCodeStore contra enroll.PostgresCodeStore (public.enrollment_codes);
//   - enrollhelpertest.ContratoEdgeCertRepository contra enroll.PostgresEdgeCertRepository
//     (public.edge_certs).
//
// Como contact_contrato_test.go, no es un proceso de caja negra: es la excepción de R9.4.d que
// decidió D-F1-8, y por eso solo importa la suite y el paquete del adaptador, del que solo usa los
// constructores (candado ProcessImports, regla 3).
//
// 🔴 DEL PAQUETE enroll SOLO SE NOMBRAN LOS CONSTRUCTORES, y el montaje de los certificados devuelve
// registros del puerto (Montaje.Records). Los nombra por el alias de la suite,
// enrollhelpertest.EdgeCertRecord (hallazgo 75 de F3; el precedente es outhelpertest.Invitation).
//
// Es la que prueba de verdad lo que los dobles solo imitan: que el UPDATE condicional de Consume es
// atómico bajo el lock de fila (32 consumos a la vez, uno gana), que compara el código tal cual y
// que vence contra el now() del servidor; y que Create guarda los siete campos del certificado.
//
// Lo que las suites NO afirman, y aquí tampoco (hallazgos 4 y 13 de F3): qué centinela devuelve un
// consumo fallido (Postgres siempre ErrCodeInvalid; el doble distingue tres causas), una huella
// repetida (la columna fingerprint es UNIQUE) y un tenant que no existe (lo rechaza la clave
// foránea). Las siembras usan siempre tenants reales.

const (
	// enrollContractTimeout acota cada sentencia de siembra u observación: la base es local al
	// contenedor.
	enrollContractTimeout = 15 * time.Second
)

// enrollContractCases cuenta los montajes pedidos en esta corrida por las DOS suites: cada caso
// necesita una base con nombre propio, y el prefijo del nombre distingue de cuál es.
var enrollContractCases atomic.Int64

// enrollContractTenants numera los tenants sembrados: las suites piden hasta dos por caso y el slug
// de public.tenants es único dentro de una base.
var enrollContractTenants atomic.Int64

// TestEnrollCodeStoreContrato_Postgres corre las promesas del puerto enroll.CodeStore, las mismas
// que pasan contra enrollhelpertest.MemoriaCodeStore, contra el adaptador Postgres. Cada caso
// recibe su base, sin códigos, y un PostgresCodeStore nuevo.
func TestEnrollCodeStoreContrato_Postgres(t *testing.T) {
	enrollhelpertest.ContratoCodeStore(t, enrollContractNewCodeStoreMontaje)
}

// TestEnrollEdgeCertRepositoryContrato_Postgres corre las promesas del puerto
// enroll.EdgeCertRepository, las mismas que pasan contra enrollhelpertest.MemoriaEdgeCertRepository,
// contra el adaptador Postgres. Cada caso recibe su base, sin certificados, y un repositorio nuevo.
func TestEnrollEdgeCertRepositoryContrato_Postgres(t *testing.T) {
	enrollhelpertest.ContratoEdgeCertRepository(t, enrollContractNewEdgeCertMontaje)
}

// enrollContractNewCodeStoreMontaje devuelve el montaje limpio de un caso de ContratoCodeStore:
// clona una base con nuevaBase (que la borra en el Cleanup del subtest), la abre con el arnés y
// construye el PostgresCodeStore sobre ese *sql.DB, que comparte con la siembra.
//
// SeedCode siembra por SQL, no con PostgresCodeStore.Create: así la fila que Consume lee no depende
// del código que se está probando. El código viaja tal cual como argumento, sin tocar.
func enrollContractNewCodeStoreMontaje(t *testing.T) enrollhelpertest.MontajeCodeStore {
	t.Helper()
	proceso := fmt.Sprintf("enroll_codes_contrato_%02d", enrollContractCases.Add(1))
	db := nuevaBase(t, proceso).Abrir(t)

	return enrollhelpertest.MontajeCodeStore{
		Store: enroll.NewPostgresCodeStore(db),
		SeedCode: func(t *testing.T, code string, expiresAt time.Time) string {
			t.Helper()
			tenantID := enrollContractSeedTenant(t, db)
			ctx, cancel := context.WithTimeout(t.Context(), enrollContractTimeout)
			defer cancel()
			// used_at queda NULL (sin usar): la columna no tiene valor por defecto (0002).
			if _, err := db.ExecContext(ctx,
				`INSERT INTO public.enrollment_codes (code, tenant_id, expires_at) VALUES ($1, $2::uuid, $3)`,
				code, tenantID, expiresAt,
			); err != nil {
				t.Fatalf("sembrar el código %q del tenant %s: %v", code, tenantID, err)
			}
			return tenantID
		},
	}
}

// enrollContractNewEdgeCertMontaje devuelve el montaje limpio de un caso de
// ContratoEdgeCertRepository: clona una base, la abre con el arnés y construye el
// PostgresEdgeCertRepository sobre ese *sql.DB, que comparte con la siembra de tenants y con el
// observador Records.
func enrollContractNewEdgeCertMontaje(t *testing.T) enrollhelpertest.MontajeEdgeCertRepository {
	t.Helper()
	proceso := fmt.Sprintf("enroll_certs_contrato_%02d", enrollContractCases.Add(1))
	db := nuevaBase(t, proceso).Abrir(t)

	return enrollhelpertest.MontajeEdgeCertRepository{
		Repository: enroll.NewPostgresEdgeCertRepository(db),
		SeedTenant: func(t *testing.T) string {
			t.Helper()
			return enrollContractSeedTenant(t, db)
		},
		Records: func(t *testing.T) []enrollhelpertest.EdgeCertRecord {
			t.Helper()
			return enrollContractReadCerts(t, db)
		},
	}
}

// enrollContractSeedTenant inserta un tenant en public.tenants y devuelve su id: enrollment_codes y
// edge_certs lo referencian por clave foránea. Solo rellena las columnas NOT NULL sin valor por
// defecto (0001_tenants.sql). El slug lleva un número de serie para que dos siembras del mismo caso
// no choquen y devuelvan tenants distintos. Falla el test si no puede.
func enrollContractSeedTenant(t *testing.T, db *sql.DB) string {
	t.Helper()
	slug := fmt.Sprintf("enroll-contrato-%03d", enrollContractTenants.Add(1))
	ctx, cancel := context.WithTimeout(t.Context(), enrollContractTimeout)
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

// enrollContractReadCerts devuelve TODAS las filas de public.edge_certs de la base del caso, sin
// filtrar por tenant: es el observador que pide Montaje.Records, y un registro de más (o de un
// tenant que no toca) tiene que verse. tenant_id vuelve como texto y cert_pem, que es TEXT, como
// bytes. Falla el test si no puede leer.
func enrollContractReadCerts(t *testing.T, db *sql.DB) []enrollhelpertest.EdgeCertRecord {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), enrollContractTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, `
		SELECT tenant_id::text, subject_cn, serial_number, fingerprint, not_before, not_after, cert_pem
		FROM public.edge_certs
		ORDER BY created_at, fingerprint`)
	if err != nil {
		t.Fatalf("leer public.edge_certs: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("leer public.edge_certs: cerrar rows: %v", err)
		}
	}()

	var found []enrollhelpertest.EdgeCertRecord
	for rows.Next() {
		var (
			r       enrollhelpertest.EdgeCertRecord
			certPEM string
		)
		if err := rows.Scan(&r.TenantID, &r.SubjectCN, &r.SerialNumber, &r.Fingerprint, &r.NotBefore, &r.NotAfter, &certPEM); err != nil {
			t.Fatalf("leer public.edge_certs: escanear: %v", err)
		}
		r.CertPEM = []byte(certPEM)
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("leer public.edge_certs: iterar: %v", err)
	}
	return found
}
