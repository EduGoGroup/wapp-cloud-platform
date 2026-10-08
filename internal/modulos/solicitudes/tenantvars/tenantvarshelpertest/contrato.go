// Package tenantvarshelpertest es la suite de contrato del puerto tenantvars.Store. Ningún código
// de producción lo importa: arrastra "testing".
//
// La suite la corren las dos implementaciones del puerto: tenantvars.MemoryStore en unitario
// (memory_test.go) y tenantvars.Postgres en los procesos de F9, con el arnés de testcontainers
// (test/procesos/tenantvars_contrato_test.go).
//
// Nuevo: no tiene fichero viejo. Los casos salen del contrato de tenantvars.Store y de los tests
// de integración viejos; casos leídos de internal/tenantvars/postgres_integration_test.go, no
// portados. De sus cinco tests, cuatro son del puerto (Roundtrip_Verbatim,
// Replace_ReemplazaElConjunto, Aislamiento_PorTenant, UpdatedAt_SoloSeMueveAlCambiar); el quinto,
// TestMigracion_DobleAplicacion_NoRompeNiBorra, es del runner de migraciones y no entra aquí.
package tenantvarshelpertest

import (
	"context"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/tenantvars"
)

// Montaje es lo que cada implementación entrega a la suite para UN caso: Contrato llama a nuevo
// una vez por caso.
type Montaje struct {
	// Store es la implementación bajo prueba.
	Store tenantvars.Store
	// TenantA y TenantB son dos tenant_id distintos, no vacíos y SIN variables. tenant_id es
	// TEXT sin clave foránea: no hay fila de tenants que sembrar, basta con que sean únicos por
	// caso.
	TenantA, TenantB string
	// Advance deja pasar el reloj con el que la implementación marca UpdatedAt. Promete que un
	// Replace posterior a la llamada marca con un instante ESTRICTAMENTE posterior al de
	// cualquier Replace anterior a ella. Es obligatoria.
	//
	// Por qué existe: la promesa «UpdatedAt solo se mueve si el valor cambia» tiene dos mitades.
	// «No se mueve» se afirma con Equal y vale con cualquier reloj; «se mueve» exige dos marcas
	// distintas, y eso depende del reloj de cada implementación. El test viejo
	// (TestPostgres_UpdatedAt_SoloSeMueveAlCambiar) confiaba en que dos transacciones seguidas
	// caen en microsegundos distintos; casi siempre es verdad, pero no lo garantiza nadie, y en
	// memoria un reloj fijo lo rompe. Con Advance la suite no duerme ni adivina: en memoria
	// adelanta el reloj inyectado con SetClock; contra Postgres espera, preguntándoselo a la
	// base, a que su reloj pase del microsegundo en que estaba.
	//
	// De paso da filo a la otra mitad: la suite llama a Advance antes de reescribir un valor
	// igual, y una implementación que remarque de más queda con una marca distinta, a la vista.
	Advance func(t *testing.T)
}

// Contrato ejecuta las promesas de tenantvars.Store contra la implementación que devuelve nuevo,
// con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta nada.
//
// El puerto deja escribir (Replace) y observar (List) todo lo que la suite necesita: el Montaje
// no trae ni siembra ni observador. La «marca de estado» de un tenant es su List entero —clave,
// valor y UpdatedAt de cada variable, que son las tres columnas que Replace puede tocar—, y cada
// caso que escribe en TenantA siembra antes un testigo en TenantB y comprueba al final que su
// marca de estado no se movió.
//
// Lo que la suite NO afirma, a propósito:
//
//   - el orden de claves que no sean letras minúsculas ASCII: MemoryStore ordena byte a byte y
//     Postgres con la colación de la base, que pueden divergir con mayúsculas, signos o
//     acentos. Las claves de la suite son solo minúsculas ASCII;
//   - el valor concreto de UpdatedAt: en memoria es el reloj inyectado y en Postgres el now()
//     de la transacción. Solo se afirma que no es cero, cuándo se conserva y cuándo avanza;
//   - nada sobre la forma de las claves o los valores (longitud, clave vacía): los límites son
//     del transporte, no del puerto.
//
// Los instantes se comparan con Equal: Postgres guarda microsegundos y devuelve la zona de la
// sesión.
func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("tenantvarshelpertest.Contrato: nuevo es nil; hace falta una función que devuelva un Montaje")
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
		{"List_TenantWithoutVariables_EmptyNotNil", caseEmptyList},              // vacío no es nil ni error
		{"List_SortedByKey", caseSortedByKey},                                   // ordenado por clave
		{"Replace_NewVariable_CarriesAChangeMark", caseNewVariableIsMarked},     // el alta lleva UpdatedAt
		{"Replace_IsTotal_AbsentKeysDisappear", caseTotalReplacement},           // reemplazo TOTAL
		{"Replace_EmptyMap_LeavesNoVariables", caseEmptyMapClears},              // vaciado con el mapa vacío
		{"Replace_NilMap_LeavesNoVariables", caseNilMapClears},                  // nil vacía igual que el mapa vacío
		{"Replace_ValuesVerbatim", caseVerbatim},                                // nadie interpreta el valor
		{"Replace_OneTenant_DoesNotTouchTheOther", caseTenantIsolation},         // aislamiento por tenant (INV-8)
		{"Replace_SameSetTwice_LeavesTheSame", caseIdempotent},                  // idempotente
		{"Replace_SameValue_KeepsUpdatedAt", caseSameValueKeepsTheMark},         // mismo valor ⇒ la marca no se mueve
		{"Replace_DifferentValue_AdvancesUpdatedAt", caseNewValueMovesTheMark},  // valor distinto ⇒ la marca avanza
		{"Replace_ClearedAndWrittenAgain_StartsOver", caseClearedThenRewritten}, // tras vaciar, el alta es un alta
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
		if got := list(t, m, tenant); len(got) != 0 {
			t.Fatalf("Montaje: el tenant %q trae %d variables; tiene que venir sin ninguna", tenant, len(got))
		}
	}
}

func caseEmptyList(t *testing.T, m Montaje) {
	// El otro tenant SÍ tiene variables: el vacío de A no es «la tabla está vacía».
	w := seedWitness(t, m)
	got, err := m.Store.List(context.Background(), m.TenantA)
	if err != nil {
		t.Fatalf("List de un tenant sin variables: error inesperado %v", err)
	}
	if got == nil {
		t.Error("List de un tenant sin variables = nil, quería un slice vacío no nil")
	}
	if len(got) != 0 {
		t.Errorf("List de un tenant sin variables: %d variables, quería 0: %+v", len(got), got)
	}
	w.requireUntouched(t, m)
}

func caseSortedByKey(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	replace(t, m, m.TenantA, map[string]string{
		"kilogram": "5", "delta": "2", "alpha": "1", "kilo": "4", "golf": "3",
	})
	got := list(t, m, m.TenantA)
	keys := make([]string, 0, len(got))
	for _, v := range got {
		keys = append(keys, v.Key)
	}
	if want := []string{"alpha", "delta", "golf", "kilo", "kilogram"}; !slices.Equal(keys, want) {
		t.Errorf("claves de List = %v, quería %v (ordenadas por clave)", keys, want)
	}
	w.requireUntouched(t, m)
}

func caseNewVariableIsMarked(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	replace(t, m, m.TenantA, map[string]string{"currency": "Bs", "shipping": "free"})
	for _, v := range list(t, m, m.TenantA) {
		if v.UpdatedAt.IsZero() {
			t.Errorf("la variable %q recién guardada trae UpdatedAt cero", v.Key)
		}
	}
	w.requireUntouched(t, m)
}

func caseTotalReplacement(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	replace(t, m, m.TenantA, map[string]string{"currency": "Bs", "shipping": "free", "phone": "support"})
	// Un cambio (currency), un alta (fresh) y dos ausentes (shipping, phone).
	want := map[string]string{"currency": "USD", "fresh": "sí"}
	replace(t, m, m.TenantA, want)
	requireVars(t, m, m.TenantA, want)
	w.requireUntouched(t, m)
}

func caseEmptyMapClears(t *testing.T, m Montaje) {
	requireClears(t, m, map[string]string{})
}

// caseNilMapClears fija lo que hacen las dos implementaciones viejas con un mapa nil: lo mismo
// que con uno vacío. En Postgres es donde muerde el array NO nil del DELETE.
func caseNilMapClears(t *testing.T, m Montaje) {
	requireClears(t, m, nil)
}

// requireClears afirma que Replace con empty (un mapa vacío o nil) deja a TenantA sin variables
// y con un List vacío no nil, sin tocar a TenantB.
func requireClears(t *testing.T, m Montaje, empty map[string]string) {
	t.Helper()
	w := seedWitness(t, m)
	replace(t, m, m.TenantA, map[string]string{"currency": "Bs", "shipping": "free"})
	replace(t, m, m.TenantA, empty)
	got := list(t, m, m.TenantA)
	if len(got) != 0 {
		t.Errorf("tras el vaciado quedan %d variables, quería 0: %+v", len(got), got)
	}
	if got == nil {
		t.Error("List tras el vaciado = nil, quería un slice vacío no nil")
	}
	w.requireUntouched(t, m)
}

func caseVerbatim(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	want := map[string]string{
		"currency": "Bs",
		"greeting": "¡Hola! ¿Qué tal? — Panadería Ñandú",
		"notice":   "   dos espacios a cada lado   ",
		"payload":  `{"a":1,"b":["x","y"],"c":null}`,
		"empty":    "",
		"lines":    "primera\n\tsegunda",
	}
	replace(t, m, m.TenantA, want)
	requireVars(t, m, m.TenantA, want)
	w.requireUntouched(t, m)
}

// caseTenantIsolation escribe, cambia y vacía A con las MISMAS claves que tiene B: ni los
// valores ni las marcas de B se mueven, y A no ve nada de B.
func caseTenantIsolation(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	if got := list(t, m, m.TenantA); len(got) != 0 {
		t.Fatalf("el tenant A ve %d variables ajenas: %+v", len(got), got)
	}
	want := map[string]string{"currency": "Bs", "only": "de-a"}
	replace(t, m, m.TenantA, want)
	requireVars(t, m, m.TenantA, want)
	w.requireUntouched(t, m)

	replace(t, m, m.TenantA, map[string]string{"currency": "USD"})
	w.requireUntouched(t, m)

	replace(t, m, m.TenantA, map[string]string{})
	w.requireUntouched(t, m)
	if got := list(t, m, m.TenantA); len(got) != 0 {
		t.Errorf("tras vaciar A quedan %d variables suyas: %+v", len(got), got)
	}
}

func caseIdempotent(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	vars := map[string]string{"currency": "Bs", "shipping": "free", "empty": ""}
	replace(t, m, m.TenantA, vars)
	before := list(t, m, m.TenantA)
	m.Advance(t)
	replace(t, m, m.TenantA, vars)
	requireVars(t, m, m.TenantA, vars)
	requireSameRows(t, "el segundo Replace idéntico", list(t, m, m.TenantA), before)
	w.requireUntouched(t, m)
}

func caseSameValueKeepsTheMark(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	replace(t, m, m.TenantA, map[string]string{"currency": "Bs", "shipping": "free"})
	before := mark(t, list(t, m, m.TenantA), "shipping")
	m.Advance(t)
	// Mismo valor para shipping, aunque el resto del conjunto cambie.
	replace(t, m, m.TenantA, map[string]string{"currency": "USD", "shipping": "free", "fresh": "x"})
	after := mark(t, list(t, m, m.TenantA), "shipping")
	if !after.Equal(before) {
		t.Errorf("shipping no cambió de valor y su UpdatedAt pasó de %v a %v", before, after)
	}
	w.requireUntouched(t, m)
}

func caseNewValueMovesTheMark(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	replace(t, m, m.TenantA, map[string]string{"currency": "Bs", "shipping": "free"})
	before := mark(t, list(t, m, m.TenantA), "currency")
	m.Advance(t)
	replace(t, m, m.TenantA, map[string]string{"currency": "USD", "shipping": "free"})
	rows := list(t, m, m.TenantA)
	after := mark(t, rows, "currency")
	if !after.After(before) {
		t.Errorf("currency cambió de valor y su UpdatedAt no avanzó: %v → %v", before, after)
	}
	for _, v := range rows {
		if v.Key == "currency" && v.Value != "USD" {
			t.Errorf("currency = %q, quería USD", v.Value)
		}
	}
	w.requireUntouched(t, m)
}

// caseClearedThenRewritten: una variable borrada no deja rastro. Si vuelve con el mismo valor
// es un alta, con su marca nueva, no «el mismo valor de antes».
func caseClearedThenRewritten(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	vars := map[string]string{"currency": "Bs"}
	replace(t, m, m.TenantA, vars)
	before := mark(t, list(t, m, m.TenantA), "currency")
	replace(t, m, m.TenantA, map[string]string{})
	m.Advance(t)
	replace(t, m, m.TenantA, vars)
	requireVars(t, m, m.TenantA, vars)
	if after := mark(t, list(t, m, m.TenantA), "currency"); !after.After(before) {
		t.Errorf("currency se borró y volvió: su UpdatedAt debía ser posterior (%v → %v)", before, after)
	}
	w.requireUntouched(t, m)
}

// witness es la marca de estado del tenant que un caso NO toca: sus filas tal como quedaron al
// sembrarlo.
type witness struct {
	rows []tenantvars.Variable
}

// seedWitness siembra en TenantB un conjunto que comparte una clave (currency) con los que los
// casos escriben en TenantA, guarda su marca de estado y deja pasar el reloj: si una escritura
// en A rozara a B, su valor o su UpdatedAt saldrían distintos.
func seedWitness(t *testing.T, m Montaje) witness {
	t.Helper()
	replace(t, m, m.TenantB, map[string]string{"currency": "witness", "untouched": "de-b"})
	w := witness{rows: list(t, m, m.TenantB)}
	if len(w.rows) != 2 {
		t.Fatalf("el testigo de TenantB quedó con %d variables, quería 2: %+v", len(w.rows), w.rows)
	}
	m.Advance(t)
	return w
}

// requireUntouched afirma que TenantB sigue exactamente como se sembró: claves, valores y marcas.
func (w witness) requireUntouched(t *testing.T, m Montaje) {
	t.Helper()
	requireSameRows(t, "el otro tenant (TenantB)", list(t, m, m.TenantB), w.rows)
}

// replace reemplaza el conjunto del tenant o falla el test.
func replace(t *testing.T, m Montaje, tenant string, vars map[string]string) {
	t.Helper()
	if err := m.Store.Replace(context.Background(), tenant, vars); err != nil {
		t.Fatalf("Replace(%q, %v): error inesperado %v", tenant, vars, err)
	}
}

// list lee las variables del tenant o falla el test.
func list(t *testing.T, m Montaje, tenant string) []tenantvars.Variable {
	t.Helper()
	got, err := m.Store.List(context.Background(), tenant)
	if err != nil {
		t.Fatalf("List(%q): error inesperado %v", tenant, err)
	}
	return got
}

// requireVars afirma que el tenant tiene exactamente want: las mismas claves, en orden, cada una
// con su valor byte a byte.
func requireVars(t *testing.T, m Montaje, tenant string, want map[string]string) {
	t.Helper()
	got := list(t, m, tenant)
	gotVars := make(map[string]string, len(got))
	keys := make([]string, 0, len(got))
	for _, v := range got {
		gotVars[v.Key] = v.Value
		keys = append(keys, v.Key)
	}
	if !maps.Equal(gotVars, want) || len(got) != len(want) {
		t.Fatalf("variables de %q = %#v, quería %#v", tenant, gotVars, want)
	}
	if wantKeys := slices.Sorted(maps.Keys(want)); !slices.Equal(keys, wantKeys) {
		t.Errorf("claves de %q = %v, quería %v (ordenadas por clave)", tenant, keys, wantKeys)
	}
}

// requireSameRows afirma que got es la misma marca de estado que want: mismas variables, en el
// mismo orden, con el mismo valor y el mismo UpdatedAt.
func requireSameRows(t *testing.T, what string, got, want []tenantvars.Variable) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d variables, quería %d: %+v", what, len(got), len(want), got)
	}
	for i := range want {
		if got[i].Key != want[i].Key || got[i].Value != want[i].Value {
			t.Errorf("%s: variable %d = (%q, %q), quería (%q, %q)",
				what, i, got[i].Key, got[i].Value, want[i].Key, want[i].Value)
		}
		if !got[i].UpdatedAt.Equal(want[i].UpdatedAt) {
			t.Errorf("%s: el UpdatedAt de %q pasó de %v a %v y no debía moverse",
				what, want[i].Key, want[i].UpdatedAt, got[i].UpdatedAt)
		}
	}
}

// mark devuelve el UpdatedAt de la variable key, o falla el test si no está.
func mark(t *testing.T, rows []tenantvars.Variable, key string) (at time.Time) {
	t.Helper()
	for _, v := range rows {
		if v.Key == key {
			return v.UpdatedAt
		}
	}
	t.Fatalf("no está la variable %q en %+v", key, rows)
	return at
}
