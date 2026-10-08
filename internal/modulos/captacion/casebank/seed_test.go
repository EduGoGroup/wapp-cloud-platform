package casebank_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/casebank"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/casebank/casebankhelpertest"
)

// seed_test.go — la semilla del caso Ambar. Reglas leídas de los cuatro tests de
// la semilla de internal/casebank/casebank_test.go y de
// TestSembrarElCasoAmbar_EsIdempotenteContraPostgres, no portadas.

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestAmbarCase_ByteForByteTheOldSeed es el candado de la copia: la longitud y el
// sha256 del texto y del `expected` se calcularon ejecutando el paquete VIEJO
// (internal/casebank/semilla.go @ 8d875ab), que este no puede importar. Es el caso
// que `cmd/casebank` siembra en UAT: un byte distinto aquí y la siembra dejaría de
// ser idempotente contra lo ya sembrado.
func TestAmbarCase_ByteForByteTheOldSeed(t *testing.T) {
	cases := []struct {
		name    string
		got     []byte
		wantLen int
		wantSHA string
	}{
		{"source text", []byte(casebank.AmbarCaseText), 552,
			"47cff5bd79d33be9cbb29eb1100b45e3a94b49456aafb4696e17568fc6005a3e"},
		{"expected", casebank.AmbarCaseExpected(), 1489,
			"9433995936bd57dc9b8e7005f636322e4f1e52685f24eb96e0ee82a310db9c3a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if len(c.got) != c.wantLen {
				t.Errorf("longitud = %d bytes; la del viejo es %d", len(c.got), c.wantLen)
			}
			if got := sha256Hex(c.got); got != c.wantSHA {
				t.Errorf("sha256 = %s; el del viejo es %s", got, c.wantSHA)
			}
		})
	}
}

// TestCaseNames_TheOldListAndAFreshSlice: la lista es la del viejo, en su orden, y
// cada llamada devuelve un slice propio.
func TestCaseNames_TheOldListAndAFreshSlice(t *testing.T) {
	want := []string{"Ambar", "Herminia", "Fusión", "Fusion"}
	got := casebank.CaseNames()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CaseNames() = %q; la del viejo es %q", got, want)
	}
	got[0] = "pisado"
	if again := casebank.CaseNames(); !reflect.DeepEqual(again, want) {
		t.Errorf("tocar lo devuelto cambió la lista: %q", again)
	}
}

// TestAmbarCaseText_PassesTheSweep es el criterio literal de T5.3 («el caso
// sembrado pasa el barrido de anonimización»).
//
// 🔴 NO ES UNA TAUTOLOGÍA: `AmbarCaseText` está ESCRITO A MANO y NO pasó por
// `Anonymize`, así que el barrido responde una pregunta abierta. Su control
// negativo es `TestRemains_FindsTheThreeClasses`: sin él, un `Remains` que
// devolviera vacío siempre dejaría este test verde.
func TestAmbarCaseText_PassesTheSweep(t *testing.T) {
	a := casebank.NewAnonymizer(casebank.CaseNames()...)
	if remains := a.Remains(casebank.AmbarCaseText); len(remains) != 0 {
		t.Errorf("el caso sembrado tiene %d restos identificables: %v", len(remains), remains)
	}
}

// TestAmbarCaseText_TheAnonymizerLeavesItIntact es la otra mitad, y es la que de
// verdad vigila al anonimizador: si se volviera goloso con los números, «10 o 12
// porciones», «25 o 30» y «tequeños congelados de 30» —las cantidades sobre las
// que este caso evalúa a P4— saldrían tapadas y el dataset dejaría de medir nada.
func TestAmbarCaseText_TheAnonymizerLeavesItIntact(t *testing.T) {
	a := casebank.NewAnonymizer(casebank.CaseNames()...)
	if got := a.Anonymize(casebank.AmbarCaseText); got != casebank.AmbarCaseText {
		t.Errorf("el anonimizador CAMBIÓ la semilla.\n got: %q\nwant: %q", got, casebank.AmbarCaseText)
	}
}

// TestAmbarCaseExpected_ProvenanceTravelsInsideTheRow: el aviso de calidad C no
// puede quedarse en un comentario de Go. Quien lea la fila dentro de seis meses ve
// el `source_text` y el `expected`, y en ninguno de los dos puede parecer que esto
// es la transcripción real de un cliente.
func TestAmbarCaseExpected_ProvenanceTravelsInsideTheRow(t *testing.T) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(casebank.AmbarCaseExpected(), &doc); err != nil {
		t.Fatalf("el `expected` de la semilla no es JSON válido: %v", err)
	}
	raw, ok := doc["_procedencia"]
	if !ok {
		t.Fatal("el `expected` de la semilla no lleva `_procedencia`: la fila sembrada no declara que es material REDACTADO")
	}
	var prov struct {
		Quality string `json:"calidad"`
		IsReal  *bool  `json:"es_texto_real_del_cliente"`
		Notice  string `json:"aviso"`
	}
	if err := json.Unmarshal(raw, &prov); err != nil {
		t.Fatalf("`_procedencia` no decodifica: %v", err)
	}
	if prov.Quality != "C" {
		t.Errorf("calidad = %q; se esperaba \"C\"", prov.Quality)
	}
	if prov.IsReal == nil || *prov.IsReal {
		t.Error("`es_texto_real_del_cliente` tiene que estar y ser false")
	}
	if prov.Notice == "" {
		t.Error("`_procedencia` sin aviso: la clave sola no explica la consecuencia")
	}
}

// TestAmbarCase_ConsentedAndReadyToSeed: el caso lleva el tenant que se le pasa,
// consentimiento, el texto y el `expected` de la semilla.
func TestAmbarCase_ConsentedAndReadyToSeed(t *testing.T) {
	c := casebank.AmbarCase("t-ambar")
	if c.TenantID != "t-ambar" || !c.Consented || c.SourceText != casebank.AmbarCaseText ||
		string(c.Expected) != string(casebank.AmbarCaseExpected()) {
		t.Errorf("AmbarCase(\"t-ambar\") = %+v; no es la semilla consentida de ese tenant", c)
	}
}

// TestAmbarCase_SeedsThroughTheServiceOnce cierra el círculo: la semilla entra por
// la MISMA puerta que cualquier otro caso, no por un atajo, tal como lo hace
// `cmd/casebank` —con `CaseNames` en el anonimizador—, y la segunda corrida no
// escribe.
//
// 🔴 ESTE TEST **NO** ES LA RED DE LA ANONIMIZACIÓN, Y NO PUEDE SERLO. Sobre el
// texto de Ambar, anonimizar es LA IDENTIDAD, así que quitar la llamada a
// `Anonymize` de `Service` lo deja VERDE. Quien caza esa mutación es
// `TestService_Insert_AnonymizesBeforePersisting`, con un texto que la
// anonimización cambia.
func TestAmbarCase_SeedsThroughTheServiceOnce(t *testing.T) {
	ctx := context.Background()
	mem := casebankhelpertest.NewMemory()
	s, err := casebank.NewService(mem, casebank.NewAnonymizer(casebank.CaseNames()...))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	id, seeded, err := s.Seed(ctx, casebank.AmbarCase("t-ambar"))
	if err != nil || !seeded || id == 0 {
		t.Fatalf("la primera siembra devolvió (%d, %t, %v); se esperaba haber sembrado", id, seeded, err)
	}
	if id2, seeded2, err := s.Seed(ctx, casebank.AmbarCase("t-ambar")); err != nil || seeded2 || id2 != 0 {
		t.Errorf("la segunda siembra devolvió (%d, %t, %v); se esperaba (0, false, nil)", id2, seeded2, err)
	}

	rows := mem.Rows("t-ambar")
	if len(rows) != 1 {
		t.Fatalf("dos corridas dejaron %d filas; se esperaba 1", len(rows))
	}
	if rows[0].SourceText != casebank.AmbarCaseText {
		t.Error("el texto sembrado NO es el de la constante: el anonimizador lo modificó por el camino")
	}
	if string(rows[0].Expected) != string(casebank.AmbarCaseExpected()) || !rows[0].Consented {
		t.Errorf("la fila sembrada no lleva el expected de la semilla o no va consentida: %+v", rows[0])
	}
}
