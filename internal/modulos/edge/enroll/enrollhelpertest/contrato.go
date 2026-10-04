// Package enrollhelpertest trae las suites de contrato de los dos puertos de enroll
// (enroll.CodeStore y enroll.EdgeCertRepository) y sus dobles en memoria (D-F3-1: los dobles
// vivían en el paquete de producción viejo, como MemoryStore y MemoryEdgeCertRepository).
// Ningún código de producción lo importa: arrastra "testing".
//
//   - contrato.go: ContratoCodeStore, su MontajeCodeStore y sus casos.
//   - edgecert_contrato.go: ContratoEdgeCertRepository, su MontajeEdgeCertRepository y sus casos.
//   - memoria.go: MemoriaCodeStore y MemoriaEdgeCertRepository.
//
// Las suites las corren las dos implementaciones de cada puerto: los dobles en unitario
// (memoria_test.go) y los adaptadores Postgres en los procesos de F9 (sesión F3-05).
//
// Los casos salen de plan/F3-edge/diseno.md §2 y de los tests viejos de internal/gateway/enroll
// @ 8896f13 (TestEnrollEdge_*, TestIntegration_ConsumeIsAtomic), leídos, no portados.
package enrollhelpertest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll"
	"github.com/google/uuid"
)

// MontajeCodeStore es lo que cada implementación entrega a ContratoCodeStore para UN caso.
// Tiene que venir limpio —sin códigos— porque los casos reutilizan los mismos códigos: la suite
// llama a nuevo una vez por caso.
type MontajeCodeStore struct {
	// Store es la implementación bajo prueba.
	Store enroll.CodeStore
	// SeedCode siembra un código de activación SIN USAR que vence en expiresAt, para un tenant
	// que existe, y devuelve el id de ese tenant (con forma de UUID; uno distinto por llamada).
	// El puerto solo tiene Consume, así que sembrar es cosa del montaje: con Postgres crea la
	// fila de public.tenants y la de public.enrollment_codes; con el doble, MemoriaCodeStore.Add.
	// El código se guarda TAL CUAL. Falla el test t si no puede sembrar.
	SeedCode func(t *testing.T, code string, expiresAt time.Time) (tenantID string)
}

// ContratoCodeStore ejecuta las promesas de enroll.CodeStore contra la implementación que
// devuelve nuevo, con un montaje limpio por caso. No salta nada.
//
// La suite afirma SOLO LO COMÚN al doble y a Postgres. Divergen a propósito en QUÉ centinela
// devuelve un consumo fallido: el doble distingue ErrCodeNotFound, ErrCodeExpired y ErrCodeUsed;
// Postgres consume con un único UPDATE atómico y, si no afecta filas, devuelve siempre
// ErrCodeInvalid, sin revelar la causa. Por eso aquí un consumo fallido solo se afirma como
// «falla, con uno de los cuatro centinelas ErrCode* y sin tenant»; el centinela exacto de cada
// implementación lo fija su propio test.
func ContratoCodeStore(t *testing.T, nuevo func(t *testing.T) MontajeCodeStore) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("enrollhelpertest.ContratoCodeStore: nuevo es nil; hace falta una función que devuelva un MontajeCodeStore")
	}
	cases := []struct {
		name string
		run  func(t *testing.T, m MontajeCodeStore)
	}{
		{"ValidCode_ReturnsItsTenant", caseValidCode},                   // código válido → su tenant
		{"SecondConsume_Fails", caseSecondConsume},                      // un solo uso
		{"UnknownCode_Fails", caseUnknownCode},                          // código desconocido
		{"ExpiredCode_Fails", caseExpiredCode},                          // vencido, aunque nunca se usó
		{"Codes_AreIndependent", caseCodesIndependent},                  // quemar uno no quema otro
		{"Code_IsComparedVerbatim", caseVerbatim},                       // sin normalizar
		{"ConcurrentConsume_ExactlyOneWins", caseConcurrentConsumeOnce}, // consumo atómico
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			switch {
			case m.Store == nil:
				t.Fatal("MontajeCodeStore.Store es nil")
			case m.SeedCode == nil:
				t.Fatal("MontajeCodeStore.SeedCode es nil: la suite necesita sembrar códigos")
			}
			c.run(t, m)
		})
	}
}

const (
	codeOne = "CONTRACT-CODE-1"
	codeTwo = "CONTRACT-CODE-2"
)

// seedCode siembra un código que vence dentro de una hora y comprueba el tenant devuelto.
func seedCode(t *testing.T, m MontajeCodeStore, code string) string {
	t.Helper()
	return seedCodeUntil(t, m, code, time.Now().Add(time.Hour))
}

func seedCodeUntil(t *testing.T, m MontajeCodeStore, code string, expiresAt time.Time) string {
	t.Helper()
	tenant := m.SeedCode(t, code, expiresAt)
	if _, err := uuid.Parse(tenant); err != nil {
		t.Fatalf("MontajeCodeStore.SeedCode devolvió el tenant %q, que no es un UUID bien formado: %v", tenant, err)
	}
	return tenant
}

// isCodeError dice si err es uno de los cuatro centinelas de consumo.
func isCodeError(err error) bool {
	return errors.Is(err, enroll.ErrCodeNotFound) || errors.Is(err, enroll.ErrCodeExpired) ||
		errors.Is(err, enroll.ErrCodeUsed) || errors.Is(err, enroll.ErrCodeInvalid)
}

// requireConsumed afirma que consumir code tiene éxito y devuelve ese tenant.
func requireConsumed(t *testing.T, m MontajeCodeStore, code, wantTenant string) {
	t.Helper()
	tenant, err := m.Store.Consume(context.Background(), code)
	if err != nil {
		t.Fatalf("Consume(%q): error inesperado %v", code, err)
	}
	if tenant != wantTenant {
		t.Errorf("Consume(%q) = tenant %q, quería %q", code, tenant, wantTenant)
	}
}

// requireRejected afirma que consumir code FALLA: un centinela ErrCode* y ningún tenant.
func requireRejected(t *testing.T, m MontajeCodeStore, code, why string) {
	t.Helper()
	tenant, err := m.Store.Consume(context.Background(), code)
	if err == nil {
		t.Fatalf("Consume(%q) tuvo éxito (tenant %q); tenía que fallar: %s", code, tenant, why)
	}
	if !isCodeError(err) {
		t.Errorf("Consume(%q): error %v, quería uno de los centinelas ErrCode* (%s)", code, err, why)
	}
	if tenant != "" {
		t.Errorf("Consume(%q) falló pero devolvió el tenant %q; quería \"\"", code, tenant)
	}
}

func caseValidCode(t *testing.T, m MontajeCodeStore) {
	tenant := seedCode(t, m, codeOne)
	requireConsumed(t, m, codeOne, tenant)
}

// caseSecondConsume: el código es de UN solo uso. El segundo consumo falla, y el tercero.
func caseSecondConsume(t *testing.T, m MontajeCodeStore) {
	tenant := seedCode(t, m, codeOne)
	requireConsumed(t, m, codeOne, tenant)
	requireRejected(t, m, codeOne, "ya se consumió")
	requireRejected(t, m, codeOne, "ya se consumió")
}

func caseUnknownCode(t *testing.T, m MontajeCodeStore) {
	seedCode(t, m, codeOne)
	requireRejected(t, m, "CONTRACT-CODE-NEVER-SEEDED", "nadie lo sembró")
}

// caseExpiredCode: un código vencido no se consume aunque nunca se haya usado, y sigue sin
// poder consumirse.
func caseExpiredCode(t *testing.T, m MontajeCodeStore) {
	seedCodeUntil(t, m, codeOne, time.Now().Add(-time.Hour))
	requireRejected(t, m, codeOne, "venció hace una hora")
	requireRejected(t, m, codeOne, "venció hace una hora")
}

// caseCodesIndependent: cada código es de su tenant, y consumir (o fallar con) uno no toca al otro.
func caseCodesIndependent(t *testing.T, m MontajeCodeStore) {
	tenantOne := seedCode(t, m, codeOne)
	tenantTwo := seedCode(t, m, codeTwo)
	if tenantOne == tenantTwo {
		t.Fatalf("MontajeCodeStore.SeedCode devolvió dos veces el mismo tenant %q", tenantOne)
	}
	requireConsumed(t, m, codeOne, tenantOne)
	requireRejected(t, m, codeOne, "ya se consumió")
	requireConsumed(t, m, codeTwo, tenantTwo)
}

// caseVerbatim: el código se compara TAL CUAL. Ninguna implementación lo normaliza: con un
// espacio delante o detrás, un carácter invisible, otra capitalización o vacío, es un código
// desconocido. Y esos intentos no queman el código bueno, que después se consume.
func caseVerbatim(t *testing.T, m MontajeCodeStore) {
	tenant := seedCode(t, m, codeOne)
	for name, code := range AdversarialCodes(codeOne) {
		requireRejected(t, m, code, "no es el código sembrado: "+name)
	}
	requireConsumed(t, m, codeOne, tenant)
}

// AdversarialCodes devuelve variantes de code que un humano leería como «el mismo código» y que
// NO lo son: el enrolamiento no normaliza, así que todas son códigos desconocidos. La clave es
// el nombre de la variante. Lo usan esta suite y los tests de enroll.Service y enroll.Server.
func AdversarialCodes(code string) map[string]string {
	return map[string]string{
		"leading space":             " " + code,
		"trailing space":            code + " ",
		"trailing newline":          code + "\n",
		"leading zero width U+200B": "\u200b" + code,
		"leading BOM U+FEFF":        "\ufeff" + code,
		"other case":                swapCase(code),
		"empty":                     "",
	}
}

// swapCase cambia mayúsculas por minúsculas y al revés en las letras ASCII.
func swapCase(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z':
			b[i] = c - 'a' + 'A'
		case c >= 'A' && c <= 'Z':
			b[i] = c - 'A' + 'a'
		}
	}
	return string(b)
}

// caseConcurrentConsumeOnce: N consumos a la vez del mismo código; exactamente UNO gana y se
// lleva el tenant; los demás fallan con un centinela. Es la promesa «atómico».
func caseConcurrentConsumeOnce(t *testing.T, m MontajeCodeStore) {
	tenant := seedCode(t, m, codeOne)

	const attempts = 32
	type result struct {
		tenant string
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Go(func() {
			<-start
			got, err := m.Store.Consume(context.Background(), codeOne)
			results <- result{tenant: got, err: err}
		})
	}
	close(start)
	wg.Wait()
	close(results)

	wins := 0
	for r := range results {
		switch {
		case r.err == nil:
			wins++
			if r.tenant != tenant {
				t.Errorf("el consumo que ganó devolvió el tenant %q, quería %q", r.tenant, tenant)
			}
		case !isCodeError(r.err):
			t.Errorf("un consumo que perdió devolvió %v, quería un centinela ErrCode*", r.err)
		case r.tenant != "":
			t.Errorf("un consumo que perdió devolvió el tenant %q", r.tenant)
		}
	}
	if wins != 1 {
		t.Errorf("de %d consumos a la vez ganaron %d; el código es de un solo uso: exactamente 1", attempts, wins)
	}
}
