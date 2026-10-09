// Package casebankhelpertest trae la suite de contrato del puerto casebank.Store
// (Contrato) y su doble en memoria (Memory). Ningún código de producción lo
// importa: arrastra "testing".
//
// La suite la corren las dos implementaciones del puerto: Memory en unitario
// (memory_test.go) y casebank.Postgres en los procesos de F9, con el arnés de
// testcontainers.
//
// Nuevo: no tiene fichero viejo. Los casos salen del contrato de casebank.Store y
// de los tests de integración viejos; casos leídos de
// internal/casebank/postgres_integration_test.go, no portados. De sus cinco
// tests, tres son del puerto (TestElCheckDelConsentimientoEsLaRedDeAbajo —aquí
// por el store y no con un INSERT crudo—, TestElCheckAceptaLaFilaConsentida y
// TestExiste_DistingueTenantYTexto); TestElDefaultDeConsentedRECHAZAALDescuidado
// es del esquema (un INSERT que ni menciona la columna: el puerto no puede
// emitirlo) y TestSembrarElCasoAmbar_EsIdempotenteContraPostgres es del servicio
// (seed_test.go lo afirma con el doble).
package casebankhelpertest

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/casebank"
)

// consentedCheck es el nombre de la constraint de la 0082. El rechazo de un caso
// sin consentimiento lo lleva en el texto del error en las dos implementaciones.
const consentedCheck = "intake_case_bank_consented_check"

// Montaje es lo que cada implementación entrega a la suite para UN caso: Contrato
// llama a montar una vez por caso.
type Montaje struct {
	// Store es la implementación bajo prueba.
	Store casebank.Store
	// TenantA y TenantB son dos tenant_id distintos, no vacíos y SIN filas en el
	// banco. tenant_id es TEXT sin clave foránea: no hay fila de tenants que
	// sembrar, basta con que sean únicos por caso.
	TenantA, TenantB string
	// Rows es el observador: las filas de ese tenant, en orden de id, con las
	// cuatro columnas que Insert escribe (Row.Expected nil = NULL de SQL). El
	// puerto no tiene lectura —nadie de producción lee esta tabla—, así que sin él
	// la suite no podría afirmar QUÉ se escribió. Es obligatorio. En memoria es
	// Memory.Rows; contra Postgres, un SELECT … WHERE tenant_id = $1 ORDER BY id.
	Rows func(t *testing.T, tenantID string) []Row
}

// Contrato ejecuta las promesas de casebank.Store contra la implementación que
// devuelve montar, con un Montaje limpio por caso (montar se llama una vez por
// t.Run). No salta nada. Cada uno de los dos métodos del puerto tiene sus casos.
//
// La «marca de estado» de un tenant es su Rows entero —id, tenant, consented,
// source_text y expected de cada fila, que es todo lo que Insert puede tocar—, y
// los casos que escriben o consultan en TenantA siembran antes un testigo en
// TenantB y comprueban al final que su marca no se movió.
//
// Lo que la suite NO afirma, a propósito:
//
//   - el valor concreto de los ids ni que sean consecutivos: solo que no son cero
//     y que no se repiten;
//   - los bytes de expected: Postgres guarda JSONB y lo devuelve normalizado
//     (espacios, orden de claves). Se compara el DOCUMENTO, no su texto;
//   - el centinela casebank.ErrNoConsent: es del servicio. El store rechaza el
//     caso sin consentimiento con el error de la constraint;
//   - nada sobre tenant o texto vacíos: validar eso es del servicio, y el store
//     escribe lo que le dan.
func Contrato(t *testing.T, montar func(t *testing.T) Montaje) {
	t.Helper()
	if montar == nil {
		t.Fatal("casebankhelpertest.Contrato: montar es nil; hace falta una función que devuelva un Montaje")
	}
	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			m := montar(t)
			validateMontaje(t, m)
			c.run(t, m)
		})
	}
}

// contractCase es una promesa del puerto: su nombre (el del t.Run) y la función
// que la afirma.
type contractCase struct {
	name string
	run  func(t *testing.T, m Montaje)
}

// cases es la tabla de la suite. El comentario de cada fila es la promesa que fija.
func cases() []contractCase {
	return []contractCase{
		{"Insert_ConsentedCase_ReturnsIDAndWritesTheRow", caseInsertWritesTheRow},    // id ≠ 0 y las cuatro columnas
		{"Insert_WithoutExpected_StoresSQLNull", caseEmptyExpectedIsNull},            // vacío ⇒ NULL, no JSON
		{"Insert_SourceTextVerbatim_DoesNotAnonymize", caseSourceTextVerbatim},       // el store no anonimiza ni recorta
		{"Insert_NotConsented_RejectedAndWritesNothing", caseNotConsentedRejected},   // el CHECK consented
		{"Insert_InvalidExpectedJSON_RejectedAndWritesNothing", caseInvalidExpected}, // la columna es JSONB
		{"Insert_SameCaseTwice_TwoRowsWithDistinctIDs", caseNoDeduplication},         // no deduplica; ids únicos
		{"Insert_OneTenant_DoesNotTouchTheOther", caseTenantIsolation},               // aislamiento por tenant
		{"Exists_EmptyBank_False", caseExistsOnEmptyBank},                            // sin filas, false y sin error
		{"Exists_SameTenantSameText_True", caseExistsAfterInsert},                    // la guarda de la siembra
		{"Exists_OnlyTheExactLiteral", caseExistsExactLiteral},                       // comparación byte a byte
		{"Exists_OtherTenant_False", caseExistsOtherTenant},                          // el texto de otro tenant no cuenta
		{"Exists_DoesNotWrite", caseExistsDoesNotWrite},                              // lectura pura
	}
}

// validateMontaje exige lo que la suite da por hecho de un Montaje.
func validateMontaje(t *testing.T, m Montaje) {
	t.Helper()
	switch {
	case m.Store == nil:
		t.Fatal("Montaje.Store es nil")
	case m.Rows == nil:
		t.Fatal("Montaje.Rows es nil: la suite lo necesita para afirmar qué se escribió")
	case m.TenantA == "" || m.TenantB == "":
		t.Fatalf("Montaje: TenantA (%q) y TenantB (%q) no pueden ser vacíos", m.TenantA, m.TenantB)
	case m.TenantA == m.TenantB:
		t.Fatalf("Montaje: TenantA y TenantB son el mismo (%q)", m.TenantA)
	}
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		if rows := m.Rows(t, tenant); len(rows) != 0 {
			t.Fatalf("Montaje: el tenant %q ya tiene %d filas; la suite necesita un banco limpio", tenant, len(rows))
		}
	}
}

const (
	// plainText es un literal sin nada identificable.
	plainText = "quiero una torta de 10 porciones"
	// witnessText es el del testigo de TenantB.
	witnessText = "testigo: dos tortas y un paquete de 30"
	// curatedJSON es una interpretación curada, con espacios y un no-ASCII.
	curatedJSON = `{"version": 1, "items": [{"product": "tequeños", "qty": 2}]}`
)

// mustInsert inserta y exige que no falle.
func mustInsert(t *testing.T, m Montaje, c casebank.Case) int64 {
	t.Helper()
	id, err := m.Store.Insert(context.Background(), c)
	if err != nil {
		t.Fatalf("Insert(%+v) = error %v; se esperaba que se guardara", c, err)
	}
	return id
}

// mustExist consulta y exige que no falle.
func mustExist(t *testing.T, m Montaje, tenantID, text string) bool {
	t.Helper()
	found, err := m.Store.Exists(context.Background(), tenantID, text)
	if err != nil {
		t.Fatalf("Exists(%q, %q) = error %v", tenantID, text, err)
	}
	return found
}

// seedWitness deja una fila en TenantB y devuelve su marca de estado.
func seedWitness(t *testing.T, m Montaje) []Row {
	t.Helper()
	mustInsert(t, m, casebank.Case{
		TenantID: m.TenantB, Consented: true, SourceText: witnessText, Expected: json.RawMessage(curatedJSON),
	})
	mark := m.Rows(t, m.TenantB)
	if len(mark) != 1 {
		t.Fatalf("el testigo de TenantB dejó %d filas, se esperaba 1", len(mark))
	}
	return mark
}

// requireUntouched exige que la marca de estado del tenant no se haya movido.
func requireUntouched(t *testing.T, m Montaje, tenantID string, before []Row) {
	t.Helper()
	after := m.Rows(t, tenantID)
	if len(after) != len(before) {
		t.Fatalf("el tenant %q tenía %d filas y ahora tiene %d", tenantID, len(before), len(after))
	}
	for i := range before {
		if !sameRow(before[i], after[i]) {
			t.Errorf("la fila %d del tenant %q cambió:\n antes: %+v\nahora: %+v", i, tenantID, before[i], after[i])
		}
	}
}

// sameRow compara dos filas columna a columna (expected, como documento).
func sameRow(a, b Row) bool {
	return a.ID == b.ID && a.TenantID == b.TenantID && a.Consented == b.Consented &&
		a.SourceText == b.SourceText && sameJSON(a.Expected, b.Expected)
}

// sameJSON dice si dos expected son el mismo documento. nil (NULL) solo es igual a
// nil: el literal JSON `null` es otro valor.
func sameJSON(a, b json.RawMessage) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	var da, db any
	if json.Unmarshal(a, &da) != nil || json.Unmarshal(b, &db) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(da, db)
}

// requireRow exige que el tenant tenga exactamente una fila y que sea la esperada.
func requireRow(t *testing.T, m Montaje, tenantID string, want Row) {
	t.Helper()
	rows := m.Rows(t, tenantID)
	if len(rows) != 1 {
		t.Fatalf("el tenant %q tiene %d filas, se esperaba 1: %+v", tenantID, len(rows), rows)
	}
	if !sameRow(rows[0], want) {
		t.Errorf("la fila guardada no es la esperada:\n   got: %+v (expected=%s)\n want: %+v (expected=%s)",
			rows[0], rows[0].Expected, want, want.Expected)
	}
}

func caseInsertWritesTheRow(t *testing.T, m Montaje) {
	witness := seedWitness(t, m)
	id := mustInsert(t, m, casebank.Case{
		TenantID: m.TenantA, Consented: true, SourceText: plainText, Expected: json.RawMessage(curatedJSON),
	})
	if id == 0 {
		t.Error("Insert devolvió id 0: el id de la fila no está llegando")
	}
	requireRow(t, m, m.TenantA, Row{
		ID: id, TenantID: m.TenantA, Consented: true, SourceText: plainText, Expected: json.RawMessage(curatedJSON),
	})
	requireUntouched(t, m, m.TenantB, witness)
}

func caseEmptyExpectedIsNull(t *testing.T, m Montaje) {
	// Las dos formas de «vacío»: nil y un slice de longitud cero.
	for i, empty := range []json.RawMessage{nil, {}} {
		id := mustInsert(t, m, casebank.Case{TenantID: m.TenantA, Consented: true, SourceText: plainText, Expected: empty})
		rows := m.Rows(t, m.TenantA)
		if len(rows) != i+1 {
			t.Fatalf("tras %d inserciones hay %d filas", i+1, len(rows))
		}
		if got := rows[i]; got.ID != id || got.Expected != nil {
			t.Errorf("expected vacío (forma %d) quedó como %q en la fila %d (id %d); un caso sin curar es NULL de SQL",
				i, got.Expected, got.ID, id)
		}
	}
}

func caseSourceTextVerbatim(t *testing.T, m Montaje) {
	// Lo que un store que «ayudara» estropearía: PII (anonimizar es del servicio),
	// espacios en los extremos, saltos de línea, no-ASCII y comillas.
	const raw = "  Ambar escribió al +58 412 123 4567\n\tdesde 584121234567@s.whatsapp.net — 'ñ' \"x\" \\ %s $1  "
	id := mustInsert(t, m, casebank.Case{TenantID: m.TenantA, Consented: true, SourceText: raw})
	requireRow(t, m, m.TenantA, Row{ID: id, TenantID: m.TenantA, Consented: true, SourceText: raw})
	if !mustExist(t, m, m.TenantA, raw) {
		t.Error("Exists no encuentra el literal que se acaba de insertar tal cual")
	}
}

func caseNotConsentedRejected(t *testing.T, m Montaje) {
	witness := seedWitness(t, m)
	id, err := m.Store.Insert(context.Background(), casebank.Case{
		TenantID: m.TenantA, Consented: false, SourceText: plainText,
	})
	if err == nil {
		t.Fatalf("Insert aceptó un caso SIN consentimiento (id %d): el CHECK consented no está vigilando", id)
	}
	if id != 0 {
		t.Errorf("Insert rechazado devolvió id %d, se esperaba 0", id)
	}
	if !strings.Contains(err.Error(), consentedCheck) {
		t.Errorf("el rechazo fue %q; se esperaba que nombrara la constraint %s", err, consentedCheck)
	}
	if rows := m.Rows(t, m.TenantA); len(rows) != 0 {
		t.Errorf("el caso rechazado dejó %d filas: %+v", len(rows), rows)
	}
	if mustExist(t, m, m.TenantA, plainText) {
		t.Error("Exists dice que el caso rechazado está en el banco")
	}
	requireUntouched(t, m, m.TenantB, witness)

	// El rechazo no deja al store inservible: el mismo caso, consentido, entra.
	if id := mustInsert(t, m, casebank.Case{TenantID: m.TenantA, Consented: true, SourceText: plainText}); id == 0 {
		t.Error("tras un rechazo, el caso consentido devolvió id 0")
	}
}

func caseInvalidExpected(t *testing.T, m Montaje) {
	id, err := m.Store.Insert(context.Background(), casebank.Case{
		TenantID: m.TenantA, Consented: true, SourceText: plainText, Expected: json.RawMessage(`{"version": `),
	})
	if err == nil {
		t.Fatalf("Insert aceptó un expected que no es JSON (id %d)", id)
	}
	if id != 0 {
		t.Errorf("Insert rechazado devolvió id %d, se esperaba 0", id)
	}
	if rows := m.Rows(t, m.TenantA); len(rows) != 0 {
		t.Errorf("el caso rechazado dejó %d filas: %+v", len(rows), rows)
	}
}

func caseNoDeduplication(t *testing.T, m Montaje) {
	c := casebank.Case{TenantID: m.TenantA, Consented: true, SourceText: plainText}
	first := mustInsert(t, m, c)
	second := mustInsert(t, m, c)
	if first == 0 || second == 0 || first == second {
		t.Errorf("los ids de dos inserciones fueron %d y %d; se esperaban distintos y no cero", first, second)
	}
	rows := m.Rows(t, m.TenantA)
	if len(rows) != 2 {
		t.Fatalf("el mismo caso insertado dos veces dejó %d filas, se esperaban 2: el store no deduplica", len(rows))
	}
	if rows[0].ID != first || rows[1].ID != second {
		t.Errorf("las filas llevan los ids %d y %d; Insert devolvió %d y %d", rows[0].ID, rows[1].ID, first, second)
	}
}

func caseTenantIsolation(t *testing.T, m Montaje) {
	witness := seedWitness(t, m)
	// El MISMO literal que el testigo, en el otro tenant.
	id := mustInsert(t, m, casebank.Case{TenantID: m.TenantA, Consented: true, SourceText: witnessText})
	if id == witness[0].ID {
		t.Errorf("la fila de TenantA lleva el id %d, que es el del testigo de TenantB", id)
	}
	requireRow(t, m, m.TenantA, Row{ID: id, TenantID: m.TenantA, Consented: true, SourceText: witnessText})
	requireUntouched(t, m, m.TenantB, witness)
}

func caseExistsOnEmptyBank(t *testing.T, m Montaje) {
	if mustExist(t, m, m.TenantA, plainText) {
		t.Error("Exists dijo true sobre un banco vacío")
	}
	if mustExist(t, m, m.TenantA, "") {
		t.Error("Exists dijo true para el literal vacío sobre un banco vacío")
	}
}

func caseExistsAfterInsert(t *testing.T, m Montaje) {
	seedWitness(t, m)
	if mustExist(t, m, m.TenantA, plainText) {
		t.Fatal("Exists dijo true antes de insertar")
	}
	mustInsert(t, m, casebank.Case{TenantID: m.TenantA, Consented: true, SourceText: plainText})
	if !mustExist(t, m, m.TenantA, plainText) {
		t.Error("Exists dijo false para el tenant y el literal recién insertados")
	}
}

func caseExistsExactLiteral(t *testing.T, m Montaje) {
	mustInsert(t, m, casebank.Case{TenantID: m.TenantA, Consented: true, SourceText: plainText})
	for name, other := range map[string]string{
		"longer":         plainText + " y flan",
		"prefix":         plainText[:len(plainText)-1],
		"upper case":     strings.ToUpper(plainText),
		"trailing space": plainText + " ",
		"leading space":  " " + plainText,
		"empty":          "",
	} {
		if mustExist(t, m, m.TenantA, other) {
			t.Errorf("Exists dijo true para un literal distinto (%s): %q", name, other)
		}
	}
}

func caseExistsOtherTenant(t *testing.T, m Montaje) {
	seedWitness(t, m)
	if mustExist(t, m, m.TenantA, witnessText) {
		t.Error("Exists dijo true en TenantA para un literal que solo tiene TenantB")
	}
	if !mustExist(t, m, m.TenantB, witnessText) {
		t.Error("Exists dijo false en TenantB para su propio literal")
	}
}

func caseExistsDoesNotWrite(t *testing.T, m Montaje) {
	witness := seedWitness(t, m)
	mustInsert(t, m, casebank.Case{TenantID: m.TenantA, Consented: true, SourceText: plainText})
	before := m.Rows(t, m.TenantA)
	for _, text := range []string{plainText, "otro literal", witnessText} {
		mustExist(t, m, m.TenantA, text)
	}
	requireUntouched(t, m, m.TenantA, before)
	requireUntouched(t, m, m.TenantB, witness)
}
