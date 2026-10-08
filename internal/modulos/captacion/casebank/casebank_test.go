//go:build pendiente

package casebank_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/casebank"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/casebank/casebankhelpertest"
)

// casebank_test.go — EL GUARD DE GO y el orden validar → anonimizar → persistir.
// Reglas leídas de internal/casebank/casebank_test.go (12 tests), no portadas.
//
// 🔴 POR QUÉ ESTE FICHERO MIRA «NO LLEGÓ AL STORE» Y NO «DIO ERROR»: el
// consentimiento lo defienden DOS cosas —el guard de `Service` y el CHECK de la
// 0082, que el doble reproduce— y una defensa duplicada solo vale si cada mitad
// tiene un test que la otra NO puede salvar. Con el guard borrado, el doble
// rechazaría igual el caso sin consentimiento; lo que delata la mutación es que
// el doble RECIBIÓ la llamada (Calls) y que el error ya no es el centinela. La
// otra mitad (el store rechaza por su cuenta) es un caso de
// casebankhelpertest.Contrato.

// El store del servicio es una interfaz que el doble cumple.
var _ casebank.Store = (*casebankhelpertest.Memory)(nil)

const serviceTenant = "t-casebank"

// newService monta el servicio sobre el doble en memoria.
func newService(t *testing.T) (*casebank.Service, *casebankhelpertest.Memory) {
	t.Helper()
	mem := casebankhelpertest.NewMemory()
	s, err := casebank.NewService(mem, newTestAnonymizer())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return s, mem
}

// validCase es el molde del que parten los tests, para que cada uno estropee UNA
// cosa y se vea cuál.
func validCase() casebank.Case {
	return casebank.Case{
		TenantID:   serviceTenant,
		Consented:  true,
		SourceText: "quiero una torta de 10 porciones",
	}
}

// requireNoCalls exige que el store no haya recibido ninguna llamada.
func requireNoCalls(t *testing.T, mem *casebankhelpertest.Memory) {
	t.Helper()
	if inserts, exists := mem.Calls(); inserts != 0 || exists != 0 {
		t.Errorf("el store recibió %d Insert y %d Exists; el guard tiene que cortar ANTES de la base", inserts, exists)
	}
}

// TestSentinels_LiteralTexts: los tres centinelas son distintos y sus textos son
// los del paquete viejo.
func TestSentinels_LiteralTexts(t *testing.T) {
	texts := map[error]string{
		casebank.ErrNoConsent: "casebank: el caso no lleva consentimiento (consented=true) y no se inserta",
		casebank.ErrNoTenant:  "casebank: el caso no lleva tenant_id",
		casebank.ErrNoText:    "casebank: el caso no lleva source_text",
	}
	if len(texts) != 3 {
		t.Fatalf("los tres centinelas no son tres errores distintos: %v", texts)
	}
	for err, want := range texts {
		if err.Error() != want {
			t.Errorf("centinela = %q, se esperaba %q", err, want)
		}
	}
}

// TestNewService_WithoutStore_Fails: sin store no hay servicio.
func TestNewService_WithoutStore_Fails(t *testing.T) {
	s, err := casebank.NewService(nil, casebank.NewAnonymizer())
	if err == nil {
		t.Fatal("NewService(nil, …) no devolvió error")
	}
	if s != nil {
		t.Errorf("NewService(nil, …) devolvió un servicio además del error")
	}
	if got, want := err.Error(), "casebank: NewServicio sin store"; got != want {
		t.Errorf("error = %q, se esperaba el texto del viejo %q", got, want)
	}
}

// guardCases son los rechazos del guard, con el orden en que se miran: tenant,
// texto, consentimiento.
func guardCases() []struct {
	name   string
	mutate func(*casebank.Case)
	want   error
} {
	return []struct {
		name   string
		mutate func(*casebank.Case)
		want   error
	}{
		{"not consented", func(c *casebank.Case) { c.Consented = false }, casebank.ErrNoConsent},
		{"empty tenant", func(c *casebank.Case) { c.TenantID = "" }, casebank.ErrNoTenant},
		{"blank tenant", func(c *casebank.Case) { c.TenantID = " \t\n" }, casebank.ErrNoTenant},
		{"empty text", func(c *casebank.Case) { c.SourceText = "" }, casebank.ErrNoText},
		{"blank text", func(c *casebank.Case) { c.SourceText = "  \n " }, casebank.ErrNoText},
		{"tenant is checked before text", func(c *casebank.Case) { c.TenantID, c.SourceText = "", "" }, casebank.ErrNoTenant},
		{"tenant is checked before consent", func(c *casebank.Case) { c.TenantID, c.Consented = "", false }, casebank.ErrNoTenant},
		{"text is checked before consent", func(c *casebank.Case) { c.SourceText, c.Consented = "", false }, casebank.ErrNoText},
	}
}

// TestService_Insert_Guard_DoesNotReachTheStore es el criterio literal de T5.3
// («insert sin consentimiento ⇒ error») y sus dos hermanos: el centinela, id 0 y
// NI UNA llamada al store.
func TestService_Insert_Guard_DoesNotReachTheStore(t *testing.T) {
	for _, g := range guardCases() {
		t.Run(g.name, func(t *testing.T) {
			s, mem := newService(t)
			c := validCase()
			g.mutate(&c)
			id, err := s.Insert(context.Background(), c)
			if !errors.Is(err, g.want) {
				t.Errorf("Insert = error %v; se esperaba %v", err, g.want)
			}
			if id != 0 {
				t.Errorf("Insert rechazado devolvió id %d", id)
			}
			requireNoCalls(t, mem)
		})
	}
}

// TestService_Seed_Guard_NeitherAsksNorWrites: el guard manda también en la
// siembra, y corta antes de la consulta de idempotencia.
func TestService_Seed_Guard_NeitherAsksNorWrites(t *testing.T) {
	for _, g := range guardCases() {
		t.Run(g.name, func(t *testing.T) {
			s, mem := newService(t)
			c := validCase()
			g.mutate(&c)
			id, seeded, err := s.Seed(context.Background(), c)
			if !errors.Is(err, g.want) {
				t.Errorf("Seed = error %v; se esperaba %v", err, g.want)
			}
			if id != 0 || seeded {
				t.Errorf("Seed rechazado devolvió (%d, %t)", id, seeded)
			}
			requireNoCalls(t, mem)
		})
	}
}

// TestService_Insert_ConsentedCase_Persists es la mitad complementaria, sin la
// cual un servicio que rechazara todo pasaría los tests del guard.
func TestService_Insert_ConsentedCase_Persists(t *testing.T) {
	s, mem := newService(t)
	c := validCase()
	c.Expected = json.RawMessage(`{"version":1}`)
	id, err := s.Insert(context.Background(), c)
	if err != nil {
		t.Fatalf("Insert de un caso consentido: %v", err)
	}
	rows := mem.Rows(serviceTenant)
	if len(rows) != 1 {
		t.Fatalf("quedaron %d filas, se esperaba 1", len(rows))
	}
	want := casebankhelpertest.Row{
		ID: id, TenantID: serviceTenant, Consented: true, SourceText: c.SourceText, Expected: c.Expected,
	}
	if got := rows[0]; id == 0 || got.ID != want.ID || got.TenantID != want.TenantID || !got.Consented ||
		got.SourceText != want.SourceText || string(got.Expected) != string(want.Expected) {
		t.Errorf("fila guardada = %+v (expected=%s); se esperaba %+v (expected=%s)", got, got.Expected, want, want.Expected)
	}
	if inserts, exists := mem.Calls(); inserts != 1 || exists != 0 {
		t.Errorf("Insert hizo %d Insert y %d Exists en el store; se esperaba 1 y 0", inserts, exists)
	}
}

// dirtyText lleva las tres clases de PII, y cleanText es lo que tiene que llegar
// al store.
const (
	dirtyText = "Ambar escribió al +58 412 123 4567 desde 584121234567@s.whatsapp.net: quiero 2 tortas"
	cleanText = "[NOMBRE] escribió al [TELEFONO] desde [JID]: quiero 2 tortas"
)

// TestService_Insert_AnonymizesBeforePersisting es el candado de la otra mitad de
// T5.3: el literal crudo NO sale de este proceso. Hace falta un texto que la
// anonimización CAMBIE: con la semilla de Ambar, anonimizar es la identidad y
// quitar el paso dejaría el test verde.
func TestService_Insert_AnonymizesBeforePersisting(t *testing.T) {
	s, mem := newService(t)
	c := validCase()
	c.SourceText = dirtyText
	if _, err := s.Insert(context.Background(), c); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	rows := mem.Rows(serviceTenant)
	if len(rows) != 1 {
		t.Fatalf("quedaron %d filas, se esperaba 1", len(rows))
	}
	if got := rows[0].SourceText; got != cleanText {
		t.Errorf("al store llegó %q; se esperaba el texto ANONIMIZADO %q", got, cleanText)
	}
}

// TestService_Insert_StoreFails_WrappedError: el fallo del store sale envuelto,
// con el tenant en el prefijo.
func TestService_Insert_StoreFails_WrappedError(t *testing.T) {
	s, mem := newService(t)
	boom := errors.New("boom")
	mem.FailInsert(boom)
	id, err := s.Insert(context.Background(), validCase())
	if !errors.Is(err, boom) {
		t.Fatalf("Insert = error %v; se esperaba que envolviera el del store", err)
	}
	if id != 0 {
		t.Errorf("Insert fallido devolvió id %d", id)
	}
	if got, want := err.Error(), `casebank: insertar el caso del tenant "t-casebank": boom`; got != want {
		t.Errorf("error = %q, se esperaba %q", got, want)
	}
}

// TestService_Seed_IsIdempotent: la primera corrida escribe y lo dice; la segunda
// no escribe y devuelve (0, false) sin error.
func TestService_Seed_IsIdempotent(t *testing.T) {
	s, mem := newService(t)
	ctx := context.Background()

	id, seeded, err := s.Seed(ctx, validCase())
	if err != nil || !seeded || id == 0 {
		t.Fatalf("la primera siembra devolvió (%d, %t, %v); se esperaba (id, true, nil)", id, seeded, err)
	}
	if rows := mem.Rows(serviceTenant); len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("tras la primera siembra las filas son %+v; se esperaba una con id %d", rows, id)
	}

	id, seeded, err = s.Seed(ctx, validCase())
	if err != nil || seeded || id != 0 {
		t.Errorf("la segunda siembra devolvió (%d, %t, %v); se esperaba (0, false, nil)", id, seeded, err)
	}
	if rows := mem.Rows(serviceTenant); len(rows) != 1 {
		t.Errorf("la segunda siembra dejó %d filas; la siembra no es idempotente", len(rows))
	}
	if inserts, _ := mem.Calls(); inserts != 1 {
		t.Errorf("el store recibió %d Insert en dos siembras; se esperaba 1", inserts)
	}
}

// TestService_Seed_AsksForTheAnonymizedText es el defecto que este método tiene a
// mano: preguntar por el crudo daría siempre «no existe» y sembraría un duplicado
// en cada corrida. Las dos mitades: el caso sucio sembrado dos veces deja UNA
// fila, anonimizada; y si el literal ya anonimizado está en la base, el crudo no
// se siembra.
func TestService_Seed_AsksForTheAnonymizedText(t *testing.T) {
	ctx := context.Background()
	dirty := validCase()
	dirty.SourceText = dirtyText

	t.Run("dirty case seeded twice", func(t *testing.T) {
		s, mem := newService(t)
		for i := range 2 {
			if _, _, err := s.Seed(ctx, dirty); err != nil {
				t.Fatalf("siembra %d: %v", i+1, err)
			}
		}
		rows := mem.Rows(serviceTenant)
		if len(rows) != 1 {
			t.Fatalf("dos siembras del mismo caso sucio dejaron %d filas; se preguntó por el texto crudo", len(rows))
		}
		if rows[0].SourceText != cleanText {
			t.Errorf("la fila sembrada lleva %q; se esperaba el texto anonimizado %q", rows[0].SourceText, cleanText)
		}
	})

	t.Run("anonymized literal already in the bank", func(t *testing.T) {
		s, mem := newService(t)
		clean := validCase()
		clean.SourceText = cleanText
		if _, err := mem.Insert(ctx, clean); err != nil {
			t.Fatalf("sembrando el literal anonimizado a mano: %v", err)
		}
		id, seeded, err := s.Seed(ctx, dirty)
		if err != nil || seeded || id != 0 {
			t.Errorf("Seed del crudo devolvió (%d, %t, %v); ya estaba su forma anonimizada", id, seeded, err)
		}
		if rows := mem.Rows(serviceTenant); len(rows) != 1 {
			t.Errorf("quedaron %d filas; se esperaba 1", len(rows))
		}
	})

	t.Run("same text of another tenant does not count", func(t *testing.T) {
		s, mem := newService(t)
		other := dirty
		other.TenantID = "t-otro"
		if _, _, err := s.Seed(ctx, other); err != nil {
			t.Fatalf("sembrando en el otro tenant: %v", err)
		}
		if _, seeded, err := s.Seed(ctx, dirty); err != nil || !seeded {
			t.Errorf("Seed devolvió (%t, %v); el caso de otro tenant no es «ya estaba»", seeded, err)
		}
		if rows := mem.Rows(serviceTenant); len(rows) != 1 {
			t.Errorf("el tenant quedó con %d filas; se esperaba 1", len(rows))
		}
	})
}

// TestService_Seed_StoreFails_WrappedErrors: los dos fallos del store salen
// envueltos con su prefijo, y si falla la consulta no se escribe.
func TestService_Seed_StoreFails_WrappedErrors(t *testing.T) {
	boom := errors.New("boom")

	t.Run("exists fails", func(t *testing.T) {
		s, mem := newService(t)
		mem.FailExists(boom)
		id, seeded, err := s.Seed(context.Background(), validCase())
		if !errors.Is(err, boom) || id != 0 || seeded {
			t.Fatalf("Seed = (%d, %t, %v); se esperaba (0, false, boom envuelto)", id, seeded, err)
		}
		if got, want := err.Error(), "casebank: comprobar si el caso ya estaba: boom"; got != want {
			t.Errorf("error = %q, se esperaba %q", got, want)
		}
		if inserts, _ := mem.Calls(); inserts != 0 {
			t.Errorf("con la consulta fallida el store recibió %d Insert", inserts)
		}
	})

	t.Run("insert fails", func(t *testing.T) {
		s, mem := newService(t)
		mem.FailInsert(boom)
		id, seeded, err := s.Seed(context.Background(), validCase())
		if !errors.Is(err, boom) || id != 0 || seeded {
			t.Fatalf("Seed = (%d, %t, %v); se esperaba (0, false, boom envuelto)", id, seeded, err)
		}
		if got, want := err.Error(), `casebank: sembrar el caso del tenant "t-casebank": boom`; got != want {
			t.Errorf("error = %q, se esperaba %q", got, want)
		}
	})
}
