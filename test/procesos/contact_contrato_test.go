//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact/contacthelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Este fichero corre la suite de contrato de contact.Resolver (contacthelpertest.Contrato) contra
// contact.PostgresResolver sobre una base clonada de la plantilla migrada (T1.13, R1.3.c). No es
// un proceso de caja negra: es la excepción de R9.4.d que decidió D-F1-8 (2026-10-02), y por eso
// solo importa la suite, el paquete del adaptador (su constructor) y crypto (sus argumentos).

const (
	// contactFlowID rellena flow_id, la única columna NOT NULL de public.flow_state que la marca no
	// gobierna: es igual en todas las filas sembradas y no significa nada para el contrato.
	contactFlowID = "contrato"

	// contactVarsKey es la clave del único par de vars (JSONB) de una fila sembrada: su valor es la
	// marca.
	contactVarsKey = "mark"

	// contactTimeout acota cada sentencia del adaptador: la base es local al contenedor de la corrida.
	contactTimeout = 15 * time.Second
)

// contactEventNamespace es el espacio de nombres fijo del que sale el event_id de cada fila sembrada
// (UUID v5 de la marca): la misma marca da siempre el mismo UUID, y marcas distintas, UUID distintos.
var contactEventNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("wapp-cloud-platform/test/procesos/contact_contrato/flow_state"))

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
// La marca (D-F1-7) identifica la fila ENTERA, no una columna: viaja en las cinco columnas de
// contenido que una fusión podría mezclar. current_node la lleva tal cual y es la que Dueno
// devuelve; vars, last_wa_message_id, event_id y flow_version llevan lo que deriveStateColumns saca
// de ella. La fusión correcta poda la fila del huérfano o le cambia solo el contact_id (fuseDB), así
// que la fila que sobrevive sigue concordando consigo misma; una que copie al canónico alguna columna
// del huérfano deja una fila mezclada, y Dueno falla el test nombrando la columna (hallazgo 35 de F1:
// con la marca solo en current_node, ese defecto pasaba en verde).
type postgresState struct {
	db *sql.DB
}

var _ contacthelpertest.Estado = (*postgresState)(nil)

// stateColumns son las columnas de contenido de una fila de public.flow_state que, además de
// current_node, llevan la marca. Salen todas de ella (deriveStateColumns). Quedan fuera
// owner_event_id (tiene clave foránea) y flow_id (constante).
type stateColumns struct {
	// vars es el JSON de la columna vars: un objeto con un único par, contactVarsKey → marca.
	vars string
	// lastWaMessageID es la marca tal cual (en producción, la marca de idempotencia del motor).
	lastWaMessageID string
	// eventID es el UUID v5 de la marca en contactEventNamespace.
	eventID string
	// flowVersion es el FNV-1a de 32 bits de la marca llevado a [1, math.MaxInt32]: positivo y
	// dentro del INTEGER de la columna.
	flowVersion int64
}

// deriveStateColumns devuelve las columnas que le corresponden a la fila sembrada con mark. Es pura
// y estable: Sembrar escribe lo que devuelve y Dueno lo vuelve a calcular a partir del current_node
// que lee, así que una fila cuyas columnas salen de dos marcas distintas no concuerda consigo misma.
// Falla el test si la marca no se puede serializar.
func deriveStateColumns(t *testing.T, mark string) stateColumns {
	t.Helper()
	vars, err := json.Marshal(map[string]string{contactVarsKey: mark})
	if err != nil {
		t.Fatalf("deriveStateColumns(marca %q): serializar vars: %v", mark, err)
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(mark)) // hash.Hash.Write nunca devuelve error
	return stateColumns{
		vars:            string(vars),
		lastWaMessageID: mark,
		eventID:         uuid.NewSHA1(contactEventNamespace, []byte(mark)).String(),
		flowVersion:     int64(h.Sum32()%math.MaxInt32) + 1,
	}
}

// stateRow es una fila de public.flow_state tal como la lee Dueno: el dueño y las cinco columnas
// que llevan la marca. last_wa_message_id y event_id admiten NULL en la tabla.
type stateRow struct {
	contactID       string
	currentNode     string
	vars            string
	lastWaMessageID sql.NullString
	eventID         sql.NullString
	flowVersion     int64
}

// mixedColumns compara la fila con lo que deriveStateColumns da para su current_node y devuelve una
// frase por columna que no concuerda, con lo que hay y lo que se esperaba; ninguna si la fila es
// entera de una sola siembra. vars se compara como JSON, no como texto: Postgres reescribe el JSONB.
func (r stateRow) mixedColumns(t *testing.T) []string {
	t.Helper()
	want := deriveStateColumns(t, r.currentNode)
	var mixed []string

	var gotVars map[string]string
	if err := json.Unmarshal([]byte(r.vars), &gotVars); err != nil || len(gotVars) != 1 || gotVars[contactVarsKey] != r.currentNode {
		mixed = append(mixed, fmt.Sprintf("vars es %s; quiere %s", r.vars, want.vars))
	}
	if !r.lastWaMessageID.Valid || r.lastWaMessageID.String != want.lastWaMessageID {
		mixed = append(mixed, fmt.Sprintf("last_wa_message_id es %s; quiere %q", describeNullable(r.lastWaMessageID), want.lastWaMessageID))
	}
	if !r.eventID.Valid || r.eventID.String != want.eventID {
		mixed = append(mixed, fmt.Sprintf("event_id es %s; quiere %q", describeNullable(r.eventID), want.eventID))
	}
	if r.flowVersion != want.flowVersion {
		mixed = append(mixed, fmt.Sprintf("flow_version es %d; quiere %d", r.flowVersion, want.flowVersion))
	}
	return mixed
}

// describeNullable devuelve el valor entre comillas, o NULL si la columna venía nula.
func describeNullable(v sql.NullString) string {
	if !v.Valid {
		return "NULL"
	}
	return fmt.Sprintf("%q", v.String)
}

// Sembrar implementa contacthelpertest.Estado con un INSERT … ON CONFLICT DO NOTHING sobre la clave
// primaria (tenant_id, session_id, contact_id): repetir una siembra conserva la primera marca, como
// promete Estado. Escribe la marca en current_node y sus derivadas en las otras cuatro columnas.
// Falla el test si algún argumento viene vacío o si el INSERT falla.
func (s *postgresState) Sembrar(t *testing.T, tenantID, sessionID, contactID, mark string) {
	t.Helper()
	if tenantID == "" || sessionID == "" || contactID == "" || mark == "" {
		t.Fatalf("postgresState.Sembrar(tenant %q, sesión %q, contacto %q, marca %q): los cuatro son obligatorios", tenantID, sessionID, contactID, mark)
	}
	cols := deriveStateColumns(t, mark)
	ctx, cancel := context.WithTimeout(t.Context(), contactTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO public.flow_state
			(tenant_id, session_id, contact_id, flow_id, flow_version, current_node, vars, last_wa_message_id, event_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9::uuid)
		ON CONFLICT (tenant_id, session_id, contact_id) DO NOTHING`,
		tenantID, sessionID, contactID, contactFlowID, cols.flowVersion, mark, cols.vars, cols.lastWaMessageID, cols.eventID)
	if err != nil {
		t.Fatalf("postgresState.Sembrar(tenant %q, sesión %q, contacto %q): %v", tenantID, sessionID, contactID, err)
	}
}

// Dueno implementa contacthelpertest.Estado: lee las filas de flow_state de la sesión del tenant y
// devuelve su contact_id y su marca (la de current_node); ok=false sin filas. Con más de una fila
// falla el test nombrándolas, en vez de elegir una (la fusión no resolvió el conflicto). Y si la
// única fila mezcla contenido de dos siembras —alguna de vars, last_wa_message_id, event_id o
// flow_version no es la que corresponde a su current_node— da el test por fallido con una línea por
// columna y sigue (Errorf, no Fatalf): así el informe trae todas las columnas mezcladas y, además,
// lo que la suite diga del dueño y de la marca.
func (s *postgresState) Dueno(t *testing.T, tenantID, sessionID string) (contactID, mark string, ok bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), contactTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT contact_id::text, current_node, vars::text, last_wa_message_id, event_id::text, flow_version
		FROM public.flow_state
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

	var found []stateRow
	for rows.Next() {
		var r stateRow
		if err := rows.Scan(&r.contactID, &r.currentNode, &r.vars, &r.lastWaMessageID, &r.eventID, &r.flowVersion); err != nil {
			t.Fatalf("postgresState.Dueno(tenant %q, sesión %q): escanear: %v", tenantID, sessionID, err)
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("postgresState.Dueno(tenant %q, sesión %q): iterar: %v", tenantID, sessionID, err)
	}

	switch len(found) {
	case 0:
		return "", "", false
	case 1:
		r := found[0]
		for _, column := range r.mixedColumns(t) {
			t.Errorf("fila de estado mezclada: la sesión %q del tenant %q, de %q, lleva la marca %q en current_node y su %s",
				sessionID, tenantID, r.contactID, r.currentNode, column)
		}
		return r.contactID, r.currentNode, true
	default:
		owners := make([]string, 0, len(found))
		marks := make([]string, 0, len(found))
		for _, r := range found {
			owners = append(owners, r.contactID)
			marks = append(marks, r.currentNode)
		}
		t.Fatalf("estado inconsistente: la sesión %q del tenant %q tiene %d dueños [%s] (marks [%s]) y debía tener uno",
			sessionID, tenantID, len(owners), strings.Join(owners, ", "), strings.Join(marks, ", "))
		return "", "", false
	}
}
