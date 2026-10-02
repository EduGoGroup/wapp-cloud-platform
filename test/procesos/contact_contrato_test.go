//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact/contacthelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Este fichero corre la suite de contrato de contact.Resolver (contacthelpertest.Contrato) contra
// contact.PostgresResolver sobre una base clonada de la plantilla migrada (T1.13, R1.3.c). No es
// un proceso de caja negra: es la excepción de R9.4.d que decidió D-F1-8 (2026-10-02), y por eso
// solo importa la suite, el paquete del adaptador (su constructor) y crypto (sus argumentos).

const (
	// contactFlowID y contactFlowVersion rellenan las columnas NOT NULL de public.flow_state que la
	// suite no observa: el flujo de la fila sembrada no significa nada para el contrato.
	contactFlowID      = "contrato"
	contactFlowVersion = 1

	// contactTimeout acota cada sentencia del adaptador: la base es local al contenedor de la corrida.
	contactTimeout = 15 * time.Second
)

// contactCases cuenta los Montajes pedidos en esta corrida: Contrato llama a nuevo una vez por caso
// (19, en serie) y cada uno necesita una base con nombre propio mientras la anterior siga viva.
var contactCases atomic.Int64

// TestContactContrato_Postgres es R1.3.c: las 19 promesas del puerto contact.Resolver, las mismas
// que pasan contra la memoria, contra el adaptador Postgres. Cada caso recibe su base, sus dos
// tenants (filas reales de public.tenants: public.contacts los referencia por clave foránea) y sus
// claves de PII, todo nuevo.
func TestContactContrato_Postgres(t *testing.T) {
	contacthelpertest.Contrato(t, newContactMontaje)
}

// newContactMontaje devuelve el Montaje limpio de un caso: clona una base con nuevaBase (que la
// borra en el Cleanup del subtest), la abre con el arnés, siembra los dos tenants por SQL (D-F1-8:
// no con el repositorio de tenants, para no importar nada más de internal/) y construye el
// PostgresResolver con un KeyProvider de claves aleatorias de esta corrida.
func newContactMontaje(t *testing.T) contacthelpertest.Montaje {
	t.Helper()
	proceso := fmt.Sprintf("contact_contrato_%02d", contactCases.Add(1))
	db := nuevaBase(t, proceso).Abrir(t)

	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		MasterB64: clavesSecretoB64(t),
		IndexB64:  clavesSecretoB64(t),
	})
	if err != nil {
		t.Fatalf("KeyProvider del contrato: %v", err)
	}

	return contacthelpertest.Montaje{
		Resolver: contact.NewPostgresResolver(db, crypto.NewFieldCipher(kp), kp),
		TenantA:  seedContactTenant(t, db, "contrato-a"),
		TenantB:  seedContactTenant(t, db, "contrato-b"),
		Estado:   &postgresState{db: db},
	}
}

// seedContactTenant inserta un tenant con el slug dado en public.tenants y devuelve su id. Solo
// rellena las columnas NOT NULL sin valor por defecto (0001_tenants.sql). Falla el test si no puede.
func seedContactTenant(t *testing.T, db *sql.DB, slug string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), contactTimeout)
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

// postgresState es el contacthelpertest.Estado de Postgres: siembra y observa public.flow_state de
// la misma base que usa el PostgresResolver, que migra esas filas en su propia transacción de fusión.
//
// La marca (D-F1-7) va en current_node: es TEXT NOT NULL, no forma parte de la clave primaria y la
// fusión no la toca —poda la fila del huérfano o le cambia solo el contact_id (fuseDB)—, así que
// la marca que se lee después es la de la fila que sobrevivió. vars (JSONB) obligaría a serializar,
// y flow_id/flow_version no se distinguen mejor; ninguna otra columna se observa.
type postgresState struct {
	db *sql.DB
}

var _ contacthelpertest.Estado = (*postgresState)(nil)

// Sembrar implementa contacthelpertest.Estado con un INSERT … ON CONFLICT DO NOTHING sobre la clave
// primaria (tenant_id, session_id, contact_id): repetir una siembra conserva la primera marca, como
// promete Estado. Falla el test si algún argumento viene vacío o si el INSERT falla.
func (s *postgresState) Sembrar(t *testing.T, tenantID, sessionID, contactID, mark string) {
	t.Helper()
	if tenantID == "" || sessionID == "" || contactID == "" || mark == "" {
		t.Fatalf("postgresState.Sembrar(tenant %q, sesión %q, contacto %q, marca %q): los cuatro son obligatorios", tenantID, sessionID, contactID, mark)
	}
	ctx, cancel := context.WithTimeout(t.Context(), contactTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO public.flow_state (tenant_id, session_id, contact_id, flow_id, flow_version, current_node)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, session_id, contact_id) DO NOTHING`,
		tenantID, sessionID, contactID, contactFlowID, contactFlowVersion, mark)
	if err != nil {
		t.Fatalf("postgresState.Sembrar(tenant %q, sesión %q, contacto %q): %v", tenantID, sessionID, contactID, err)
	}
}

// Dueno implementa contacthelpertest.Estado: lee las filas de flow_state de la sesión del tenant y
// devuelve su contact_id y su marca; ok=false sin filas. Con más de una fila falla el test
// nombrándolas, en vez de elegir una (la fusión no resolvió el conflicto).
func (s *postgresState) Dueno(t *testing.T, tenantID, sessionID string) (contactID, mark string, ok bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), contactTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT contact_id::text, current_node FROM public.flow_state
		WHERE tenant_id = $1 AND session_id = $2
		ORDER BY contact_id`,
		tenantID, sessionID)
	if err != nil {
		t.Fatalf("postgresState.Dueno(tenant %q, sesión %q): %v", tenantID, sessionID, err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("postgresState.Dueno(tenant %q, sesión %q): cerrar rows: %v", tenantID, sessionID, err)
		}
	}()

	var owners, marks []string
	for rows.Next() {
		var id, m string
		if err := rows.Scan(&id, &m); err != nil {
			t.Fatalf("postgresState.Dueno(tenant %q, sesión %q): escanear: %v", tenantID, sessionID, err)
		}
		owners = append(owners, id)
		marks = append(marks, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("postgresState.Dueno(tenant %q, sesión %q): iterar: %v", tenantID, sessionID, err)
	}

	switch len(owners) {
	case 0:
		return "", "", false
	case 1:
		return owners[0], marks[0], true
	default:
		t.Fatalf("estado inconsistente: la sesión %q del tenant %q tiene %d dueños [%s] (marks [%s]) y debía tener uno",
			sessionID, tenantID, len(owners), strings.Join(owners, ", "), strings.Join(marks, ", "))
		return "", "", false
	}
}
