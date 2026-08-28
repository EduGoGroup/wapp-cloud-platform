package intakes_test

// metricas_test.go — LOS TRES EVENTOS QUE PUBLICA LA BANDEJA (Plan 044 · T5.2,
// design §10).
//
// Lo que se sostiene aquí, y es todo lo que la tarea promete:
//   · el payload de cada uno es EXACTAMENTE el de design §10 (claves y tipos);
//   · en ninguno entra una palabra del cliente ni del dueño — y el fixture SÍ mete
//     texto de los dos, o el barrido no probaría nada;
//   · sin emisor cableado las tres acciones siguen funcionando enteras;
//   · un emisor que FALLA tampoco las tumba: se avisa y se sigue.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// --- el doble del outbox ----------------------------------------------------

// medición es UNA publicación, tal como la vio el puerto.
type medición struct {
	tenantID  string
	contactID string
	name      string
	payload   map[string]any
}

// espíaDeEventos es un doble de intakes.PublicadorDeMetricas que retiene lo que se le
// pidió publicar y, si se le pone un `err`, lo rechaza todo.
//
// Con mutex porque el puerto no promete en qué goroutine se llama, igual que crmSpy.
type espíaDeEventos struct {
	mu     sync.Mutex
	err    error
	filas  []medición
	fallos int
}

func (e *espíaDeEventos) PublicarMetrica(_ context.Context, tenantID, contactID, name string, payload map[string]any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		e.fallos++
		return e.err
	}
	e.filas = append(e.filas, medición{tenantID: tenantID, contactID: contactID, name: name, payload: payload})
	return nil
}

// laFila devuelve la ÚNICA medición con ese `name`, fallando si hay otra cuenta.
// Buscar por nombre y no por índice es lo que hace que estos tests no dependan del
// orden en que una acción publica sus efectos.
func (e *espíaDeEventos) laFila(t *testing.T, name string) medición {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []medición
	nombres := make([]string, 0, len(e.filas))
	for _, ev := range e.filas {
		nombres = append(nombres, ev.name)
		if ev.name == name {
			out = append(out, ev)
		}
	}
	if len(out) != 1 {
		t.Fatalf("quiero UNA medición de %q y hay %d; las publicadas fueron %v", name, len(out), nombres)
	}
	return out[0]
}

func (e *espíaDeEventos) cuántas() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.filas)
}

// --- la escena --------------------------------------------------------------

// Los dos instantes del KPI de la aprobación. La diferencia son 1.900.000 ms
// EXACTOS, que es el literal que design §10 usa de ejemplo
// (`{"rev": 3, "elapsed_from_draft_ms": 1900000}`): 31 min 40 s del dueño mirando su
// bandeja antes de decidir.
var (
	instanteDelBorrador   = time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	instanteDeLaDecisión  = instanteDelBorrador.Add(1_900_000 * time.Millisecond)
	laNotaDelCliente      = "dejarlo en portería, timbre roto"
	laPersonalizaciónDeÉl = "sin cebolla, bien cocida"
)

// escenaMétrica es la bandeja del dueño con su telemetría cableada: una solicitud en
// `pending_approval` con DOS líneas del cliente —con etiqueta, sku y personalización
// escritas a mano— y una nota de pedido.
//
// 🔴 EL TEXTO DEL CLIENTE ESTÁ AQUÍ A PROPÓSITO. Los barridos anti-PII de abajo son
// asserts NEGATIVOS, y un assert negativo sobre un fixture que nunca metió el dato es
// decorado: pasa por la razón equivocada y parece cobertura.
type escenaMétrica struct {
	svc     *intakes.Service
	store   *intakes.MemoryStore
	eventos *espíaDeEventos
	sender  *stubSender
	log     *logSpy
}

func nuevaEscenaMétrica(t *testing.T, opts ...intakes.Option) *escenaMétrica {
	t.Helper()
	st := intakes.NewMemoryStore()
	// El reloj del STORE fija el `created_at` de las revisiones que se siembran, que
	// es el extremo INICIO de `elapsed_from_draft_ms`. Sin fijarlo, ese campo solo se
	// podría comprobar por rango — la clase de aserción que deja pasar un cero.
	st.SetClock(func() time.Time { return instanteDelBorrador })
	st.Add(tenantA, intakes.Intake{
		ID:           intakeDePrueba,
		ContactID:    "contacto-opaco-1",
		SessionID:    "sess-a",
		Status:       intakes.StatusPendingApproval,
		Total:        26000,
		CustomerNote: laNotaDelCliente,
		CreatedAt:    instanteDelBorrador,
		UpdatedAt:    instanteDelBorrador,
	},
		intakes.Item{SKU: "torta-v1", Label: "Torta 10-12 porciones", Qty: 1, UnitPrice: 18000},
		intakes.Item{SKU: "hamb-v1", Label: "Hamburguesa", Customization: laPersonalizaciónDeÉl, Qty: 2, UnitPrice: 4000},
	)

	eventos := &espíaDeEventos{}
	sender := &stubSender{}
	log := &logSpy{}
	base := make([]intakes.Option, 0, 3+len(opts))
	base = append(base,
		intakes.WithQuoteSender(newNotifier(sender, st, log)),
		intakes.WithMetrics(eventos, log),
		intakes.WithMetricsClock(func() time.Time { return instanteDeLaDecisión }),
	)
	return &escenaMétrica{
		svc:     intakes.NewService(st, append(base, opts...)...),
		store:   st,
		eventos: eventos,
		sender:  sender,
		log:     log,
	}
}

// sembrarBorrador deja la revisión `interpreted` del pipeline, que es el extremo
// INICIO del cronómetro de la aprobación.
func (e *escenaMétrica) sembrarBorrador(t *testing.T) {
	t.Helper()
	sembrarRevisión(t, e.store, intakes.RevisionKindInterpreted, `{"version":1,"lines":[]}`)
}

// lasDosLíneas son las líneas de cliente TAL COMO ESTÁN sembradas: el punto de
// partida de toda edición de estos tests.
func lasDosLíneas() []intakes.Item {
	return []intakes.Item{
		{SKU: "torta-v1", Label: "Torta 10-12 porciones", Qty: 1, UnitPrice: 18000},
		{SKU: "hamb-v1", Label: "Hamburguesa", Customization: laPersonalizaciónDeÉl, Qty: 2, UnitPrice: 4000},
	}
}

// ---------------------------------------------------------------------------
// LOS TRES GOLDEN DE DESIGN §10
// ---------------------------------------------------------------------------

// TestMétrica_LíneaCorregida_ContratoDeDesign10 fija el payload ENTERO de
// `intake_line_corrected` (`{"lines_corrected": 2, "lines_total": 4}` en el ejemplo).
//
// Se compara el mapa completo y no clave a clave: lo que hay que sostener no es «las
// dos están», es que no hay una tercera. Un evento de telemetría que gana campos en
// silencio acaba llevando algo que no debía.
func TestMétrica_LíneaCorregida_ContratoDeDesign10(t *testing.T) {
	e := nuevaEscenaMétrica(t)

	// El dueño corrige UNA de las dos: la hamburguesa pasa de 2 a 3 unidades.
	nuevas := lasDosLíneas()
	nuevas[1].Qty = 3
	if _, err := e.svc.ReplaceItems(context.Background(), tenantA, intakeDePrueba, nuevas, intakes.EditPlain); err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}

	ev := e.eventos.laFila(t, intakes.EventoLineaCorregida)
	if got, want := ev.payload, map[string]any{"lines_corrected": 1, "lines_total": 2}; !igualJSON(t, got, want) {
		t.Fatalf("payload=%v, quiero %v", got, want)
	}
	// El tenant y el contacto que viajan son los de la solicitud, y el contacto es el
	// OPACO. Con qué flujo se FIRMA la fila es cosa del adaptador y se prueba allí
	// (internal/intakes/telemetria).
	if ev.tenantID != tenantA || ev.contactID != "contacto-opaco-1" {
		t.Fatalf("la medición viaja con tenant=%q contacto=%q; quiero los de la solicitud",
			ev.tenantID, ev.contactID)
	}
}

// TestMétrica_Aprobado_ContratoDeDesign10 fija `intake_approved` con el literal
// EXACTO del ejemplo: `{"rev": 3, "elapsed_from_draft_ms": 1900000}`.
//
// La escena reproduce ese 3: el pipeline dejó la revisión 1 (`interpreted`), el dueño
// corrigió y salió la 2 (`corrected`), y la aprobación escribe la 3. Un `rev` clavado
// a 1 —que es como han nacido dos campos de este plan— pasaría un test que aprobara
// sobre una solicitud sin historia.
func TestMétrica_Aprobado_ContratoDeDesign10(t *testing.T) {
	e := nuevaEscenaMétrica(t)
	e.sembrarBorrador(t)
	sembrarRevisión(t, e.store, intakes.RevisionKindCorrected, `{"version":1,"lines":[]}`)

	if _, err := e.svc.Approve(context.Background(), tenantA, intakeDePrueba, "Tu pedido: $26.000"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	ev := e.eventos.laFila(t, intakes.EventoAprobado)
	want := map[string]any{"rev": 3, "elapsed_from_draft_ms": int64(1_900_000)}
	if !igualJSON(t, ev.payload, want) {
		t.Fatalf("payload=%v, quiero %v", ev.payload, want)
	}
}

// TestMétrica_InfoPedida_ContratoDeDesign10 fija `intake_info_requested`
// (`{"questions": 1}`).
func TestMétrica_InfoPedida_ContratoDeDesign10(t *testing.T) {
	e := nuevaEscenaMétrica(t)

	if _, err := e.svc.RequestInfo(context.Background(), tenantA, intakeDePrueba,
		"¿La torta la querés con dulce de leche o con chocolate?"); err != nil {
		t.Fatalf("RequestInfo: %v", err)
	}

	ev := e.eventos.laFila(t, intakes.EventoInfoPedida)
	if !igualJSON(t, ev.payload, map[string]any{"questions": 1}) {
		t.Fatalf("payload=%v, quiero {\"questions\":1}", ev.payload)
	}
}

// ---------------------------------------------------------------------------
// `elapsed_from_draft_ms` — LOS DOS CASOS QUE NO SON EL FELIZ
// ---------------------------------------------------------------------------

// TestMétrica_Aprobado_SinBorradorNoInventaUnNúmero: una solicitud que NO nació del
// pipeline —el cierre de un carrito la crea directa— no tiene borrador que
// cronometrar, y el único valor honesto es 0.
//
// Rellenar con el `created_at` de la cabecera mediría «desde que existe el pedido»,
// que es otra cosa con el mismo nombre, y el KPI no podría distinguir las dos.
//
// 🔴 Y EL REGISTRO ES DEBUG, NO WARN. Este caso es el curso NORMAL de todo pedido que
// viene del carrito: con Warn, cada aprobación sana dejaría una línea de alarma, y un
// log que grita por lo normal enseña a ignorarlo. La aserción mira el NIVEL a
// propósito — sin ella, «volvamos a Warn» pasaría sin que nada saltara.
func TestMétrica_Aprobado_SinBorradorNoInventaUnNúmero(t *testing.T) {
	e := nuevaEscenaMétrica(t) // sin sembrarBorrador: no hay revisión `interpreted`

	if _, err := e.svc.Approve(context.Background(), tenantA, intakeDePrueba, "Tu pedido: $26.000"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	ev := e.eventos.laFila(t, intakes.EventoAprobado)
	if got := ev.payload["elapsed_from_draft_ms"]; got != int64(0) {
		t.Fatalf("elapsed_from_draft_ms=%v; sin revisión interpretada tiene que ser 0", got)
	}
	if !strings.Contains(e.log.all(), "DEBUG intakes: la solicitud aprobada no nació del pipeline") {
		t.Fatalf("el 0 salió MUDO, o salió con otro nivel. log=%s", e.log.all())
	}
	// Se mira ESTA línea y no «¿hay algún WARN?»: el log es compartido con el
	// notificador, que avisa por su cuenta de que el tenant no tiene plantilla de seña.
	// Un assert más ancho fallaría por el vecino y no diría nada de lo que protege.
	if strings.Contains(e.log.all(), "WARN intakes: la solicitud aprobada") {
		t.Fatalf("una aprobación de un pedido del CARRITO no puede dejar un WARN: es el curso "+
			"normal, no una anomalía. log=%s", e.log.all())
	}
}

// TestMétrica_SinEmisor_NiSIQUIERACalculaElKPI es el arreglo de la guarda que llegaba
// tarde.
//
// Go evalúa los ARGUMENTOS antes de entrar en la función, así que el payload de la
// aprobación —que recorre las revisiones para cronometrar el borrador— se construía
// entero aunque `publicarMetrica` fuera a salir por su guarda. El efecto visible era
// un servicio SIN telemetría cableada que igualmente calculaba el KPI y dejaba rastro
// en el log, justo lo contrario de lo que promete WithMetrics.
//
// CÓMO SE OBSERVA algo que por definición no deja resultado: se cablea el LOG pero no
// el emisor (WithMetrics admite las dos cosas por separado) y se usa la solicitud SIN
// revisión `interpreted` — el único camino del cálculo que deja rastro. Con la guarda
// tarde, ese rastro aparece aunque no haya nada que publicar; con la guarda en su
// sitio, no se llega a mirar una sola revisión.
//
// 🔴 SEMBRAR EL BORRADOR AQUÍ ROMPERÍA EL TEST SIN QUE SE NOTARA: el cálculo saldría
// bien, no loguearía nada, y el assert pasaría con y sin la guarda. Lo comprobé: con
// el fixture sembrado, la mutación que quita la guarda salía VERDE.
func TestMétrica_SinEmisor_NiSIQUIERACalculaElKPI(t *testing.T) {
	log := &logSpy{}
	e := nuevaEscenaMétrica(t, intakes.WithMetrics(nil, log))

	if _, err := e.svc.Approve(context.Background(), tenantA, intakeDePrueba, "Tu pedido: $26.000"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	if e.eventos.cuántas() != 0 {
		t.Fatalf("se publicó algo con el emisor descableado: %d filas", e.eventos.cuántas())
	}
	if strings.Contains(log.all(), "elapsed_from_draft_ms") || strings.Contains(log.all(), "no nació del pipeline") {
		t.Fatalf("el servicio calculó el KPI que no iba a publicar. log=%s", log.all())
	}
}

// TestMétrica_Aprobado_NuncaPublicaUnNegativo: si el reloj de la base va por delante
// del proceso, el resultado se recorta a 0 y se avisa. Un tiempo negativo en un panel
// no se lee como «relojes desajustados», se lee como un bug.
func TestMétrica_Aprobado_NuncaPublicaUnNegativo(t *testing.T) {
	e := nuevaEscenaMétrica(t, intakes.WithMetricsClock(
		func() time.Time { return instanteDelBorrador.Add(-90 * time.Second) }))
	e.sembrarBorrador(t)

	if _, err := e.svc.Approve(context.Background(), tenantA, intakeDePrueba, "Tu pedido: $26.000"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	ev := e.eventos.laFila(t, intakes.EventoAprobado)
	if got := ev.payload["elapsed_from_draft_ms"]; got != int64(0) {
		t.Fatalf("elapsed_from_draft_ms=%v; un negativo NO se publica", got)
	}
	if !strings.Contains(e.log.all(), "NEGATIVO") {
		t.Fatalf("el recorte salió MUDO. log=%s", e.log.all())
	}
}

// TestMétrica_Aprobado_MideDesdeElPRIMERBorrador: una solicitud RE-ANALIZADA tiene
// DOS revisiones `interpreted`, y el cronómetro arranca en la primera.
//
// Medir desde la última diría que el dueño aprobó en dos minutos un pedido que llevaba
// media hora en su bandeja — y el KPI del plan («del mensaje a la decisión») quedaría
// sistemáticamente subestimado justo en los casos que más tardaron, que son los que
// hay que ver.
func TestMétrica_Aprobado_MideDesdeElPRIMERBorrador(t *testing.T) {
	e := nuevaEscenaMétrica(t)
	e.sembrarBorrador(t) // revisión 1, en instanteDelBorrador

	// El re-análisis escribe una SEGUNDA `interpreted`, media hora más tarde.
	elReanalisis := instanteDelBorrador.Add(30 * time.Minute)
	e.store.SetClock(func() time.Time { return elReanalisis })
	sembrarRevisión(t, e.store, intakes.RevisionKindInterpreted, `{"version":1,"lines":[]}`)

	if _, err := e.svc.Approve(context.Background(), tenantA, intakeDePrueba, "Tu pedido: $26.000"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	ev := e.eventos.laFila(t, intakes.EventoAprobado)
	if got := ev.payload["elapsed_from_draft_ms"]; got != int64(1_900_000) {
		t.Fatalf("elapsed_from_draft_ms=%v; quiero 1900000 (desde la PRIMERA interpreted, "+
			"no desde la del re-análisis, que daría 100000)", got)
	}
}

// ---------------------------------------------------------------------------
// EL RECUENTO DE LA CORRECCIÓN — LOS TRES ACTOS DE REQ-36
// ---------------------------------------------------------------------------

// TestMétrica_LíneaCorregida_LosTresActos: corregir, quitar y añadir cuentan los tres,
// y ninguno se sale del denominador.
//
// 🔴 EL CASO QUE JUSTIFICA LA DEFINICIÓN es «quitar»: con `len(items)` de denominador
// —lo primero que uno escribe— borrar una de dos daría `1/1`, y borrar las dos daría
// `2/0`. Un porcentaje por encima de 100 en un panel no se lee como «esta métrica está
// mal definida», se lee como un dato roto.
func TestMétrica_LíneaCorregida_LosTresActos(t *testing.T) {
	casos := []struct {
		nombre     string
		nuevas     func() []intakes.Item
		corregidas int
		total      int
	}{
		{
			nombre:     "CORREGIR una de dos (la cantidad)",
			nuevas:     func() []intakes.Item { l := lasDosLíneas(); l[1].Qty = 3; return l },
			corregidas: 1, total: 2,
		},
		{
			nombre:     "QUITAR una de dos",
			nuevas:     func() []intakes.Item { return lasDosLíneas()[:1] },
			corregidas: 1, total: 2,
		},
		{
			nombre: "AÑADIR una a las dos",
			nuevas: func() []intakes.Item {
				return append(lasDosLíneas(), intakes.Item{SKU: "gaseosa-v1", Label: "Gaseosa 1,5 L", Qty: 1, UnitPrice: 2000})
			},
			corregidas: 1, total: 3,
		},
		{
			nombre:     "NO TOCAR NADA: cero corregidas, y el evento sale igual",
			nuevas:     lasDosLíneas,
			corregidas: 0, total: 2,
		},
		{
			nombre: "CORREGIR SOLO LA PERSONALIZACIÓN: el sku no cambia y la línea SÍ",
			nuevas: func() []intakes.Item {
				l := lasDosLíneas()
				l[1].Customization = "con cebolla"
				return l
			},
			corregidas: 1, total: 2,
		},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			e := nuevaEscenaMétrica(t)
			if _, err := e.svc.ReplaceItems(context.Background(), tenantA, intakeDePrueba, c.nuevas(), intakes.EditPlain); err != nil {
				t.Fatalf("ReplaceItems: %v", err)
			}
			ev := e.eventos.laFila(t, intakes.EventoLineaCorregida)
			if !igualJSON(t, ev.payload, map[string]any{"lines_corrected": c.corregidas, "lines_total": c.total}) {
				t.Fatalf("payload=%v; quiero corrected=%d total=%d", ev.payload, c.corregidas, c.total)
			}
		})
	}
}

// TestMétrica_LíneaCorregida_ElEnvíoNoCuenta: la línea de la PLATAFORMA no entra en el
// KPI por ninguno de los dos lados.
//
// La materializa el propio dominio al entrar a `pending_approval` (D-041.11) y
// sobrevive intacta a toda edición: contarla metería en «% de líneas corregidas» una
// línea que el LLM nunca interpretó y que el dueño no puede tocar desde esta puerta.
func TestMétrica_LíneaCorregida_ElEnvíoNoCuenta(t *testing.T) {
	e := nuevaEscenaMétrica(t)
	e.store.SetShippingZones(tenantA, intakes.ShippingZone{Label: "Centro", Price: 1500})
	// Se vuelve a entrar a pending_approval por el camino de producción, que es lo que
	// materializa la línea de envío. Sin este paso el test no probaría nada: estaría
	// filtrando una línea que no existe.
	if _, err := e.svc.SetStatus(context.Background(), tenantA, intakeDePrueba, intakes.StatusConfirmed, intakes.NoticeByCaller); err != nil {
		t.Fatalf("a confirmed: %v", err)
	}
	if _, err := e.svc.SetStatus(context.Background(), tenantA, intakeDePrueba, intakes.StatusPendingApproval, intakes.NoticeByCaller); err != nil {
		t.Fatalf("de vuelta a pending_approval: %v", err)
	}
	antes, err := e.svc.Get(context.Background(), tenantA, intakeDePrueba)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var envíos int
	for _, it := range antes.Items {
		if strings.HasPrefix(it.SKU, intakes.ReservedSKUPrefix) {
			envíos++
		}
	}
	if envíos != 1 {
		t.Fatalf("el fixture no materializó la línea de envío (%d): el filtro no estaría filtrando nada", envíos)
	}

	nuevas := lasDosLíneas()
	nuevas[1].Qty = 3
	if _, err := e.svc.ReplaceItems(context.Background(), tenantA, intakeDePrueba, nuevas, intakes.EditPlain); err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}

	ev := e.eventos.laFila(t, intakes.EventoLineaCorregida)
	if !igualJSON(t, ev.payload, map[string]any{"lines_corrected": 1, "lines_total": 2}) {
		t.Fatalf("payload=%v; el envío se coló en el recuento (serían 3 líneas)", ev.payload)
	}
}

// ---------------------------------------------------------------------------
// CERO TEXTO LIBRE — LOS TRES EVENTOS, CON EL FIXTURE CARGADO DE TEXTO
// ---------------------------------------------------------------------------

// TestMétrica_NingúnPayloadLlevaTextoDeNadie barre el JSON YA SERIALIZADO de los tres
// eventos buscando lo que sí entró en el sistema durante la escena: la etiqueta de los
// artículos, sus skus, la personalización del cliente, su nota de pedido, la
// cotización que escribió el dueño y la pregunta que le mandó.
//
// 🔴 SE BARRE EL JSON ENTERO Y NO LOS CAMPOS UNO A UNO: lo que hay que garantizar no
// es «este campo está limpio», es que NO HAY DÓNDE se haya colado. Un test que mirara
// claves conocidas se quedaría ciego el día que alguien añada una tercera.
//
// 🔴 Y EL CONTROL VA PRIMERO: se comprueba que el texto perseguido de verdad viajó
// —está en el mensaje que salió y en la revisión escrita—, porque un assert negativo
// sobre un dato que nunca entró pasa por la razón equivocada.
func TestMétrica_NingúnPayloadLlevaTextoDeNadie(t *testing.T) {
	const laCotizaciónDelDueño = "Ambar, tu torta de chocolate sale $18.000 y las hamburguesas $8.000"
	const laPreguntaDelDueño = "¿La torta la querés de vainilla o de chocolate?"

	e := nuevaEscenaMétrica(t)
	e.sembrarBorrador(t)

	// (1) las tres acciones, en el orden en que una bandeja real las hace.
	nuevas := lasDosLíneas()
	nuevas[1].Qty = 3
	if _, err := e.svc.ReplaceItems(context.Background(), tenantA, intakeDePrueba, nuevas, intakes.EditPlain); err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}
	if _, err := e.svc.RequestInfo(context.Background(), tenantA, intakeDePrueba, laPreguntaDelDueño); err != nil {
		t.Fatalf("RequestInfo: %v", err)
	}
	if _, err := e.svc.SetStatus(context.Background(), tenantA, intakeDePrueba, intakes.StatusPendingApproval, intakes.NoticeByCaller); err != nil {
		t.Fatalf("de vuelta a pending_approval: %v", err)
	}
	if _, err := e.svc.Approve(context.Background(), tenantA, intakeDePrueba, laCotizaciónDelDueño); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	// (2) EL CONTROL: el texto perseguido SÍ existe en la escena.
	enviado := e.sender.messages()
	if len(enviado) == 0 {
		t.Fatal("no salió ni un mensaje: el barrido de abajo no probaría nada")
	}
	var todoLoEnviado strings.Builder
	for _, m := range enviado {
		todoLoEnviado.WriteString(m.text)
	}
	for _, debe := range []string{laCotizaciónDelDueño, laPreguntaDelDueño} {
		if !strings.Contains(todoLoEnviado.String(), debe) {
			t.Fatalf("el fixture no llegó a mandar %q: el assert negativo sería VACUO", debe)
		}
	}

	// (3) la invariante, sobre los tres payloads.
	prohibidos := []string{
		laCotizaciónDelDueño, laPreguntaDelDueño, laNotaDelCliente, laPersonalizaciónDeÉl,
		"Torta 10-12 porciones", "Hamburguesa", "torta-v1", "hamb-v1",
		"portería", "cebolla", "chocolate", "vainilla",
	}
	for _, name := range []string{intakes.EventoLineaCorregida, intakes.EventoInfoPedida, intakes.EventoAprobado} {
		ev := e.eventos.laFila(t, name)
		crudo, err := json.Marshal(ev.payload)
		if err != nil {
			t.Fatalf("serializar el payload de %s: %v", name, err)
		}
		for _, prohibido := range prohibidos {
			if strings.Contains(strings.ToLower(string(crudo)), strings.ToLower(prohibido)) {
				t.Errorf("🔴 %s lleva %q: flow_events es una tabla EN CLARO y ahí no entra ni el "+
					"literal ni el catálogo (ADR-0034). payload=%s", name, prohibido, crudo)
			}
		}
		// Y las claves son EXACTAMENTE las del contrato: ni una de más.
		if !mismasClaves(ev.payload, clavesEsperadas[name]) {
			t.Errorf("%s publica las claves %v; design §10 fija %v", name, clavesDe(ev.payload), clavesEsperadas[name])
		}
		// El contacto es el opaco, y ninguno de los tres lo enriquece.
		if strings.ContainsAny(ev.contactID, "@+") {
			t.Errorf("%s lleva un contact_id que parece un JID o un teléfono: %q", name, ev.contactID)
		}
	}
}

// clavesEsperadas es design §10 escrito una sola vez, para que el barrido de arriba
// no tenga que repetirlo por evento.
var clavesEsperadas = map[string][]string{
	intakes.EventoLineaCorregida: {"lines_corrected", "lines_total"},
	intakes.EventoAprobado:       {"rev", "elapsed_from_draft_ms"},
	intakes.EventoInfoPedida:     {"questions"},
}

// ---------------------------------------------------------------------------
// EL EMISOR QUE NO ESTÁ Y EL QUE FALLA
// ---------------------------------------------------------------------------

// TestMétrica_SinEmisorCableado_LasTresAccionesSiguenEnteras: sin WithMetrics el
// servicio funciona igual y no publica nada, que es lo mismo que ya prometen
// WithNotifier y WithCRMPusher.
//
// Importa porque el cable puede faltar de verdad: un despliegue con el arranque a
// medias, o cualquiera de las decenas de tests de dominio que construyen el Service
// con lo justo. Ninguna acción del dueño puede depender de la telemetría.
func TestMétrica_SinEmisorCableado_LasTresAccionesSiguenEnteras(t *testing.T) {
	st := intakes.NewMemoryStore()
	st.SetClock(func() time.Time { return instanteDelBorrador })
	st.Add(tenantA, intakes.Intake{
		ID: intakeDePrueba, ContactID: "contacto-opaco-1", SessionID: "sess-a",
		Status: intakes.StatusPendingApproval, Total: 18000,
		CreatedAt: instanteDelBorrador, UpdatedAt: instanteDelBorrador,
	}, intakes.Item{SKU: "torta-v1", Label: "Torta 10-12 porciones", Qty: 1, UnitPrice: 18000})
	// SIN intakes.WithMetrics, a propósito.
	svc := intakes.NewService(st, intakes.WithQuoteSender(newNotifier(&stubSender{}, st, &logSpy{})))

	nuevas := []intakes.Item{{SKU: "torta-v1", Label: "Torta 10-12 porciones", Qty: 2, UnitPrice: 18000}}
	if _, err := svc.ReplaceItems(context.Background(), tenantA, intakeDePrueba, nuevas, intakes.EditPlain); err != nil {
		t.Fatalf("ReplaceItems sin emisor: %v", err)
	}
	if _, err := svc.RequestInfo(context.Background(), tenantA, intakeDePrueba, "¿Para cuándo la querés?"); err != nil {
		t.Fatalf("RequestInfo sin emisor: %v", err)
	}
	if _, err := svc.SetStatus(context.Background(), tenantA, intakeDePrueba, intakes.StatusPendingApproval, intakes.NoticeByCaller); err != nil {
		t.Fatalf("de vuelta a pending_approval: %v", err)
	}
	detalle, err := svc.Approve(context.Background(), tenantA, intakeDePrueba, "Tu pedido: $36.000")
	if err != nil {
		t.Fatalf("Approve sin emisor: %v", err)
	}
	if detalle.Status != intakes.StatusConfirmed {
		t.Fatalf("la aprobación no se aplicó: status=%q", detalle.Status)
	}
}

// TestMétrica_ElEmisorQueFallaNoTumbaLaAcción: BEST-EFFORT, como el notificador y el
// empuje al CRM y por lo mismo.
//
// Aprobar un presupuesto NO puede fallar porque una fila de telemetría no se
// escribiera: el pedido ya está confirmado y el cliente ya recibió su cotización.
// Devolverle un 500 al dueño le haría reintentar contra un 422 y acabar creyendo que
// no se aplicó.
func TestMétrica_ElEmisorQueFallaNoTumbaLaAcción(t *testing.T) {
	e := nuevaEscenaMétrica(t)
	e.sembrarBorrador(t)
	e.eventos.err = errors.New("la base no responde")

	detalle, err := e.svc.Approve(context.Background(), tenantA, intakeDePrueba, "Tu pedido: $26.000")
	if err != nil {
		t.Fatalf("Approve tumbó por la telemetría: %v", err)
	}
	if detalle.Status != intakes.StatusConfirmed {
		t.Fatalf("la aprobación no se aplicó: status=%q", detalle.Status)
	}
	if len(e.sender.messages()) != 1 {
		t.Fatalf("el cliente no recibió su cotización: %d mensajes", len(e.sender.messages()))
	}
	if e.eventos.cuántas() != 0 {
		t.Fatal("el doble dice que escribió algo: el test no está probando el fallo")
	}
	// Y el fallo NO es mudo: sin esta línea sería un agujero silencioso.
	if !strings.Contains(e.log.all(), "no se pudo publicar la métrica") {
		t.Fatalf("el fallo de la telemetría no dejó rastro. log=%s", e.log.all())
	}
}

// ---------------------------------------------------------------------------
// utilidades del fichero
// ---------------------------------------------------------------------------

// igualJSON compara dos payloads POR SU JSON y no por los tipos de Go: lo que viaja a
// `flow_events.payload` es JSONB, así que un `int` y un `int64` con el mismo valor son
// la MISMA fila — y exigir el tipo de Go convertiría este golden en un test del
// compilador. Lo que sí distingue es un número de una cadena, que es lo que importa.
func igualJSON(t *testing.T, a, b map[string]any) bool {
	t.Helper()
	ja, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("serializar %v: %v", a, err)
	}
	jb, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("serializar %v: %v", b, err)
	}
	var ma, mb map[string]any
	if err := json.Unmarshal(ja, &ma); err != nil {
		t.Fatalf("releer %s: %v", ja, err)
	}
	if err := json.Unmarshal(jb, &mb); err != nil {
		t.Fatalf("releer %s: %v", jb, err)
	}
	if len(ma) != len(mb) {
		return false
	}
	for k, v := range ma {
		if mb[k] != v {
			return false
		}
	}
	return true
}

func clavesDe(payload map[string]any) []string {
	out := make([]string, 0, len(payload))
	for k := range payload {
		out = append(out, k)
	}
	return out
}

func mismasClaves(payload map[string]any, quiero []string) bool {
	if len(payload) != len(quiero) {
		return false
	}
	for _, k := range quiero {
		if _, hay := payload[k]; !hay {
			return false
		}
	}
	return true
}
