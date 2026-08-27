package intakes_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// El instante fijo de estos tests. Todo lo demás se cuelga de él: la seña vence
// ANTES y el toque ocurre AQUÍ. Sin reloj inyectable habría que dormir tres días.
var ahora = time.Date(2026, 8, 6, 15, 0, 0, 0, time.UTC)

// plantillaDeSeña es la del tenant: lleva los tres marcadores para que cualquier
// test pueda comprobar qué se rellenó y qué no.
const plantillaDeSeña = "Abona {total} a la cuenta 001-2 antes del {fecha_limite} ({plazo} días)."

// señaEnCurso siembra una solicitud con la seña pedida y la fecha límite dada, más
// la plantilla del tenant. `vencida` decide de qué lado del reloj cae.
func señaEnCurso(t *testing.T, dueAt time.Time) *intakes.MemoryStore {
	t.Helper()
	st := intakes.NewMemoryStore()
	st.SetClock(func() time.Time { return ahora })
	st.SetDepositTemplate(tenantA, plantillaDeSeña, 3)
	st.Add(tenantA, intakes.Intake{
		ID:           intakeDePrueba,
		ContactID:    "contacto-opaco-1",
		SessionID:    "sess-negocio",
		Status:       intakes.StatusDepositRequested,
		Total:        18000,
		CreatedAt:    ahora.AddDate(0, 0, -10),
		UpdatedAt:    ahora.AddDate(0, 0, -10),
		DepositDueAt: dueAt,
	})
	return st
}

// conRecordatorio arma el servicio con el recordatorio cableado y devuelve también
// el sender, que es donde se cuentan los mensajes que llegarían a un teléfono.
func conRecordatorio(t *testing.T, st *intakes.MemoryStore) (*intakes.Service, *stubSender, *logSpy) {
	t.Helper()
	sender := &stubSender{}
	spy := &logSpy{}
	rem := intakes.NewDepositReminder(newNotifier(sender, st, spy), st,
		intakes.WithReminderClock(func() time.Time { return ahora }))
	return intakes.NewService(st, intakes.WithDepositReminder(rem)), sender, spy
}

// tocarListado hace lo que hace el dueño al abrir su bandeja.
func tocarListado(t *testing.T, svc *intakes.Service) {
	t.Helper()
	if _, err := svc.List(context.Background(), tenantA, intakes.Filter{}); err != nil {
		t.Fatalf("List: %v", err)
	}
}

// --- el ciclo: pedir la seña fija el plazo -----------------------------------

// TestSeña_PedirlaFijaElPlazoYLoCuentaEnElMensaje: la transición a
// deposit_requested escribe deposit_due_at (now + deposit_due_days) y el texto que
// sale hacia el cliente lleva ESA fecha. Las dos mitades van juntas a propósito: si
// la fecha se fijara después del envío, el cliente recibiría un plazo que no es el
// que la BD guarda.
func TestSeña_PedirlaFijaElPlazoYLoCuentaEnElMensaje(t *testing.T) {
	st := intakes.NewMemoryStore()
	st.SetClock(func() time.Time { return ahora })
	st.SetDepositTemplate(tenantA, plantillaDeSeña, 5)
	st.Add(tenantA, intakes.Intake{
		ID: intakeDePrueba, ContactID: "contacto-opaco-1", SessionID: "sess-negocio",
		Status: intakes.StatusConfirmed, Total: 18000,
		CreatedAt: ahora, UpdatedAt: ahora,
	})
	sender := &stubSender{}
	svc := intakes.NewService(st, intakes.WithNotifier(newNotifier(sender, st, &logSpy{})))

	updated, err := svc.SetStatus(context.Background(), tenantA, intakeDePrueba, intakes.StatusDepositRequested, intakes.NoticeToClient)
	if err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	quiero := ahora.AddDate(0, 0, 5)
	if !updated.DepositDueAt.Equal(quiero) {
		t.Fatalf("deposit_due_at = %v, quiero %v (now + deposit_due_days)", updated.DepositDueAt, quiero)
	}
	msgs := sender.messages()
	if len(msgs) != 1 {
		t.Fatalf("envíos = %d, quiero 1", len(msgs))
	}
	if got, want := msgs[0].text, "Abona $18000.00 a la cuenta 001-2 antes del 11/08/2026 (5 días)."; got != want {
		t.Fatalf("texto = %q, quiero %q", got, want)
	}
}

// TestSeña_SinFechaElMarcadorNoSeInventa: si por lo que sea la solicitud llega sin
// fecha límite, {fecha_limite} se queda SIN sustituir en vez de rellenarse con la
// fecha cero. Un mensaje que enseña el marcador delata el fallo; uno que dice
// «01/01/0001» le miente al cliente y nadie se entera.
func TestSeña_SinFechaElMarcadorNoSeInventa(t *testing.T) {
	st := intakes.NewMemoryStore()
	st.SetDepositTemplate(tenantA, plantillaDeSeña, 3)
	sender := &stubSender{}
	n := newNotifier(sender, st, &logSpy{})

	n.NotifyStatus(context.Background(), tenantA, intakeEn(intakes.StatusDepositRequested), intakes.StatusConfirmed)

	msgs := sender.messages()
	if len(msgs) != 1 {
		t.Fatalf("envíos = %d, quiero 1", len(msgs))
	}
	if !strings.Contains(msgs[0].text, "{fecha_limite}") {
		t.Fatalf("texto = %q, quiero el marcador intacto (sin fecha no se inventa una)", msgs[0].text)
	}
	if strings.Contains(msgs[0].text, "0001") {
		t.Fatalf("texto = %q: se coló la fecha cero", msgs[0].text)
	}
}

// --- la regla dura: UN recordatorio ------------------------------------------

// TestRecordatorio_UnSoloRecordatorioPorMuchosQueSeanLosToques es EL test del
// criterio de T4.4: con la fecha límite en el pasado, el PRIMER toque manda el
// recordatorio y los CINCO siguientes no mandan nada. Lo que se cuenta es lo que
// llegaría a un teléfono.
func TestRecordatorio_UnSoloRecordatorioPorMuchosQueSeanLosToques(t *testing.T) {
	st := señaEnCurso(t, ahora.Add(-time.Hour))
	svc, sender, _ := conRecordatorio(t, st)

	const toques = 6
	for range toques {
		tocarListado(t, svc)
	}

	msgs := sender.messages()
	if len(msgs) != 1 {
		t.Fatalf("envíos = %d tras %d toques, quiero exactamente 1", len(msgs), toques)
	}
	if !strings.Contains(msgs[0].text, "esperando la seña") {
		t.Fatalf("texto = %q, no es el recordatorio enlatado", msgs[0].text)
	}
	// El recordatorio NO es un texto suelto: arrastra la plantilla del tenant con sus
	// datos de pago, porque un «te recordamos la seña» sin decir dónde pagarla obliga
	// al cliente a rebuscar el mensaje anterior.
	if !strings.Contains(msgs[0].text, "cuenta 001-2") {
		t.Fatalf("texto = %q, no lleva los datos de pago del tenant", msgs[0].text)
	}
	if msgs[0].sessionID != "sess-negocio" {
		t.Fatalf("session_id = %q, quiero la de la solicitud", msgs[0].sessionID)
	}
}

// TestRecordatorio_ToquesSimultáneosMandanUnoSolo: dos pestañas del dueño y un
// mensaje del cliente que LEYERON LO MISMO y evalúan a la vez. Es la carrera de
// verdad, y por eso los ocho toques parten de la MISMA página ya leída: si la
// garantía fuera el pre-filtro en memoria —que mira la fila que trae el llamante—
// los ocho la verían sin recordar y los ocho mandarían. Lo que la sostiene es el
// compare-and-swap del store. Corre con -race en el gate.
func TestRecordatorio_ToquesSimultáneosMandanUnoSolo(t *testing.T) {
	st := señaEnCurso(t, ahora.Add(-time.Hour))
	sender := &stubSender{}
	rem := intakes.NewDepositReminder(newNotifier(sender, st, &logSpy{}), st,
		intakes.WithReminderClock(func() time.Time { return ahora }))

	page, err := intakes.NewService(st).List(context.Background(), tenantA, intakes.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	var arranque, wg sync.WaitGroup
	arranque.Add(1)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			arranque.Wait() // todos salen del mismo sitio: máxima superposición
			rem.Remind(context.Background(), tenantA, page.Intakes)
		}()
	}
	arranque.Done()
	wg.Wait()

	if got := len(sender.messages()); got != 1 {
		t.Fatalf("envíos = %d con ocho toques a la vez sobre la misma lectura, quiero exactamente 1", got)
	}
}

// TestRecordatorio_SiYaPagóNoSeLeRecuerda: entre el vencimiento y el toque, el dueño
// marcó la seña recibida. Recordarle a alguien que pague lo que ya pagó es peor que
// no recordarle nada, y la condición vive en el compare-and-swap (el estado dejó de
// casar), no en una comprobación que alguien pueda saltarse.
func TestRecordatorio_SiYaPagóNoSeLeRecuerda(t *testing.T) {
	st := señaEnCurso(t, ahora.Add(-time.Hour))
	svc, sender, _ := conRecordatorio(t, st)

	if _, err := svc.SetStatus(context.Background(), tenantA, intakeDePrueba, intakes.StatusDepositPaid, intakes.NoticeToClient); err != nil {
		t.Fatalf("marcar la seña recibida: %v", err)
	}
	antes := len(sender.messages()) // el aviso de cambio de estado, que sí corresponde
	tocarListado(t, svc)
	tocarListado(t, svc)

	if got := len(sender.messages()) - antes; got != 0 {
		t.Fatalf("recordatorios = %d tras pagar, quiero 0", got)
	}
}

// TestRecordatorio_SinVencerNoRecuerda: la fecha límite es mañana. Tocar la
// solicitud no adelanta nada — el plazo es del cliente, no del dueño que mira.
func TestRecordatorio_SinVencerNoRecuerda(t *testing.T) {
	st := señaEnCurso(t, ahora.Add(24*time.Hour))
	svc, sender, _ := conRecordatorio(t, st)

	tocarListado(t, svc)

	if got := len(sender.messages()); got != 0 {
		t.Fatalf("envíos = %d con la seña sin vencer, quiero 0", got)
	}
}

// TestRecordatorio_SinPlantillaDelTenantNoMandaNiGastaLaMarca: la misma decisión de
// producto de T4.2 heredada gratis —sin datos de pago no hay nada útil que decir—,
// con una consecuencia que sí es propia de T4.4: la marca NO se gasta. Si se gastara,
// el tenant que configura su plantilla mañana descubriría que sus clientes de hoy
// perdieron el recordatorio para siempre, en silencio.
func TestRecordatorio_SinPlantillaDelTenantNoMandaNiGastaLaMarca(t *testing.T) {
	st := señaEnCurso(t, ahora.Add(-time.Hour))
	st.SetDepositTemplate(tenantA, "", 3) // el DEFAULT de la columna
	svc, sender, spy := conRecordatorio(t, st)

	tocarListado(t, svc)

	if got := len(sender.messages()); got != 0 {
		t.Fatalf("envíos = %d sin plantilla, quiero 0", got)
	}
	if !strings.Contains(spy.all(), "deposit_template") {
		t.Fatalf("el silencio no quedó registrado con su causa; log:\n%s", spy.all())
	}

	// El tenant configura por fin su plantilla: el recordatorio sigue disponible.
	st.SetDepositTemplate(tenantA, plantillaDeSeña, 3)
	tocarListado(t, svc)
	if got := len(sender.messages()); got != 1 {
		t.Fatalf("envíos = %d tras configurar la plantilla, quiero 1: la marca no podía estar gastada", got)
	}
}

// --- los tres toques ---------------------------------------------------------

// TestRecordatorio_ElDetalleTambiénEsUnToque: abrir LA solicitud (no la bandeja)
// también evalúa. Es el toque más específico que existe y el que antes lo nota.
func TestRecordatorio_ElDetalleTambiénEsUnToque(t *testing.T) {
	st := señaEnCurso(t, ahora.Add(-time.Hour))
	svc, sender, _ := conRecordatorio(t, st)

	if _, err := svc.Get(context.Background(), tenantA, intakeDePrueba); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := svc.Get(context.Background(), tenantA, intakeDePrueba); err != nil {
		t.Fatalf("segundo Get: %v", err)
	}

	if got := len(sender.messages()); got != 1 {
		t.Fatalf("envíos = %d tras dos detalles, quiero exactamente 1", got)
	}
}

// TestRecordatorio_ElMensajeDelClienteEsUnToque: el tercer camino (RemindContact),
// que es el que consume el motor de flujos cuando el contacto vuelve a escribir. No
// trae ninguna solicitud consigo —el motor solo sabe quién habló—, así que las busca
// por contacto.
func TestRecordatorio_ElMensajeDelClienteEsUnToque(t *testing.T) {
	st := señaEnCurso(t, ahora.Add(-time.Hour))
	sender := &stubSender{}
	rem := intakes.NewDepositReminder(newNotifier(sender, st, &logSpy{}), st,
		intakes.WithReminderClock(func() time.Time { return ahora }))

	rem.RemindContact(context.Background(), tenantA, "contacto-opaco-1")
	rem.RemindContact(context.Background(), tenantA, "contacto-opaco-1")

	if got := len(sender.messages()); got != 1 {
		t.Fatalf("envíos = %d tras dos mensajes del cliente, quiero exactamente 1", got)
	}
}

// TestRecordatorio_OtroContactoNoRecibeLoAjeno: la consulta por contacto no puede
// alcanzar la seña de otro. Es la misma frontera que el tenant, un nivel más abajo.
func TestRecordatorio_OtroContactoNoRecibeLoAjeno(t *testing.T) {
	st := señaEnCurso(t, ahora.Add(-time.Hour))
	sender := &stubSender{}
	rem := intakes.NewDepositReminder(newNotifier(sender, st, &logSpy{}), st,
		intakes.WithReminderClock(func() time.Time { return ahora }))

	rem.RemindContact(context.Background(), tenantA, "contacto-opaco-OTRO")

	if got := len(sender.messages()); got != 0 {
		t.Fatalf("envíos = %d, quiero 0: la seña es de otro contacto", got)
	}
}

// --- lo que no puede romperse -----------------------------------------------

// TestRecordatorio_SinCablearLasLecturasSiguenPuras: sin la opción, List y Get son
// exactamente lo que eran antes de T4.4. Es lo que hace que un test de dominio no le
// haga sonar el teléfono a nadie por accidente.
func TestRecordatorio_SinCablearLasLecturasSiguenPuras(t *testing.T) {
	st := señaEnCurso(t, ahora.Add(-time.Hour))
	svc := intakes.NewService(st)

	if _, err := svc.List(context.Background(), tenantA, intakes.Filter{}); err != nil {
		t.Fatalf("List sin recordatorio: %v", err)
	}
	if _, err := svc.Get(context.Background(), tenantA, intakeDePrueba); err != nil {
		t.Fatalf("Get sin recordatorio: %v", err)
	}

	detalle, err := svc.Get(context.Background(), tenantA, intakeDePrueba)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !detalle.DepositRemindedAt.IsZero() {
		t.Fatal("sin recordatorio cableado nadie puede haber marcado la solicitud")
	}
}

// TestRecordatorio_SesiónOfflineNoTumbaElListado: el teléfono del cliente está
// apagado (o el Edge caído). El listado del dueño devuelve su página igual: una
// consola que se cae porque un WhatsApp no salió sería inservible.
func TestRecordatorio_SesiónOfflineNoTumbaElListado(t *testing.T) {
	st := señaEnCurso(t, ahora.Add(-time.Hour))
	sender := &stubSender{err: &offlineError{cmdID: "cmd-sena-offline"}}
	spy := &logSpy{}
	rem := intakes.NewDepositReminder(newNotifier(sender, st, spy), st,
		intakes.WithReminderClock(func() time.Time { return ahora }))
	svc := intakes.NewService(st, intakes.WithDepositReminder(rem))

	page, err := svc.List(context.Background(), tenantA, intakes.Filter{})
	if err != nil {
		t.Fatalf("el listado no puede fallar porque el envío falle: %v", err)
	}
	if len(page.Intakes) != 1 {
		t.Fatalf("solicitudes = %d, quiero 1", len(page.Intakes))
	}
	if !strings.Contains(spy.all(), "cmd-sena-offline") {
		t.Fatalf("el fallo no quedó registrado con su command_id; log:\n%s", spy.all())
	}
	// La marca SÍ se gastó: es el error elegido (ver la cabecera de deposit.go). Un
	// segundo toque no reintenta, porque reintentar es el camino al goteo.
	detalle, err := svc.Get(context.Background(), tenantA, intakeDePrueba)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detalle.DepositRemindedAt.IsZero() {
		t.Fatal("la marca tiene que quedar puesta aunque el envío fallara")
	}
}

// TestRecordatorio_ADemediasNoRevienta: un recordatorio mal cableado no recuerda,
// pero tampoco se lleva por delante la lectura que lo invocó.
func TestRecordatorio_ADemediasNoRevienta(t *testing.T) {
	var nilo *intakes.DepositReminder
	nilo.Remind(context.Background(), tenantA, []intakes.Intake{intakeEn(intakes.StatusDepositRequested)})
	nilo.RemindContact(context.Background(), tenantA, "contacto-opaco-1")

	incompleto := intakes.NewDepositReminder(nil, nil)
	incompleto.Remind(context.Background(), tenantA, []intakes.Intake{intakeEn(intakes.StatusDepositRequested)})
	incompleto.RemindContact(context.Background(), tenantA, "contacto-opaco-1")
}

// TestRecordatorio_NuncaLogueaElDestino: el recordatorio abre la MISMA puerta que el
// notificador y hereda su regla — el número del contacto se usa y se suelta (ADR-0007
// / INV-04). Se barren los dos caminos que emiten log.
func TestRecordatorio_NuncaLogueaElDestino(t *testing.T) {
	casos := []struct {
		nombre string
		toque  func(*intakes.DepositReminder, *intakes.Service)
	}{
		{"listado", func(_ *intakes.DepositReminder, svc *intakes.Service) {
			if _, err := svc.List(context.Background(), tenantA, intakes.Filter{}); err != nil {
				t.Errorf("List: %v", err)
			}
		}},
		{"mensaje entrante", func(rem *intakes.DepositReminder, _ *intakes.Service) {
			rem.RemindContact(context.Background(), tenantA, "contacto-opaco-1")
		}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			st := señaEnCurso(t, ahora.Add(-time.Hour))
			spy := &logSpy{}
			rem := intakes.NewDepositReminder(newNotifier(&stubSender{}, st, spy), st,
				intakes.WithReminderClock(func() time.Time { return ahora }))
			c.toque(rem, intakes.NewService(st, intakes.WithDepositReminder(rem)))

			if strings.Contains(spy.all(), destinoDePrueba) {
				t.Fatalf("el destino (PII) se filtró al log:\n%s", spy.all())
			}
		})
	}
}
