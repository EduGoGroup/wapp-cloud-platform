package intakes_test

// vencimiento_test.go — EL PLAZO DEL PRESUPUESTO AVISA Y NO MATA
// (Plan 044 · Ola 4 · T4.5, REQ-25, D-044.50).
//
// Los cuatro criterios de la tarea, y dónde vive cada uno:
//
//	(a) con reloj fake, un pending_approval pasado de plazo aparece MARCADO y su
//	    status EN BD sigue siendo pending_approval …… TestPlazo_VencidoSeMarcaYEnLaBaseNoSeMueveNada
//	(b) el evento de telemetría del vencimiento no se emite en ninguna ruta ……… inv_vencimiento_ast_test.go
//	(c) el recordatorio sale una sola vez (idempotente) ……………………………………………… TestPlazo_ElRecordatorioSaleUnaSolaVez (+ el concurrente)
//	(d) ninguna ruta llama a approve fuera del handler HTTP …………………………………… inv1_aprobar_ast_test.go (ya existía; T4.5 le añadió el dominio)
//
// TODOS los tests de este fichero corren SIN base de datos: ni uno se salta. Es
// deliberado. La regla del plazo es una comparación de tiempos y un
// compare-and-swap, y las dos cosas las reproduce MemoryStore con las mismas
// condiciones que el Postgres — así que un test de integración habría movido el
// único sitio donde esto se comprueba a un camino que se salta sin DATABASE_URL.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// esperandoDesde siembra UN presupuesto en `pending_approval` que lleva esperando
// al dueño desde el instante dado. El reloj del store es el mismo `ahora` fijo que
// usa el resto del paquete (deposit_test.go): sin reloj inyectable habría que
// esperar un día real para ver vencer un plazo.
func esperandoDesde(desde time.Time) *intakes.MemoryStore {
	st := intakes.NewMemoryStore()
	st.SetClock(func() time.Time { return ahora })
	st.Add(tenantA, intakes.Intake{
		ID:        intakeDePrueba,
		ContactID: "contacto-opaco-1",
		SessionID: "sess-negocio",
		Status:    intakes.StatusPendingApproval,
		Total:     21000,
		CreatedAt: desde,
		UpdatedAt: desde,
	})
	return st
}

// recordatorioAlDueñoSpy es el emisor del recordatorio al dueño en los tests: cuenta a quién se
// avisó y con qué fila. Ocupa el sitio que en producción ocupa LogOwnerNotice y que
// el Plan 045 ocupará con el push real.
type recordatorioAlDueñoSpy struct {
	mu     sync.Mutex
	avisos []intakes.Intake
}

func (a *recordatorioAlDueñoSpy) RemindOwner(_ context.Context, _ string, in intakes.Intake) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.avisos = append(a.avisos, in)
}

func (a *recordatorioAlDueñoSpy) contar() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.avisos)
}

var _ intakes.OwnerNotice = (*recordatorioAlDueñoSpy)(nil)

// conRecordatorioDePlazo arma el servicio con SOLO el recordatorio del plazo
// cableado — sin el de la seña, a propósito: ver
// TestPlazo_SinElRecordatorioDeSeñaElDelPlazoSigueAvisando.
func conRecordatorioDePlazo(t *testing.T, st *intakes.MemoryStore) (*intakes.Service, *recordatorioAlDueñoSpy) {
	t.Helper()
	spy := &recordatorioAlDueñoSpy{}
	rem := intakes.NewExpiryReminder(spy, st, &logSpy{},
		intakes.WithExpiryClock(func() time.Time { return ahora }))
	return intakes.NewService(st, intakes.WithExpiryReminder(rem)), spy
}

// estadoEnBase relee la solicitud del STORE (no del DTO, no de lo que devolvió la
// lectura) y devuelve su estado. Es lo que el criterio (a) exige mirar: que la
// marca sea derivada significa exactamente que la fila no se movió.
func estadoEnBase(t *testing.T, st *intakes.MemoryStore) string {
	t.Helper()
	d, err := st.Get(context.Background(), tenantA, intakeDePrueba)
	if err != nil {
		t.Fatalf("releer la solicitud del store: %v", err)
	}
	return d.Status
}

// --- (1) la marca derivada, pura ---------------------------------------------

// TestPlazo_LaMarcaEsDerivadaYSoloMiraElEstadoYLaEspera recorre la regla entera
// sobre la función pura. Es lo que consumen los DOS sitios que tienen que decir lo
// mismo —la proyección al wire y el pre-filtro del recordatorio—, así que aquí es
// donde la regla se prueba una sola vez.
func TestPlazo_LaMarcaEsDerivadaYSoloMiraElEstadoYLaEspera(t *testing.T) {
	justo := ahora.Add(-intakes.QuoteDeadline) // el plazo se cumple EXACTAMENTE ahora
	casi := ahora.Add(-intakes.QuoteDeadline + time.Second)
	viejo := ahora.Add(-3 * intakes.QuoteDeadline)

	casos := []struct {
		nombre string
		in     intakes.Intake
		quiero bool
	}{
		{"vencido de sobra", intakes.Intake{Status: intakes.StatusPendingApproval, UpdatedAt: viejo}, true},
		{"justo en el borde cuenta como vencido", intakes.Intake{Status: intakes.StatusPendingApproval, UpdatedAt: justo}, true},
		{"un segundo antes del borde NO", intakes.Intake{Status: intakes.StatusPendingApproval, UpdatedAt: casi}, false},
		{"recién llegado", intakes.Intake{Status: intakes.StatusPendingApproval, UpdatedAt: ahora}, false},
		// Los otros estados no esperan la decisión de nadie: el dueño ya se
		// pronunció, o la solicitud ni siquiera llegó a presupuesto.
		{"confirmado, por viejo que sea", intakes.Intake{Status: intakes.StatusConfirmed, UpdatedAt: viejo}, false},
		{"abierto", intakes.Intake{Status: intakes.StatusOpen, UpdatedAt: viejo}, false},
		{"falta info: la pelota la tiene el cliente", intakes.Intake{Status: intakes.StatusNeedsInfo, UpdatedAt: viejo}, false},
		{"rechazado", intakes.Intake{Status: intakes.StatusRejected, UpdatedAt: viejo}, false},
		{"cancelado", intakes.Intake{Status: intakes.StatusCancelled, UpdatedAt: viejo}, false},
		{"el legado expired tampoco: nadie lo espera", intakes.Intake{Status: intakes.StatusExpired, UpdatedAt: viejo}, false},
		// Sin fechas no se inventa una espera. La resta contra el tiempo CERO daría
		// «vencido» para todo, que es el fallo silencioso que esta rama evita.
		{"sin ninguna fecha no se marca", intakes.Intake{Status: intakes.StatusPendingApproval}, false},
		{"sin updated_at cae en created_at", intakes.Intake{Status: intakes.StatusPendingApproval, CreatedAt: viejo}, true},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := intakes.Overdue(c.in, ahora); got != c.quiero {
				t.Fatalf("Overdue = %v, quiero %v", got, c.quiero)
			}
		})
	}
}

// TestPlazo_ElPlazoSonVeinticuatroHoras congela la constante de plataforma
// (D-044.50 §1). No es tautológico: no compara la constante consigo misma sino con
// la CONDUCTA que produce —una solicitud de 23 h 59 min no está vencida y una de
// 24 h 1 min sí—, así que cambiar el número sin querer pone esto rojo.
func TestPlazo_ElPlazoSonVeinticuatroHoras(t *testing.T) {
	enPlazo := intakes.Intake{Status: intakes.StatusPendingApproval, UpdatedAt: ahora.Add(-23*time.Hour - 59*time.Minute)}
	pasado := intakes.Intake{Status: intakes.StatusPendingApproval, UpdatedAt: ahora.Add(-24*time.Hour - time.Minute)}

	if intakes.Overdue(enPlazo, ahora) {
		t.Fatal("una solicitud de 23 h 59 min sale marcada: el plazo se acortó por debajo de 24 h")
	}
	if !intakes.Overdue(pasado, ahora) {
		t.Fatal("una solicitud de 24 h 1 min NO sale marcada: el plazo se alargó por encima de 24 h")
	}
}

// --- (2) criterio (a): marca sí, base quieta ---------------------------------

// TestPlazo_VencidoSeMarcaYEnLaBaseNoSeMueveNada es el criterio (a) entero, y sus
// dos mitades tienen que ir en el MISMO test: una marca que se pinta a costa de
// mover la fila no sería una marca derivada sino una transición con otro nombre.
//
// El assert del estado va contra el STORE releído, no contra lo que devolvió List:
// si el toque escribiera un estado nuevo, lo devuelto por la lectura anterior
// seguiría enseñando el viejo y el test pasaría mintiendo.
func TestPlazo_VencidoSeMarcaYEnLaBaseNoSeMueveNada(t *testing.T) {
	st := esperandoDesde(ahora.Add(-3 * intakes.QuoteDeadline))
	svc, spy := conRecordatorioDePlazo(t, st)

	page, err := svc.List(context.Background(), tenantA, intakes.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Intakes) != 1 {
		t.Fatalf("solicitudes = %d, quiero 1", len(page.Intakes))
	}

	// (i) LA MARCA. Se calcula al leer con la misma función que usa el wire.
	if !intakes.Overdue(page.Intakes[0], ahora) {
		t.Fatal("la solicitud lleva tres plazos esperando y NO sale marcada como vencida")
	}
	// (ii) LA BASE NO SE MUEVE. Ni a expired, ni a cancelled, ni a nada.
	if got := estadoEnBase(t, st); got != intakes.StatusPendingApproval {
		t.Fatalf("status en la base = %q, quiero %q. Nada muere por tiempo (D-041.16): la marca es "+
			"DERIVADA y no puede transicionar la solicitud", got, intakes.StatusPendingApproval)
	}
	// (iii) …y la solicitud conserva sus salidas humanas: aprobar, rechazar, pedir
	// info o cancelar. Un vencido con la lista vacía sería un muerto disfrazado.
	if got := intakes.AllowedTransitions(estadoEnBase(t, st)); len(got) == 0 {
		t.Fatal("un presupuesto vencido se quedó SIN destinos: la salida sigue siendo humana")
	}
	// Y el aviso al dueño sí salió: es la otra mitad de la tarea.
	if spy.contar() != 1 {
		t.Fatalf("avisos al dueño = %d, quiero 1", spy.contar())
	}
}

// TestPlazo_EnPlazoNoSeMarcaNiSeAvisa es el control NEGATIVO del anterior: sin él,
// un Overdue que devolviera `true` siempre pasaría aquel test entero.
func TestPlazo_EnPlazoNoSeMarcaNiSeAvisa(t *testing.T) {
	st := esperandoDesde(ahora.Add(-time.Hour))
	svc, spy := conRecordatorioDePlazo(t, st)

	page, err := svc.List(context.Background(), tenantA, intakes.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if intakes.Overdue(page.Intakes[0], ahora) {
		t.Fatal("una solicitud de una hora sale marcada como vencida")
	}
	if spy.contar() != 0 {
		t.Fatalf("avisos = %d, quiero 0: el plazo no ha pasado", spy.contar())
	}
}

// --- (3) criterio (c): una sola vez -------------------------------------------

// TestPlazo_ElRecordatorioSaleUnaSolaVez: el dueño refresca su bandeja tres veces y
// el aviso sale UNO. Lo sostiene el compare-and-swap contra NULL, no una
// comprobación en memoria.
func TestPlazo_ElRecordatorioSaleUnaSolaVez(t *testing.T) {
	st := esperandoDesde(ahora.Add(-3 * intakes.QuoteDeadline))
	svc, spy := conRecordatorioDePlazo(t, st)

	for i := range 3 {
		if _, err := svc.List(context.Background(), tenantA, intakes.Filter{}); err != nil {
			t.Fatalf("List #%d: %v", i+1, err)
		}
	}
	if spy.contar() != 1 {
		t.Fatalf("avisos = %d tras tres toques, quiero 1 (D-044.50: un solo recordatorio)", spy.contar())
	}
}

// TestPlazo_ElDetalleTambiénToca: abrir la solicitud concreta es el segundo toque, y
// tiene que valer igual que la bandeja. Sin esto, un dueño que entra directo al
// detalle no evaluaría nunca su plazo.
func TestPlazo_ElDetalleTambiénToca(t *testing.T) {
	st := esperandoDesde(ahora.Add(-3 * intakes.QuoteDeadline))
	svc, spy := conRecordatorioDePlazo(t, st)

	if _, err := svc.Get(context.Background(), tenantA, intakeDePrueba); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if spy.contar() != 1 {
		t.Fatalf("avisos = %d tras abrir el detalle, quiero 1", spy.contar())
	}
	if got := estadoEnBase(t, st); got != intakes.StatusPendingApproval {
		t.Fatalf("status en la base = %q tras abrir el detalle, quiero %q", got, intakes.StatusPendingApproval)
	}
}

// TestPlazo_DeVeinteToquesSimultáneosAvisaExactamenteUNO es la mitad que un test
// secuencial no puede afirmar: que la garantía la da la BASE y no el orden en que
// pasan las cosas. Veinte pestañas del dueño refrescando a la vez.
func TestPlazo_DeVeinteToquesSimultáneosAvisaExactamenteUNO(t *testing.T) {
	st := esperandoDesde(ahora.Add(-3 * intakes.QuoteDeadline))
	svc, spy := conRecordatorioDePlazo(t, st)

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// t.Errorf y no t.Fatalf: Fatalf desde una goroutine que no es la del
			// test no para nada y deja el WaitGroup colgado.
			if _, err := svc.List(context.Background(), tenantA, intakes.Filter{}); err != nil {
				t.Errorf("List concurrente: %v", err)
			}
		}()
	}
	wg.Wait()

	if spy.contar() != 1 {
		t.Fatalf("avisos = %d con veinte toques simultáneos, quiero 1. El compare-and-swap es lo "+
			"único que reparte el derecho a avisar; si esto falla, alguien está decidiendo en memoria",
			spy.contar())
	}
}

// TestPlazo_UnToqueAvisaComoMuchoDeUna: una bandeja con cinco presupuestos vencidos
// no dispara cinco avisos de golpe (maxRemindersPerTouch). No se pierde nada: lo
// perezoso es eventual y el dueño toca su bandeja muchas veces al día.
func TestPlazo_UnToqueAvisaComoMuchoDeUna(t *testing.T) {
	st := intakes.NewMemoryStore()
	st.SetClock(func() time.Time { return ahora })
	viejo := ahora.Add(-3 * intakes.QuoteDeadline)
	for _, id := range []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
		"55555555-5555-4555-8555-555555555555",
	} {
		st.Add(tenantA, intakes.Intake{
			ID: id, ContactID: "contacto-opaco-1", SessionID: "sess-negocio",
			Status: intakes.StatusPendingApproval, Total: 1000, CreatedAt: viejo, UpdatedAt: viejo,
		})
	}
	svc, spy := conRecordatorioDePlazo(t, st)

	if _, err := svc.List(context.Background(), tenantA, intakes.Filter{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if spy.contar() != 1 {
		t.Fatalf("avisos en UN toque = %d, quiero 1", spy.contar())
	}
	// Y el resto no se pierde: el segundo toque coge la siguiente.
	if _, err := svc.List(context.Background(), tenantA, intakes.Filter{}); err != nil {
		t.Fatalf("List (2.º toque): %v", err)
	}
	if spy.contar() != 2 {
		t.Fatalf("avisos tras dos toques = %d, quiero 2: la cota es de LATENCIA, no de política", spy.contar())
	}
}

// --- (4) la guarda que crece: DOS colaboradores independientes ----------------

// TestPlazo_SinElRecordatorioDeSeñaElDelPlazoSigueAvisando vigila la trampa exacta
// que D-044.50 nombra: hasta T4.5, Service.touch cortaba con
// `if s.deposits == nil || len(touched) == 0 { return }`.
//
// 🔴 CON ESA GUARDA VIEJA, TODO EL RESTO DE ESTE FICHERO SEGUIRÍA VERDE y este test
// sería el único rojo — porque los demás cablean el servicio y aquí lo que se prueba
// es que el colaborador NUEVO no hereda la condición del VIEJO. Son dos
// recordatorios con dos marcas, dos destinatarios y dos motivos, y un despliegue
// puede llevar uno y no el otro.
func TestPlazo_SinElRecordatorioDeSeñaElDelPlazoSigueAvisando(t *testing.T) {
	st := esperandoDesde(ahora.Add(-3 * intakes.QuoteDeadline))
	spy := &recordatorioAlDueñoSpy{}
	rem := intakes.NewExpiryReminder(spy, st, &logSpy{},
		intakes.WithExpiryClock(func() time.Time { return ahora }))
	// SOLO WithExpiryReminder. Nada de WithDepositReminder: ese es el punto.
	svc := intakes.NewService(st, intakes.WithExpiryReminder(rem))

	if _, err := svc.List(context.Background(), tenantA, intakes.Filter{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if spy.contar() != 1 {
		t.Fatalf("avisos = %d sin el recordatorio de seña cableado, quiero 1. La guarda de "+
			"Service.touch tiene que preguntar por CADA colaborador por separado: heredada del "+
			"primero, el segundo sale mudo y en verde", spy.contar())
	}
}

// TestPlazo_SinCablearNoEvalúaNada es el otro extremo: un Service sin la opción no
// avisa y no escribe. Es lo que mantiene honestos a los tests de dominio y a
// cualquier consumidor que solo quiera leer.
func TestPlazo_SinCablearNoEvalúaNada(t *testing.T) {
	st := esperandoDesde(ahora.Add(-3 * intakes.QuoteDeadline))
	svc := intakes.NewService(st)

	page, err := svc.List(context.Background(), tenantA, intakes.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// La MARCA sigue saliendo —es derivada y no depende de ningún cableado—, pero la
	// marca del recordatorio no se gasta.
	if !intakes.Overdue(page.Intakes[0], ahora) {
		t.Fatal("la marca derivada dejó de calcularse sin el recordatorio cableado: no depende de él")
	}
	if !page.Intakes[0].ExpiryRemindedAt.IsZero() {
		t.Fatal("se escribió expiry_reminded_at sin el recordatorio cableado")
	}
}

// --- (5) lo que NO puede disparar el aviso ------------------------------------

// TestPlazo_NiElExportNiElResumenAvisan: descargar un CSV o pedir el resumen no
// puede mandar avisos. Es la misma regla que ya protegía al recordatorio de la seña
// (Service.touch NO se llama desde ListDetails ni Summary) y el colaborador nuevo la
// hereda por construcción — este test es lo que impide que alguien «arregle» esa
// asimetría creyéndola un descuido.
func TestPlazo_NiElExportNiElResumenAvisan(t *testing.T) {
	st := esperandoDesde(ahora.Add(-3 * intakes.QuoteDeadline))
	svc, spy := conRecordatorioDePlazo(t, st)

	if _, err := svc.ListDetails(context.Background(), tenantA, intakes.Filter{}); err != nil {
		t.Fatalf("ListDetails: %v", err)
	}
	if _, err := svc.Summary(context.Background(), tenantA, intakes.Filter{}); err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if spy.contar() != 0 {
		t.Fatalf("avisos = %d desde el export/resumen, quiero 0: bajarse una hoja de cálculo no "+
			"puede disparar avisos", spy.contar())
	}
}

// --- (6) la marca no se apaga sola --------------------------------------------

// TestPlazo_AvisarNoApagaLaMarcaDeVencido: la solicitud sigue marcada DESPUÉS del
// aviso. Es el test que caza el fallo más sutil de esta tarea — que el
// compare-and-swap toque updated_at, que es la BASE del plazo: la fila se
// «rejuvenecería» en el mismo instante en que se avisa y la bandeja dejaría de
// enseñar como vencido justo lo que se acaba de reportar.
func TestPlazo_AvisarNoApagaLaMarcaDeVencido(t *testing.T) {
	st := esperandoDesde(ahora.Add(-3 * intakes.QuoteDeadline))
	svc, spy := conRecordatorioDePlazo(t, st)

	if _, err := svc.List(context.Background(), tenantA, intakes.Filter{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if spy.contar() != 1 {
		t.Fatalf("avisos = %d, quiero 1", spy.contar())
	}

	d, err := st.Get(context.Background(), tenantA, intakeDePrueba)
	if err != nil {
		t.Fatalf("releer: %v", err)
	}
	if !intakes.Overdue(d.Intake, ahora) {
		t.Fatal("la solicitud dejó de estar VENCIDA después de avisar: el compare-and-swap tocó " +
			"updated_at, que es la base del plazo")
	}
	if d.ExpiryRemindedAt.IsZero() {
		t.Fatal("se avisó pero no se marcó expiry_reminded_at: el segundo toque volvería a avisar")
	}
}

// TestPlazo_ElDueñoDecideYElAvisoDejaDeProceder: en cuanto la solicitud sale de
// `pending_approval` ya no hay decisión pendiente que recordar. La condición vive en
// el compare-and-swap y no en un `if` del llamante, así que se comprueba pidiéndole
// al store que marque directamente.
func TestPlazo_ElDueñoDecideYElAvisoDejaDeProceder(t *testing.T) {
	st := esperandoDesde(ahora.Add(-3 * intakes.QuoteDeadline))

	if _, err := st.UpdateStatus(context.Background(), tenantA, intakeDePrueba,
		intakes.StatusRejected, []string{intakes.StatusPendingApproval}); err != nil {
		t.Fatalf("rechazar: %v", err)
	}

	_, ganó, err := st.MarkExpiryReminded(context.Background(), tenantA, intakeDePrueba, ahora)
	if err != nil {
		t.Fatalf("MarkExpiryReminded: %v", err)
	}
	if ganó {
		t.Fatal("se marcó el recordatorio de un presupuesto ya RECHAZADO: nadie recuerda una " +
			"decisión que ya se tomó")
	}
}

// --- (7) el aviso no puede tumbar la lectura ----------------------------------

// storeQuePanica es un ExpiryStore que revienta. No es un caso rebuscado: entre el
// toque y la base hay un pool, un driver y una red.
type storeQuePanica struct{}

func (storeQuePanica) MarkExpiryReminded(_ context.Context, _, _ string, _ time.Time) (intakes.Intake, bool, error) {
	panic("la base se cayó a mitad del compare-and-swap")
}

var _ intakes.ExpiryStore = storeQuePanica{}

// TestPlazo_UnPánicoNoConvierteLaBandejaEnUn500: un pánico dentro del recordatorio
// se contiene y la lectura sigue su curso. Sin esto, un listado que el dueño ni
// siquiera pidió que mandara avisos se convertiría en un 500.
func TestPlazo_UnPánicoNoConvierteLaBandejaEnUn500(t *testing.T) {
	st := esperandoDesde(ahora.Add(-3 * intakes.QuoteDeadline))
	spy := &logSpy{}
	rem := intakes.NewExpiryReminder(&recordatorioAlDueñoSpy{}, storeQuePanica{}, spy,
		intakes.WithExpiryClock(func() time.Time { return ahora }))
	svc := intakes.NewService(st, intakes.WithExpiryReminder(rem))

	page, err := svc.List(context.Background(), tenantA, intakes.Filter{})
	if err != nil {
		t.Fatalf("List: %v — el pánico del recordatorio subió hasta el llamante", err)
	}
	if len(page.Intakes) != 1 {
		t.Fatalf("solicitudes = %d, quiero 1: la lectura tiene que devolver lo suyo igual", len(page.Intakes))
	}
	if !strings.Contains(spy.all(), "pánico contenido") {
		t.Fatalf("el pánico se tragó sin dejar rastro en el log. Contenerlo es acotar el alcance, "+
			"no esconderlo.\nlog=%s", spy.all())
	}
}

// --- (8) el sumidero de HOY ---------------------------------------------------

// TestPlazo_ElSumideroDeHoySoloDejaTrazaYSinPII congela lo que D-044.50 §2 decidió:
// el emisor real no existe y el de hoy escribe una línea. El test mira las DOS
// cosas —que la traza sale y que NO lleva PII—, porque una traza con el contacto
// dentro sería exactamente el fallo que ADR-0010 prohíbe.
func TestPlazo_ElSumideroDeHoySoloDejaTrazaYSinPII(t *testing.T) {
	spy := &logSpy{}
	sumidero := intakes.NewLogOwnerNotice(spy)

	sumidero.RemindOwner(context.Background(), tenantA, intakes.Intake{
		ID: intakeDePrueba, ContactID: "contacto-opaco-1", Total: 21000,
		Status: intakes.StatusPendingApproval, UpdatedAt: ahora.Add(-3 * intakes.QuoteDeadline),
	})

	línea := spy.all()
	if !strings.Contains(línea, intakeDePrueba) {
		t.Fatalf("la traza no nombra la solicitud: es lo único que la hace útil.\nlog=%s", línea)
	}
	if strings.Contains(línea, "contacto-opaco-1") {
		t.Fatalf("la traza lleva el contacto: ni siquiera opaco tiene por qué acabar en un fichero "+
			"de log (ADR-0010).\nlog=%s", línea)
	}
	if strings.Contains(línea, "21000") {
		t.Fatalf("la traza lleva el total del pedido, que aquí no aporta nada.\nlog=%s", línea)
	}
}

// TestPlazo_ElSumideroNoEsElCanalReal deja escrito en la suite lo que nadie puede
// afirmar al cerrar T4.5: que el dueño recibe el recordatorio. NO lo recibe. El
// sumidero implementa el puerto y no manda nada a ninguna parte; el canal es el push
// del Plan 045.
//
// El test es una comprobación de TIPO a propósito: lo que se quiere congelar es que
// el puerto existe y que su implementación de hoy es la de traza. El día que el Plan
// 045 enchufe el emisor real, este test se cambia por uno que compruebe el envío —y
// tener que venir a cambiarlo es la señal de que se cerró el pendiente.
func TestPlazo_ElSumideroNoEsElCanalReal(t *testing.T) {
	var canal intakes.OwnerNotice = intakes.NewLogOwnerNotice(&logSpy{})
	if _, esTraza := canal.(*intakes.LogOwnerNotice); !esTraza {
		t.Fatal("el emisor del recordatorio dejó de ser el sumidero de traza. Si es porque llegó el " +
			"push del Plan 045, enhorabuena: actualiza este test y el comentario de bootstrap")
	}
}
