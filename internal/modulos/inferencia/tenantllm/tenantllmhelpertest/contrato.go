// Package tenantllmhelpertest es la suite de contrato del puerto tenantllm.Store y su doble en
// memoria, Memoria. Ningún código de producción lo importa: arrastra "testing" y el doble guarda
// la clave en claro.
//
//   - contrato.go: la entrada. Montaje, Row, Contrato, la tabla de casos, la marca de estado y
//     las ayudas.
//   - upsert_contrato.go: los casos de Upsert (las dos vías, el reemplazo completo, los tres
//     rechazos que no escriben).
//   - store_contrato.go: los casos de Get, APIKey y Delete, el aislamiento por tenant y la
//     concurrencia.
//   - memoria.go: Memoria, el Store en memoria.
//
// La suite la corren las dos implementaciones del puerto: Memoria en unitario (memoria_test.go)
// y tenantllm.Postgres en los procesos de F9 (test/procesos), con el arnés de testcontainers.
//
// Los casos salen de plan/F4-inferencia/diseno.md §2 y de los tests viejos de
// internal/tenantllm/postgres_integration_test.go @ ebf4eb7, leídos, no portados. De sus quince,
// aquí está la conducta de diez; los otros cinco no son del puerto: TestCheck_* afirma los CHECK
// de la tabla por SQL crudo y los cuatro TestBackfill0073_* son de la migración (van a F9).
//
// Para añadir un caso: escribe su función en el fichero de su tema y añade su fila a cases().
package tenantllmhelpertest

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
)

// Montaje es lo que cada implementación entrega a la suite para UN caso. Tiene que venir limpio
// —sin filas de configuración LLM— porque Contrato llama a nuevo una vez por caso.
type Montaje struct {
	// Store es la implementación bajo prueba.
	Store tenantllm.Store
	// SeedTenant devuelve el id de un tenant que la suite puede usar: tiene forma de UUID, es
	// distinto en cada llamada y NO tiene fila de configuración LLM. public.tenant_llm guarda el
	// tenant como TEXT sin clave foránea, así que hoy ni Postgres ni Memoria necesitan crear nada;
	// va por función para que un montaje pueda sembrar el tenant el día que la tabla lo exija.
	// Falla el test t si no puede.
	SeedTenant func(t *testing.T) string
	// Row es el observador de estado: la forma de la fila del tenant, columna a columna, que el
	// puerto no deja ver entera (Get no dice nada de api_key_dek ni de api_key_kek_id). found es
	// false si el tenant no tiene fila. Con Postgres es un SELECT de los seis `IS NOT NULL` sobre
	// public.tenant_llm; con Memoria, Memoria.Row. Falla el test t si no puede leer.
	Row func(t *testing.T, tenantID string) (row Row, found bool)
}

// Row es la forma de una fila de public.tenant_llm: su vía y cuáles de las seis columnas del eje
// api están rellenas (`IS NOT NULL`). No lleva ningún valor: ni la clave ni el blob salen por aquí.
type Row struct {
	Via         string
	HasProvider bool // provider
	HasModel    bool // model
	HasKeyEnc   bool // api_key_enc
	HasKeyDEK   bool // api_key_dek
	HasKEKID    bool // api_key_kek_id
	HasConsent  bool // consented_at
}

// Contrato ejecuta las promesas de tenantllm.Store contra la implementación que devuelve nuevo,
// con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta nada.
//
// La marca de estado (rowState) con la que la suite afirma «esto no escribió» vigila TODAS las
// columnas que una operación puede tocar, no solo la que el caso mira (hallazgo 35 de F1): los
// ocho campos de Config, las seis presencias de Row y lo que contesta APIKey.
//
// Lo que la suite NO afirma, a propósito, porque las dos implementaciones divergen o porque el
// puerto no lo deja ver:
//   - el CIFRADO en reposo: Memoria guarda la clave en claro. Que api_key_enc no lleve la clave
//     lo afirma el test del adaptador (postgres_test.go) sobre lo que llega al driver, y F9 por SQL.
//   - con qué KEK se descifra (T-15): el `api_key_kek_id` de la fila y no la current. Memoria no
//     tiene KEK. Lo fija postgres_test.go y lo cubre F9.
//   - los CHECK de la tabla (tenant_llm_via_api_completa_check, …_sobre_completo_check,
//     …_local_sin_credencial_check, …_provider_check): el puerto no sabe escribir una fila que
//     los viole. Por eso la suite solo usa proveedores del vocabulario y modelos no vacíos: con
//     un proveedor inventado Postgres devuelve el error del CHECK y Memoria lo guarda.
//   - una fila `via='local'` CON sobre (el cinturón y tirantes de APIKey): por el puerto no se
//     puede fabricar. Lo fija postgres_test.go.
//   - los valores exactos de CreatedAt y UpdatedAt: en Postgres son el now() del servidor. Se
//     afirma que CreatedAt no cambia al reemplazar y que UpdatedAt no retrocede.
//   - la zona horaria de los instantes devueltos (se comparan con Equal) y la precisión por
//     debajo del segundo (la suite usa segundos enteros, que sobreviven a un timestamptz).
//   - los fallos de infraestructura y el contexto cancelado: Memoria no falla.
//
// Los ficheros de la suite NO comparan por vía con `==` ni con `switch`: afirman la vía dentro
// de un valor entero (un Config o un Row esperado). Así no entran en la lista de permitidos del
// candado C2 (I-CP-3: la vía se pregunta en un solo sitio), que en este paquete solo tiene a
// memoria.go.
func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("tenantllmhelpertest.Contrato: nuevo es nil; hace falta una función que devuelva un Montaje")
	}
	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			validateMontaje(t, m)
			c.run(t, m)
		})
	}
}

// contractCase es una promesa del puerto: su nombre (el del t.Run) y la función que la afirma.
type contractCase struct {
	name string
	run  func(t *testing.T, m Montaje)
}

// cases es la tabla de la suite. El comentario de cada fila es la promesa que fija.
func cases() []contractCase {
	return []contractCase{
		{"Get_TenantWithoutRow_NotFound", caseGetWithoutRow},                              // sin fila: found=false, Config cero
		{"APIKey_TenantWithoutRow_ErrNotConfigured", caseAPIKeyWithoutRow},                // R4.4.c
		{"Upsert_API_StoresTheWholeAPIAxis", caseUpsertAPIStoresRow},                      // las seis columnas; Config sin la clave
		{"APIKey_APIRow_ReturnsTheStoredKey", caseAPIKeyReturnsStoredKey},                 // la única salida de la clave
		{"Upsert_API_ReplacesKeyAndConsent_KeepsCreatedAt", caseUpsertAPIReplaces},        // reemplazo completo
		{"Upsert_Local_RowWithoutAPIAxis", caseUpsertLocalRow},                            // fila sin sobre; R4.4.c
		{"Upsert_Local_IgnoresKeyProviderModelAndConsent", caseUpsertLocalIgnoresAPIAxis}, // la vía decide qué se escribe
		{"Upsert_APIToLocal_RemovesCredentialAndConsent", caseUpsertAPIToLocal},           // R4.4.d
		{"Upsert_LocalToAPI_StoresTheCredential", caseUpsertLocalToAPI},                   // la vuelta
		{"Upsert_ViaOutOfVocabulary_RejectedWithoutWriting", caseUpsertRejectsVia},        // R4.4.e
		{"Upsert_APIWithoutKey_RejectedWithoutWriting", caseUpsertRejectsMissingKey},      // R4.4.e
		{"Upsert_APIWithoutConsent_RejectedWithoutWriting", caseUpsertRejectsNoConsent},   // R4.4.e
		{"Delete_RemovesRowCredentialAndConsent_Idempotent", caseDeleteRevokes},           // revoca de una vez
		{"Delete_TenantWithoutRow_NoError", caseDeleteWithoutRow},                         // borrar lo que no hay
		{"Rows_AreIsolatedByTenant", caseRowsIsolatedByTenant},                            // INV-7
		{"ConcurrentUpserts_LeaveOneCoherentRow", caseConcurrentUpserts},                  // cada upsert es la foto entera
	}
}

// validateMontaje exige lo que la suite da por hecho de un Montaje.
func validateMontaje(t *testing.T, m Montaje) {
	t.Helper()
	switch {
	case m.Store == nil:
		t.Fatal("Montaje.Store es nil")
	case m.SeedTenant == nil:
		t.Fatal("Montaje.SeedTenant es nil: la suite necesita tenants")
	case m.Row == nil:
		t.Fatal("Montaje.Row es nil: la suite necesita observar la forma de la fila")
	}
}

// Las credenciales de la suite son inventadas (T-16: cero gasto, ninguna clave real). Llevan el
// prefijo público del proveedor a propósito: es la cadena que se busca en lo que no debe llevarla.
const (
	keyFirst   = "sk-ant-api03-CLAVE-FALSA-DE-CONTRATO-0001" // #nosec G101 -- clave de prueba inventada, no una credencial real
	keyRotated = "sk-ant-api03-CLAVE-FALSA-ROTADA-0002"      // #nosec G101 -- clave de prueba inventada, no una credencial real
	keyPrefix  = "sk-ant-"

	modelFirst  = "claude-sonnet-4-5"
	modelSecond = "claude-opus-4-1"
)

// Los instantes del consentimiento van en segundos enteros y en UTC para que sobrevivan sin
// pérdida a un timestamptz de Postgres.
var (
	consentFirst  = time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC)
	consentSecond = time.Date(2032, 4, 5, 6, 7, 8, 0, time.UTC)

	uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

	// Las dos formas legítimas de una fila: el eje api entero o vacío.
	rowAPI   = Row{Via: tenantllm.ViaAPI, HasProvider: true, HasModel: true, HasKeyEnc: true, HasKeyDEK: true, HasKEKID: true, HasConsent: true}
	rowLocal = Row{Via: tenantllm.ViaLocal}
)

// seedTenant pide un tenant y comprueba que su id es un UUID bien formado.
func seedTenant(t *testing.T, m Montaje) string {
	t.Helper()
	tenant := m.SeedTenant(t)
	if !uuidShape.MatchString(tenant) {
		t.Fatalf("Montaje.SeedTenant devolvió %q, que no es un UUID bien formado", tenant)
	}
	return tenant
}

// apiConfig es la configuración de la vía api: la que exige credencial y consentimiento.
func apiConfig(tenant string) tenantllm.Config {
	return tenantllm.Config{TenantID: tenant, Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderAnthropic, Model: modelFirst}
}

// localConfig es la otra vía: una configuración completa sin proveedor, modelo ni credencial.
func localConfig(tenant string) tenantllm.Config {
	return tenantllm.Config{TenantID: tenant, Via: tenantllm.ViaLocal}
}

// upsert llama a Upsert y falla el test si devuelve error.
func upsert(t *testing.T, m Montaje, cfg tenantllm.Config, apiKey string, consentedAt time.Time) {
	t.Helper()
	if err := m.Store.Upsert(context.Background(), cfg, apiKey, consentedAt); err != nil {
		t.Fatalf("Upsert(%s, vía %q): error inesperado %v", cfg.TenantID, cfg.Via, err)
	}
}

// mustGet devuelve la configuración del tenant, que tiene que existir y decir de quién es.
func mustGet(t *testing.T, m Montaje, tenant string) tenantllm.Config {
	t.Helper()
	cfg, found, err := m.Store.Get(context.Background(), tenant)
	if err != nil {
		t.Fatalf("Get(%s): error inesperado %v", tenant, err)
	}
	if !found {
		t.Fatalf("Get(%s): found = false, quería la fila", tenant)
	}
	if cfg.TenantID != tenant {
		t.Errorf("Get(%s) devolvió la fila de %q", tenant, cfg.TenantID)
	}
	return cfg
}

// withoutInstants devuelve cfg sin sus tres instantes, para comparar el resto como un valor: los
// instantes se comparan aparte, con Equal, porque la zona con la que vuelven no es del contrato.
func withoutInstants(cfg tenantllm.Config) tenantllm.Config {
	cfg.ConsentedAt, cfg.CreatedAt, cfg.UpdatedAt = time.Time{}, time.Time{}, time.Time{}
	return cfg
}

// requireConfig afirma que Get devuelve want —campo a campo, y el consentimiento como
// instante— y que la fila tiene alta y última escritura. CreatedAt y UpdatedAt de want no se
// miran: son del reloj de la implementación. Devuelve lo leído.
func requireConfig(t *testing.T, m Montaje, tenant string, want tenantllm.Config) tenantllm.Config {
	t.Helper()
	got := mustGet(t, m, tenant)
	if withoutInstants(got) != withoutInstants(want) {
		t.Errorf("Get(%s) = %+v, quería %+v", tenant, withoutInstants(got), withoutInstants(want))
	}
	if !got.ConsentedAt.Equal(want.ConsentedAt) {
		t.Errorf("Get(%s): ConsentedAt = %v, quería %v", tenant, got.ConsentedAt, want.ConsentedAt)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() || got.UpdatedAt.Before(got.CreatedAt) {
		t.Errorf("Get(%s): CreatedAt = %v, UpdatedAt = %v; quería los dos puestos y UpdatedAt no anterior al alta", tenant, got.CreatedAt, got.UpdatedAt)
	}
	return got
}

// requireRow afirma la forma de la fila del tenant, columna a columna.
func requireRow(t *testing.T, m Montaje, tenant string, want Row) {
	t.Helper()
	got, found := m.Row(t, tenant)
	if !found {
		t.Fatalf("el tenant %s no tiene fila, quería %+v", tenant, want)
	}
	if got != want {
		t.Errorf("fila de %s = %+v, quería %+v", tenant, got, want)
	}
}

// requireAPIKey afirma que APIKey devuelve esa clave. No imprime ninguna de las dos.
func requireAPIKey(t *testing.T, m Montaje, tenant, want string) {
	t.Helper()
	got, err := m.Store.APIKey(context.Background(), tenant)
	if err != nil {
		t.Fatalf("APIKey(%s): error inesperado %v", tenant, err)
	}
	if got != want {
		t.Errorf("APIKey(%s) devolvió una clave distinta de la guardada", tenant)
	}
}

// requireNotConfigured afirma que APIKey contesta ErrNotConfigured y ninguna clave.
func requireNotConfigured(t *testing.T, m Montaje, tenant string) {
	t.Helper()
	got, err := m.Store.APIKey(context.Background(), tenant)
	if !errors.Is(err, tenantllm.ErrNotConfigured) {
		t.Errorf("APIKey(%s): error = %v, quería ErrNotConfigured", tenant, err)
	}
	if got != "" {
		t.Errorf("APIKey(%s) devolvió una clave de %d bytes junto al error, quería \"\"", tenant, len(got))
	}
}

// requireNoRow afirma que el tenant no tiene fila, por las tres vistas: Get, la fila y APIKey.
func requireNoRow(t *testing.T, m Montaje, tenant string) {
	t.Helper()
	cfg, found, err := m.Store.Get(context.Background(), tenant)
	if err != nil || found || cfg != (tenantllm.Config{}) {
		t.Errorf("Get(%s) = (%+v, found=%v, err=%v), quería (Config cero, false, nil)", tenant, cfg, found, err)
	}
	if row, found := m.Row(t, tenant); found {
		t.Errorf("el tenant %s tiene fila (%+v), quería ninguna", tenant, row)
	}
	requireNotConfigured(t, m, tenant)
}

// requireRejected afirma que err es un rechazo del propio store —dice fragment— y no uno que
// haya bajado a la base: el nombre de un CHECK de la tabla en el texto querría decir que la
// guarda previa no está, y un CHECK violado sale por la API como 500 en vez de como 400.
func requireRejected(t *testing.T, err error, fragment string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Upsert devolvió nil, quería el rechazo («%s»)", fragment)
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Errorf("rechazo = %v, quería uno que diga «%s»", err, fragment)
	}
	if strings.Contains(err.Error(), "tenant_llm_") {
		t.Errorf("el rechazo lo dio la base (%v): la guarda previa a la escritura no está", err)
	}
	if strings.Contains(err.Error(), keyPrefix) {
		t.Error("FUGA: el error del rechazo lleva la clave")
	}
}

// rowState es la marca de estado: TODO lo que la suite puede observar de un tenant. Con ella se
// afirma que una operación no escribió nada, en ninguna columna (hallazgo 35).
type rowState struct {
	found         bool
	cfg           tenantllm.Config
	rowFound      bool
	row           Row
	key           string
	notConfigured bool
}

// captureState lee la marca de estado del tenant.
func captureState(t *testing.T, m Montaje, tenant string) rowState {
	t.Helper()
	var s rowState
	var err error
	if s.cfg, s.found, err = m.Store.Get(context.Background(), tenant); err != nil {
		t.Fatalf("Get(%s): error inesperado %v", tenant, err)
	}
	s.row, s.rowFound = m.Row(t, tenant)
	s.key, err = m.Store.APIKey(context.Background(), tenant)
	s.notConfigured = errors.Is(err, tenantllm.ErrNotConfigured)
	if err != nil && !s.notConfigured {
		t.Fatalf("APIKey(%s): error inesperado %v", tenant, err)
	}
	return s
}

// requireSameState afirma que el tenant sigue exactamente como en before. No imprime claves.
func requireSameState(t *testing.T, m Montaje, tenant string, before rowState) {
	t.Helper()
	after := captureState(t, m, tenant)
	a, b := after.cfg, before.cfg
	sameConfig := withoutInstants(a) == withoutInstants(b) && a.ConsentedAt.Equal(b.ConsentedAt) &&
		a.CreatedAt.Equal(b.CreatedAt) && a.UpdatedAt.Equal(b.UpdatedAt)
	if after.found != before.found || !sameConfig {
		t.Errorf("la configuración de %s cambió: (found=%v) %+v, antes (found=%v) %+v", tenant, after.found, a, before.found, b)
	}
	if after.rowFound != before.rowFound || after.row != before.row {
		t.Errorf("la fila de %s cambió: (found=%v) %+v, antes (found=%v) %+v", tenant, after.rowFound, after.row, before.rowFound, before.row)
	}
	if after.key != before.key || after.notConfigured != before.notConfigured {
		t.Errorf("la credencial de %s cambió (sin configurar: %v, antes %v)", tenant, after.notConfigured, before.notConfigured)
	}
}
