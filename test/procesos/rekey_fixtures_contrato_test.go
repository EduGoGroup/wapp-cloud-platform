//go:build integracion

package procesos

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Los fixtures de rekey_contrato_test.go (parte de ese fichero, 05 E-13): los keyrings de antes y
// después de una rotación, la siembra por SQL de los OCHO sobres del censo y la lectura cruda de
// cada uno. Se siembra por SQL y no por los adaptadores porque lo que se prueba es la rotación, y
// la rotación no conoce a ningún adaptador: solo las columnas (dek, kek_id) de cada sobre.

const (
	// rekeyOldKeyID es la KEK bajo la que se siembra; rekeyNewKeyID, la current tras rotar; y
	// rekeyOtherKeyID, una segunda KEK vieja, para ver el mapa de pendientes con dos claves.
	rekeyOldKeyID   = "A"
	rekeyNewKeyID   = "B"
	rekeyOtherKeyID = "C"

	// rekeyTimeout acota cada pasada y cada lectura. No es un margen de carrera: una rotación que
	// no avanza (un UPDATE que no mueve el key_id deja la fila pendiente para siempre) gira sin
	// fin, y con este tope el test falla con su mensaje en vez de colgarse hasta el -timeout.
	rekeyTimeout = 30 * time.Second
)

// Los claros de los ocho sobres. Son PII y secretos de mentira sobre una base efímera, y aun así
// ningún mensaje de fallo los imprime: se habla de sobres y de key_ids. El nombre lleva espacio y
// mayúsculas porque es texto libre y no se normaliza (0069).
const (
	rekeyPhone       = "573001110001"
	rekeyPushName    = "Nombre De Prueba"
	rekeySelfPn      = "56984467443"
	rekeyBuyer       = `{"rut":"11.111.111-1"}`
	rekeyBridgeSig   = "valor-de-prueba-de-la-firma-del-puente"
	rekeyProviderKey = "clave-inventada-del-proveedor-llm"
	rekeyLiteral     = `{"source_text":"cliente: Hola, quería dos tortas","evidence":{"0":"quería dos tortas"}}`
	rekeyJobText     = "cliente: Hola\ncliente: quería dos tortas para el sábado"
)

// rekeyCases cuenta las bases pedidas en esta corrida: cada test necesita la suya con nombre
// propio, porque la rotación barre la base ENTERA (WHERE kek_id <> current, sin filtrar empresa).
var rekeyCases atomic.Int64

// newRekeyDB clona una base con nuevaBase (que la borra en el Cleanup del test) y la abre con el
// arnés: la única conexión del caso (D-F9-6). La plantilla llega migrada y sin un solo sobre, así
// que los conteos son exactos sin limpiar nada: el `wipeCifradas` del test viejo no tiene aquí
// nada que hacer.
func newRekeyDB(t *testing.T) *sql.DB {
	t.Helper()
	return nuevaBase(t, fmt.Sprintf("rekey_contrato_%02d", rekeyCases.Add(1))).Abrir(t)
}

// rekeyKeys es el material de un caso: una KEK por key_id y la clave del índice ciego, que es
// EXPLÍCITA y la misma en todos los keyrings (sin IndexB64 el proveedor la derivaría de la KEK
// current, y rotar movería el índice: hallazgo 30 de F3).
type rekeyKeys struct {
	kek   map[string]string
	index string
}

// newRekeyKeys genera tres KEK aleatorias (A, B y C) y la clave del índice.
func newRekeyKeys(t *testing.T) rekeyKeys {
	t.Helper()
	return rekeyKeys{
		kek: map[string]string{
			rekeyOldKeyID:   clavesSecretoB64(t),
			rekeyNewKeyID:   clavesSecretoB64(t),
			rekeyOtherKeyID: clavesSecretoB64(t),
		},
		index: clavesSecretoB64(t),
	}
}

// rekeyRing es un despliegue del servidor visto desde la rotación: el proveedor de llaves con un
// keyring concreto y su cifrador de campo. Dos rings sobre la misma base son el servidor antes y
// después de cambiar WAPP_KEK_KEYRING y WAPP_KEK_CURRENT.
type rekeyRing struct {
	kp     crypto.KeyProvider
	cipher *crypto.FieldCipher
}

// ring construye el ring cuyo keyring tiene las KEK ids, con current como vigente. Falla el test
// si el keyring no es válido.
func (k rekeyKeys) ring(t *testing.T, current string, ids ...string) rekeyRing {
	t.Helper()
	entries := make([]string, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, id+":"+k.kek[id])
	}
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: strings.Join(entries, ","),
		CurrentID:  current,
		IndexB64:   k.index,
	})
	if err != nil {
		t.Fatalf("KeyProvider con las KEK %v y current %q: %v", ids, current, err)
	}
	return rekeyRing{kp: kp, cipher: crypto.NewFieldCipher(kp)}
}

// rekeyEnvelope es UN sobre sembrado —no una fila: la de contacts lleva dos—: cómo se llama, qué
// claro guarda, los bytes con que se sembró y cómo se lee de vuelta.
type rekeyEnvelope struct {
	name  string // "tabla.sobre", para los mensajes de fallo
	plain string // el claro que debe seguir saliendo
	enc   []byte // el dato cifrado sembrado: la rotación no lo toca
	dek   []byte // la DEK envuelta sembrada: la rotación la cambia por otra
	read  string // SELECT <enc>, <dek>, <kek_id> de la fila del sobre
	args  []any
}

// rekeyStored es el trío de un sobre tal como está en la base.
type rekeyStored struct {
	enc, dek []byte
	keyID    string
}

// stored lee el trío del sobre por SQL. Falla el test si la fila no está o el trío tiene un NULL.
func (e rekeyEnvelope) stored(t *testing.T, db *sql.DB) rekeyStored {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), rekeyTimeout)
	defer cancel()
	var s rekeyStored
	if err := db.QueryRowContext(ctx, e.read, e.args...).Scan(&s.enc, &s.dek, &s.keyID); err != nil {
		t.Fatalf("%s: leer el sobre: %v", e.name, err)
	}
	return s
}

// seal cifra plain con la KEK current del ring y devuelve el sobre a medio describir: quien lo
// siembra le pone el nombre y la lectura.
func (r rekeyRing) seal(t *testing.T, name, plain string) (rekeyEnvelope, string) {
	t.Helper()
	enc, dek, keyID, err := r.cipher.Encrypt(plain)
	if err != nil {
		// El claro no va en el mensaje: basta con saber qué sobre falló.
		t.Fatalf("%s: Encrypt: %v", name, err)
	}
	return rekeyEnvelope{name: name, plain: plain, enc: enc, dek: dek}, keyID
}

// seedRekeyTenant crea una empresa (public.contacts y public.fleet_sessions la referencian por
// clave foránea) y devuelve su UUID.
func seedRekeyTenant(t *testing.T, db *sql.DB) string {
	t.Helper()
	slug := "rekey-" + edgeAleatorioHex(t, 6)
	id := p2Text(t, db, `INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`, slug, "Rotación "+slug)
	if id == "" {
		t.Fatalf("sembrar la empresa %q: sin id", slug)
	}
	return id
}

// seedRekeyNamelessContact siembra un contacto del que WhatsApp nunca dio nombre: el sobre del
// número bajo la KEK current del ring y las tres columnas del sobre del nombre OMITIDAS del INSERT,
// que es como nacen en producción (la 0069 las creó NULLables y sin DEFAULT a propósito).
func seedRekeyNamelessContact(t *testing.T, db *sql.DB, r rekeyRing, tenant, phone string) rekeyEnvelope {
	t.Helper()
	e, keyID := r.seal(t, "contacts.value (contacto sin nombre)", phone)
	bidx := r.kp.BlindIndex(tenant, phone)
	p10Exec(t, db, `INSERT INTO public.contacts (tenant_id, kind, value_bidx, value_enc, value_dek, value_kek_id)
		VALUES ($1, 'phone_e164', $2, $3, $4, $5)`, tenant, bidx, e.enc, e.dek, keyID)
	e.read = `SELECT value_enc, value_dek, value_kek_id FROM public.contacts WHERE tenant_id = $1 AND value_bidx = $2`
	e.args = []any{tenant, bidx}
	return e
}

// seedRekeyContact siembra los DOS sobres de una misma fila de public.contacts: el del número
// (Plan 011) y el del nombre (0069). Es una fila física con dos DEK independientes; por eso
// devuelve dos sobres con un solo INSERT.
func seedRekeyContact(t *testing.T, db *sql.DB, r rekeyRing, tenant string) []rekeyEnvelope {
	t.Helper()
	value, valueKey := r.seal(t, "contacts.value", rekeyPhone)
	name, nameKey := r.seal(t, "contacts.push_name", rekeyPushName)
	// Guarda contra el copiar y pegar: si los dos sobres compartieran bytes, rotarlos por separado
	// no probaría nada. Dos Encrypt distintos nunca coinciden.
	if bytes.Equal(value.dek, name.dek) || bytes.Equal(value.enc, name.enc) {
		t.Fatal("los dos sobres de la fila de contacts comparten DEK envuelta o dato cifrado")
	}
	bidx := r.kp.BlindIndex(tenant, rekeyPhone)
	p10Exec(t, db, `INSERT INTO public.contacts
			(tenant_id, kind, value_bidx, value_enc, value_dek, value_kek_id, push_name_enc, push_name_dek, push_name_kek_id)
		VALUES ($1, 'phone_e164', $2, $3, $4, $5, $6, $7, $8)`,
		tenant, bidx, value.enc, value.dek, valueKey, name.enc, name.dek, nameKey)
	value.read = `SELECT value_enc, value_dek, value_kek_id FROM public.contacts WHERE tenant_id = $1 AND value_bidx = $2`
	name.read = `SELECT push_name_enc, push_name_dek, push_name_kek_id FROM public.contacts WHERE tenant_id = $1 AND value_bidx = $2`
	value.args, name.args = []any{tenant, bidx}, []any{tenant, bidx}
	return []rekeyEnvelope{value, name}
}

// seedRekeySession siembra la sesión de flota con el sobre del número propio y su índice ciego. Las
// cuatro columnas van juntas: el índice se siembra para poder afirmar después que la rotación no
// lo tocó (lo calcula la clave del índice, que no rota con la KEK).
func seedRekeySession(t *testing.T, db *sql.DB, r rekeyRing, tenant string) rekeyEnvelope {
	t.Helper()
	e, keyID := r.seal(t, "fleet_sessions.self_pn", rekeySelfPn)
	p10Exec(t, db, `INSERT INTO public.fleet_sessions
			(tenant_id, edge_id, session_id, state, self_pn_enc, self_pn_dek, self_pn_kek_id, self_pn_bidx,
			 last_connected_at, last_seen_at, updated_at)
		VALUES ($1, 'edge-rekey', 'sesion-rekey', 'online', $2, $3, $4, $5, now(), now(), now())`,
		tenant, e.enc, e.dek, keyID, r.kp.BlindIndex(tenant, rekeySelfPn))
	e.read = `SELECT self_pn_enc, self_pn_dek, self_pn_kek_id FROM public.fleet_sessions WHERE tenant_id = $1`
	e.args = []any{tenant}
	return e
}

// seedRekeyIntake siembra la cadena empresa → evento → solicitud (desde la 0054 una solicitud nueva
// declara a su evento padre) y los TRES sobres que cuelgan de ella: los datos del comprador, el
// literal del cliente en su revisión —la única tabla del censo sin updated_at— y el texto de la
// ventana de captación de un job en vuelo. Este último es la OCTAVA entrada del censo, la que el
// test viejo no sembraba (hallazgo 58 de F9).
func seedRekeyIntake(t *testing.T, db *sql.DB, r rekeyRing, tenant string) []rekeyEnvelope {
	t.Helper()
	event := p2Text(t, db, `INSERT INTO public.conversation_events
			(tenant_id, session_id, contact_id, kind, history_id, status, flow_id, flow_version)
		VALUES ($1::uuid, 'sesion-rekey', $2::uuid, 'cart', 'cart-rekey-0001', 'open', 'flujo-rekey', 1)
		RETURNING id::text`, tenant, uuidAleatorio(t))
	intake := uuidAleatorio(t)
	p10Exec(t, db, `INSERT INTO public.intakes (id, tenant_id, contact_id, session_id, status, total, event_id)
		VALUES ($1::uuid, $2, 'contacto-opaco', 'sesion-rekey', 'open', 0, $3::uuid)`, intake, tenant, event)

	buyer, buyerKey := r.seal(t, "intake_buyer_data.data", rekeyBuyer)
	p10Exec(t, db, `INSERT INTO public.intake_buyer_data (intake_id, data_enc, data_dek, data_kek_id)
		VALUES ($1::uuid, $2, $3, $4)`, intake, buyer.enc, buyer.dek, buyerKey)
	buyer.read = `SELECT data_enc, data_dek, data_kek_id FROM public.intake_buyer_data WHERE intake_id = $1::uuid`
	buyer.args = []any{intake}

	literal, literalKey := r.seal(t, "intake_revisions.literal", rekeyLiteral)
	p10Exec(t, db, `INSERT INTO public.intake_revisions
			(intake_id, revision_no, kind, payload, created_by, literal_enc, literal_dek, literal_kek_id)
		VALUES ($1::uuid, 1, 'interpreted', '{"version":1,"lines":[]}'::jsonb, 'system', $2, $3, $4)`,
		intake, literal.enc, literal.dek, literalKey)
	literal.read = `SELECT literal_enc, literal_dek, literal_kek_id FROM public.intake_revisions WHERE intake_id = $1::uuid AND revision_no = 1`
	literal.args = []any{intake}

	text, textKey := r.seal(t, "intake_jobs.source_text", rekeyJobText)
	job := p2Text(t, db, `INSERT INTO public.intake_jobs
			(tenant_id, session_id, contact_id, event_id, status, source_text_enc, source_text_dek, source_text_kek_id)
		VALUES ($1, 'sesion-rekey', 'contacto-opaco', $2::uuid, 'pending', $3, $4, $5)
		RETURNING id::text`, tenant, event, text.enc, text.dek, textKey)
	text.read = `SELECT source_text_enc, source_text_dek, source_text_kek_id FROM public.intake_jobs WHERE id = $1::uuid`
	text.args = []any{job}

	return []rekeyEnvelope{buyer, literal, text}
}

// seedRekeyTenantSecrets siembra los dos sobres que guardan un secreto de la empresa: la firma del
// puente CRM y la API key de la vía LLM `api` —una credencial de un tercero, de la que wApp no
// tiene copia; `via` va explícita porque el DEFAULT 'local' de la 0073 no admite sobre—.
func seedRekeyTenantSecrets(t *testing.T, db *sql.DB, r rekeyRing, tenant string) []rekeyEnvelope {
	t.Helper()
	bridge, bridgeKey := r.seal(t, "tenant_integrations.secret", rekeyBridgeSig)
	p10Exec(t, db, `INSERT INTO public.tenant_integrations
			(tenant_id, catalog_adapter, events_adapter, endpoint_url, secret_enc, secret_dek, secret_kek_id, enabled)
		VALUES ($1, 'local', 'webhook', 'https://puente.example/hook', $2, $3, $4, true)`,
		tenant, bridge.enc, bridge.dek, bridgeKey)
	bridge.read = `SELECT secret_enc, secret_dek, secret_kek_id FROM public.tenant_integrations WHERE tenant_id = $1`
	bridge.args = []any{tenant}

	llm, llmKey := r.seal(t, "tenant_llm.api_key", rekeyProviderKey)
	p10Exec(t, db, `INSERT INTO public.tenant_llm
			(tenant_id, via, provider, model, api_key_enc, api_key_dek, api_key_kek_id, consented_at)
		VALUES ($1, 'api', 'anthropic', 'claude-sonnet-4-5', $2, $3, $4, now())`, tenant, llm.enc, llm.dek, llmKey)
	llm.read = `SELECT api_key_enc, api_key_dek, api_key_kek_id FROM public.tenant_llm WHERE tenant_id = $1`
	llm.args = []any{tenant}

	return []rekeyEnvelope{bridge, llm}
}

// seedRekeyCensus siembra los OCHO sobres del censo de la rotación bajo la KEK current del ring,
// sobre SIETE filas de SIETE tablas: la fila de contacts aporta dos. Su longitud es el número que
// se compara con Report.Processed y con el mapa de pendientes, que cuentan sobres y no filas.
func seedRekeyCensus(t *testing.T, db *sql.DB, r rekeyRing, tenant string) []rekeyEnvelope {
	t.Helper()
	envelopes := seedRekeyContact(t, db, r, tenant)
	envelopes = append(envelopes, seedRekeySession(t, db, r, tenant))
	envelopes = append(envelopes, seedRekeyIntake(t, db, r, tenant)...)
	envelopes = append(envelopes, seedRekeyTenantSecrets(t, db, r, tenant)...)
	return envelopes
}

// rekeyEmptyTrios cuenta las piezas de sobre pobladas en las filas que se sembraron SIN sobre (las
// de seedRekeyRowsWithoutEnvelope): tiene que dar 0 antes y después de rotar.
const rekeyEmptyTrios = `SELECT
	(SELECT coalesce(sum(num_nonnulls(secret_enc, secret_dek, secret_kek_id)), 0) FROM public.tenant_integrations WHERE tenant_id = $1) +
	(SELECT coalesce(sum(num_nonnulls(api_key_enc, api_key_dek, api_key_kek_id)), 0) FROM public.tenant_llm WHERE tenant_id = $1) +
	(SELECT coalesce(sum(num_nonnulls(source_text_enc, source_text_dek, source_text_kek_id)), 0) FROM public.intake_jobs WHERE tenant_id = $1) +
	(SELECT coalesce(sum(num_nonnulls(self_pn_enc, self_pn_dek, self_pn_kek_id, self_pn_bidx)), 0) FROM public.fleet_sessions WHERE tenant_id::text = $1)`

// seedRekeyRowsWithoutEnvelope siembra, en OTRA empresa, una fila sin sobre en cada tabla del censo
// cuyo trío es NULLable: una integración sin secreto, la vía LLM local, un job con la ventana
// todavía abierta y una sesión sin emparejar. El barrido no las ve (`NULL <> 'x'` no es TRUE en
// SQL): ni cuentan como pendientes ni nadie les inventa un sobre. El test viejo solo sembraba la
// primera. Devuelve la empresa.
func seedRekeyRowsWithoutEnvelope(t *testing.T, db *sql.DB) string {
	t.Helper()
	tenant := seedRekeyTenant(t, db)
	p10Exec(t, db, `INSERT INTO public.tenant_integrations (tenant_id, catalog_adapter, events_adapter) VALUES ($1, 'local', 'local')`, tenant)
	p10Exec(t, db, `INSERT INTO public.tenant_llm (tenant_id, via) VALUES ($1, 'local')`, tenant)
	event := p2Text(t, db, `INSERT INTO public.conversation_events
			(tenant_id, session_id, contact_id, kind, history_id, status, flow_id, flow_version)
		VALUES ($1::uuid, 'sesion-rekey', $2::uuid, 'cart', 'cart-rekey-0002', 'open', 'flujo-rekey', 1)
		RETURNING id::text`, tenant, uuidAleatorio(t))
	p10Exec(t, db, `INSERT INTO public.intake_jobs (tenant_id, session_id, contact_id, event_id, status)
		VALUES ($1, 'sesion-rekey', 'contacto-opaco', $2::uuid, 'aggregating')`, tenant, event)
	p10Exec(t, db, `INSERT INTO public.fleet_sessions (tenant_id, edge_id, session_id, state, last_connected_at, last_seen_at, updated_at)
		VALUES ($1, 'edge-rekey', 'sesion-sin-numero', 'online', now(), now(), now())`, tenant)
	if n := consultaEntero(t, db, rekeyEmptyTrios, tenant); n != 0 {
		t.Fatalf("las filas sin sobre nacen con %d piezas de sobre pobladas, quería 0", n)
	}
	return tenant
}
