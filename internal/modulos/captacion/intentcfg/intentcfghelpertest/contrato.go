// Package intentcfghelpertest es la suite de contrato del puerto intentcfg.Store. Ningún código
// de producción lo importa: arrastra "testing".
//
// La suite la corren las dos implementaciones del puerto: intentcfg.MemoryStore en unitario
// (store_test.go) y intentcfg.PostgresStore en los procesos de F9, con el arnés de
// testcontainers. Este paquete NO trae un doble propio: el gemelo en memoria ya existe y es el
// MemoryStore del paquete (trampa T-9 de F7).
//
// Nuevo: no tiene fichero viejo. Los casos salen del contrato de intentcfg.Store y de los tests
// viejos, leídos y no portados: internal/intentcfg/store_test.go (GetNotFound, UpsertYGet,
// UpsertReemplaza) e internal/intentcfg/store_integration_test.go (UpsertGet, de donde sale la
// comparación del blob por equivalencia JSON).
package intentcfghelpertest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
)

// Montaje es lo que cada implementación entrega a la suite para UN caso: Contrato llama a nuevo
// una vez por caso.
type Montaje struct {
	// Store es la implementación bajo prueba.
	Store intentcfg.Store
	// TenantA y TenantB son dos tenant_id distintos, no vacíos y SIN config. tenant_id es TEXT
	// sin clave foránea: no hay fila de tenants que sembrar, basta con que sean únicos por caso.
	TenantA, TenantB string
	// Advance deja pasar el reloj con el que la implementación marca UpdatedAt. Promete que un
	// Upsert posterior a la llamada marca con un instante ESTRICTAMENTE posterior al de
	// cualquier Upsert anterior a ella. Es obligatoria.
	//
	// Por qué existe: «Upsert refresca UpdatedAt» exige dos marcas distintas, y eso depende del
	// reloj de cada implementación (time.Now() en memoria, que no se puede inyectar; now() de la
	// base en Postgres, con resolución de microsegundos). Con Advance la suite no duerme ni
	// adivina: cada implementación espera a que SU reloj pase del instante en que estaba.
	Advance func(t *testing.T)
}

// Contrato ejecuta las promesas de intentcfg.Store contra la implementación que devuelve nuevo,
// con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta nada.
//
// El puerto deja escribir (Upsert) y observar (Get) todo lo que la suite necesita: el Montaje no
// trae ni siembra ni observador. La «marca de estado» de un tenant es su Config entero —version,
// blob y UpdatedAt, las tres columnas que Upsert puede tocar—, y cada caso que escribe en
// TenantA siembra antes un testigo en TenantB y comprueba al final que su marca no se movió.
//
// Lo que la suite NO afirma, a propósito:
//
//   - la identidad de BYTES del blob: MemoryStore lo devuelve byte a byte y Postgres devuelve
//     el texto del JSONB, que reordena claves y normaliza espacios. Se compara por equivalencia
//     JSON, y todos los blobs de la suite son JSON válido con números exactos en float64;
//   - qué pasa con un blob que no es JSON (o nil): MemoryStore lo guarda y Postgres lo rechaza.
//     Validar el blob es del llamante, no del puerto;
//   - el valor concreto de UpdatedAt ni el texto del error de «no encontrada» (el Postgres lo
//     envuelve con el tenant): solo que no es cero, cuándo avanza, y errors.Is.
//
// Los instantes se comparan con Equal/After: Postgres guarda microsegundos y devuelve la zona de
// la sesión.
func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("intentcfghelpertest.Contrato: nuevo es nil; hace falta una función que devuelva un Montaje")
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
		{"Get_TenantWithoutConfig_ErrNotFound", caseNotFound},                    // sin config ⇒ ErrNotFound (errors.Is) y Config cero
		{"Upsert_NewTenant_CreatesTheConfig", caseCreates},                       // el alta: version, blob y marca
		{"Upsert_ExistingTenant_ReplacesVersionAndBlob", caseReplaces},           // sustituye entero, no mezcla
		{"Upsert_BlobComesBackAsEquivalentJSON", caseBlobRoundTrip},              // el blob vuelve equivalente como JSON
		{"Upsert_DifferentContent_AdvancesUpdatedAt", caseReplaceMovesTheMark},   // el reemplazo refresca la marca
		{"Upsert_SameContent_StillAdvancesUpdatedAt", caseSameContentMovesMark},  // la marca se refresca SIEMPRE
		{"Upsert_OneTenant_DoesNotTouchTheOther", caseTenantIsolation},           // aislamiento por tenant (INV-8)
		{"Upsert_DoesNotKeepTheCallersSlice", caseCallerSliceIsNotKept},          // el slice del llamante no se retiene
		{"Get_Twice_SameConfig_AndCallerMayMutateTheBlob", caseGetDoesNotChange}, // leer no cambia nada
	}
}

// validateMontaje exige lo que la suite da por hecho de un Montaje.
func validateMontaje(t *testing.T, m Montaje) {
	t.Helper()
	switch {
	case m.Store == nil:
		t.Fatal("Montaje.Store es nil")
	case m.Advance == nil:
		t.Fatal("Montaje.Advance es nil: la suite la necesita para afirmar sobre UpdatedAt")
	case m.TenantA == "" || m.TenantB == "":
		t.Fatalf("Montaje: TenantA (%q) y TenantB (%q) no pueden ser vacíos", m.TenantA, m.TenantB)
	case m.TenantA == m.TenantB:
		t.Fatalf("Montaje: TenantA y TenantB son el mismo (%q); deben ser distintos", m.TenantA)
	}
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		if _, err := m.Store.Get(context.Background(), tenant); !errors.Is(err, intentcfg.ErrNotFound) {
			t.Fatalf("Montaje: el tenant %q tiene que venir sin config; Get devolvió err=%v", tenant, err)
		}
	}
}

func caseNotFound(t *testing.T, m Montaje) {
	// El otro tenant SÍ tiene config: el «no encontrada» de A no es «la tabla está vacía».
	w := seedWitness(t, m)
	got, err := m.Store.Get(context.Background(), m.TenantA)
	if !errors.Is(err, intentcfg.ErrNotFound) {
		t.Fatalf("Get de un tenant sin config: err=%v, quería uno que cumpla errors.Is(ErrNotFound)", err)
	}
	if got.Version != "" || len(got.Blob) != 0 || !got.UpdatedAt.IsZero() {
		t.Errorf("Get de un tenant sin config devolvió %+v, quería un Config cero", got)
	}
	w.requireUntouched(t, m)
}

func caseCreates(t *testing.T, m Montaje) {
	blob := []byte(`{"version":"v1","intents":[{"name":"x"}]}`)
	upsert(t, m, m.TenantA, "hash-1", blob)
	got := get(t, m, m.TenantA)
	requireConfig(t, "tras el alta", got, "hash-1", blob)
	if got.UpdatedAt.IsZero() {
		t.Error("tras el alta, UpdatedAt es cero; quería el instante del Upsert")
	}
}

func caseReplaces(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	upsert(t, m, m.TenantA, "hash-1", []byte(`{"version":"v1","intents":[{"name":"x"}],"threshold":0.5}`))
	// El segundo blob NO trae "threshold": si la implementación mezclara en vez de sustituir,
	// la clave sobreviviría.
	second := []byte(`{"version":"v2","intents":[{"name":"y"}]}`)
	upsert(t, m, m.TenantA, "hash-2", second)
	requireConfig(t, "tras el reemplazo", get(t, m, m.TenantA), "hash-2", second)
	w.requireUntouched(t, m)
}

func caseBlobRoundTrip(t *testing.T, m Montaje) {
	blobs := map[string]string{
		"compact":             `{"version":"v1","intents":[]}`,
		"spaces_and_newlines": "{\n  \"version\" : \"v1\",\n\t\"intents\" : [ { \"name\" : \"x\" } ]\n}",
		"keys_out_of_order":   `{"zeta":1,"alpha":{"b":2,"a":1},"mid":[3,2,1]}`,
		"non_ascii_text":      `{"greeting":"¡Hola, ñandú! 你好","emoji":"👋","escaped":"café"}`,
		"scalars":             `{"t":true,"f":false,"n":null,"i":-7,"d":0.25,"s":""}`,
		"empty_object":        `{}`,
	}
	for name, blob := range blobs {
		t.Run(name, func(t *testing.T) {
			upsert(t, m, m.TenantA, "hash-"+name, []byte(blob))
			requireConfig(t, "tras guardar "+name, get(t, m, m.TenantA), "hash-"+name, []byte(blob))
		})
	}
}

func caseReplaceMovesTheMark(t *testing.T, m Montaje) {
	upsert(t, m, m.TenantA, "hash-1", []byte(`{"a":1}`))
	first := get(t, m, m.TenantA).UpdatedAt
	m.Advance(t)
	upsert(t, m, m.TenantA, "hash-2", []byte(`{"b":2}`))
	if second := get(t, m, m.TenantA).UpdatedAt; !second.After(first) {
		t.Errorf("tras reemplazar, UpdatedAt = %v; quería uno posterior al del alta (%v)", second, first)
	}
}

func caseSameContentMovesMark(t *testing.T, m Montaje) {
	blob := []byte(`{"a":1}`)
	upsert(t, m, m.TenantA, "hash-1", blob)
	first := get(t, m, m.TenantA).UpdatedAt
	m.Advance(t)
	upsert(t, m, m.TenantA, "hash-1", blob)
	got := get(t, m, m.TenantA)
	requireConfig(t, "tras reescribir lo mismo", got, "hash-1", blob)
	if !got.UpdatedAt.After(first) {
		t.Errorf("tras reescribir la misma config, UpdatedAt = %v; quería uno posterior al anterior (%v): la marca se refresca siempre",
			got.UpdatedAt, first)
	}
}

func caseTenantIsolation(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	// Misma version que el testigo y otro blob: lo único que los separa es el tenant.
	blob := []byte(`{"owner":"a"}`)
	upsert(t, m, m.TenantA, witnessVersion, blob)
	requireConfig(t, "el tenant A", get(t, m, m.TenantA), witnessVersion, blob)
	w.requireUntouched(t, m)
	m.Advance(t)
	upsert(t, m, m.TenantA, "hash-a2", []byte(`{"owner":"a","n":2}`))
	w.requireUntouched(t, m)
}

func caseCallerSliceIsNotKept(t *testing.T, m Montaje) {
	blob := []byte(`{"a":1}`)
	upsert(t, m, m.TenantA, "hash-1", blob)
	// El llamante reutiliza su slice: sigue siendo JSON válido, pero otro.
	copy(blob, `{"a":9}`)
	requireConfig(t, "tras modificar el slice del llamante", get(t, m, m.TenantA), "hash-1", []byte(`{"a":1}`))
}

func caseGetDoesNotChange(t *testing.T, m Montaje) {
	blob := []byte(`{"a":1}`)
	upsert(t, m, m.TenantA, "hash-1", blob)
	first := get(t, m, m.TenantA)
	// El reloj pasa y el llamante pisa el blob que recibió: nada de eso llega a lo guardado.
	m.Advance(t)
	for i := range first.Blob {
		first.Blob[i] = 'X'
	}
	second := get(t, m, m.TenantA)
	requireConfig(t, "en la segunda lectura", second, "hash-1", blob)
	if !second.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("leer movió UpdatedAt: %v, quería %v", second.UpdatedAt, first.UpdatedAt)
	}
}

// witnessVersion es la version de la config testigo.
const witnessVersion = "hash-witness"

// witness es la marca de estado de TenantB: su Config entero tal como quedó al sembrarlo.
type witness struct{ before intentcfg.Config }

// seedWitness da una config a TenantB, deja pasar el reloj y apunta su marca de estado.
func seedWitness(t *testing.T, m Montaje) witness {
	t.Helper()
	upsert(t, m, m.TenantB, witnessVersion, []byte(`{"owner":"b","intents":[{"name":"witness"}]}`))
	w := witness{before: get(t, m, m.TenantB)}
	// Lo que el caso escriba después lleva una marca posterior: un Upsert que tocara al testigo
	// se vería también en su UpdatedAt.
	m.Advance(t)
	return w
}

// requireUntouched comprueba que la config de TenantB sigue EXACTAMENTE como se sembró: version,
// blob y UpdatedAt.
func (w witness) requireUntouched(t *testing.T, m Montaje) {
	t.Helper()
	got := get(t, m, m.TenantB)
	requireConfig(t, "el testigo (tenant B)", got, w.before.Version, w.before.Blob)
	if !got.UpdatedAt.Equal(w.before.UpdatedAt) {
		t.Errorf("el testigo (tenant B) cambió de UpdatedAt: %v, quería %v", got.UpdatedAt, w.before.UpdatedAt)
	}
}

// upsert guarda la config del tenant y corta el caso si falla.
func upsert(t *testing.T, m Montaje, tenant, version string, blob []byte) {
	t.Helper()
	if err := m.Store.Upsert(context.Background(), tenant, version, blob); err != nil {
		t.Fatalf("Upsert(%q, %q): error inesperado %v", tenant, version, err)
	}
}

// get lee la config del tenant y corta el caso si falla.
func get(t *testing.T, m Montaje, tenant string) intentcfg.Config {
	t.Helper()
	got, err := m.Store.Get(context.Background(), tenant)
	if err != nil {
		t.Fatalf("Get(%q): error inesperado %v", tenant, err)
	}
	return got
}

// requireConfig comprueba la version y, por equivalencia JSON, el blob.
func requireConfig(t *testing.T, what string, got intentcfg.Config, version string, blob []byte) {
	t.Helper()
	if got.Version != version {
		t.Errorf("%s: Version = %q, quería %q", what, got.Version, version)
	}
	if !jsonEqual(t, got.Blob, blob) {
		t.Errorf("%s: Blob = %s, quería uno equivalente a %s", what, got.Blob, blob)
	}
}

// jsonEqual compara dos blobs por CONTENIDO JSON, no por bytes: la columna config es JSONB y
// Postgres canonicaliza (reordena claves, normaliza espacios), así que el texto leído no es
// byte-idéntico al escrito. Un blob que no es JSON corta el caso.
func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("JSON inválido en la comparación (%s): %v", a, err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("JSON inválido en la comparación (%s): %v", b, err)
	}
	return reflect.DeepEqual(va, vb)
}
