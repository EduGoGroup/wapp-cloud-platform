package intakes_test

// approve_test.go — LA ACCIÓN APROBAR (Plan 044 · Ola 4 · T4.3).
//
// Lo que se prueba aquí, y que es todo lo que la tarea promete:
//   · el cliente recibe UN mensaje, el del dueño, con la plantilla de seña adjunta;
//   · lo que se GUARDA en la revisión `approved` es byte a byte lo que se ENVIÓ;
//   · el aviso genérico del estado destino NO sale (D-044.49 §1, NoticeByCaller);
//   · el empuje al CRM lleva el revision_no REAL y el estado REAL;
//   · las precondiciones cortan ANTES de escribir nada.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// --- dobles -----------------------------------------------------------------

// empujeCRM es UN encolado al puente, tal como lo vio el puerto.
type empujeCRM struct {
	status     string
	revisionNo int
	items      int
	total      float64
}

// crmSpy retiene los empujes. Lo que se mira de él no es que empuje, sino CON QUÉ:
// el número de revisión y el estado son los dos campos que el candado del AST de
// crmpush vigila por haber estado clavados a `1` y a `"confirmed"`.
type crmSpy struct {
	mu      sync.Mutex
	empujes []empujeCRM
}

func (c *crmSpy) PushRevision(_ context.Context, _ string, d intakes.Detail, revisionNo int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.empujes = append(c.empujes, empujeCRM{
		status: d.Status, revisionNo: revisionNo, items: len(d.Items), total: d.Total,
	})
}

func (c *crmSpy) all() []empujeCRM {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]empujeCRM(nil), c.empujes...)
}

// escenaAprobar arma el escenario completo de una aprobación: la solicitud por
// aprobar con su línea, el notificador real —que es quien compone y entrega—, el
// espía del CRM y el espía del aviso genérico.
type escenaAprobar struct {
	svc    *intakes.Service
	store  *intakes.MemoryStore
	sender *stubSender
	crm    *crmSpy
	aviso  *avisoSpy
}

func nuevaEscenaAprobar(t *testing.T) *escenaAprobar {
	t.Helper()
	st := seedStore(t, intakes.StatusPendingApproval)
	sender := &stubSender{}
	crm := &crmSpy{}
	aviso := &avisoSpy{}
	svc := intakes.NewService(st,
		intakes.WithQuoteSender(newNotifier(sender, st, &logSpy{})),
		intakes.WithNotifier(aviso),
		intakes.WithCRMPusher(crm))
	return &escenaAprobar{svc: svc, store: st, sender: sender, crm: crm, aviso: aviso}
}

// laCotización es el texto que escribe el dueño. Lleva acentos, emoji, saltos de
// línea y espacios al final a propósito: «byte a byte» solo se demuestra con un
// texto que un recorte o una normalización estropearían.
const laCotización = "Hola Ambar 👋\nTu torta de chocolate: $18.000\nEntrega miércoles 22/07.  "

// --- el camino feliz --------------------------------------------------------

// TestApprove_LoQueSeGuardaEsLoQueSeEnvía es el criterio central de la tarea: la
// revisión `approved` tiene que poder citarse el día que el cliente diga «a mí me
// dijeron otra cosa», y para eso su rendered_text tiene que ser el mensaje que salió
// por el cable, no una aproximación.
func TestApprove_LoQueSeGuardaEsLoQueSeEnvía(t *testing.T) {
	e := nuevaEscenaAprobar(t)
	e.store.SetDepositTemplate(tenantA, "Para reservar, abona el 50% a la cuenta 123-4 (total {total}).", 3)

	detail, err := e.svc.Approve(context.Background(), tenantA, intakeDePrueba, laCotización)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}

	msgs := e.sender.messages()
	if len(msgs) != 1 {
		t.Fatalf("mensajes enviados = %d, quiero exactamente 1 (el del dueño)", len(msgs))
	}
	rev := últimaRevisión(t, detail.Revisions)
	if rev.RenderedText != msgs[0].text {
		t.Fatalf("la revisión NO guarda lo que se envió.\nguardado=%q\nenviado =%q", rev.RenderedText, msgs[0].text)
	}
	if !strings.HasPrefix(msgs[0].text, laCotización) {
		t.Fatalf("el texto del dueño se alteró antes de salir: %q", msgs[0].text)
	}
	if !strings.Contains(msgs[0].text, "cuenta 123-4") {
		t.Fatalf("la plantilla de seña del tenant no se adjuntó: %q", msgs[0].text)
	}
	if !strings.Contains(msgs[0].text, "$18000.00") {
		t.Fatalf("el marcador {total} no se rellenó: %q", msgs[0].text)
	}
	if msgs[0].sessionID != "sess-a" {
		t.Fatalf("salió por la sesión %q; tiene que salir por la de la solicitud", msgs[0].sessionID)
	}
	if rev.Kind != intakes.RevisionKindApproved || rev.CreatedBy != intakes.RevisionByOwner {
		t.Fatalf("revisión kind=%q created_by=%q; quiero approved/owner", rev.Kind, rev.CreatedBy)
	}
	if detail.Status != intakes.StatusConfirmed {
		t.Fatalf("status=%q, quiero confirmed (aprobar no es cobrar: NO deposit_requested)", detail.Status)
	}
}

// TestApprove_ElClienteRecibeUnSoloMensaje es D-044.49 §1 por el lado que se puede
// romper sin que nadie lo note: el aviso genérico del estado destino —«✅ Tu pedido
// quedó confirmado. Total $X»— repetiría el número que la cotización ya dijo, peor
// contado. Con NoticeByCaller la plataforma se calla en ESTA transición.
func TestApprove_ElClienteRecibeUnSoloMensaje(t *testing.T) {
	e := nuevaEscenaAprobar(t)

	if _, err := e.svc.Approve(context.Background(), tenantA, intakeDePrueba, laCotización); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	if got := e.aviso.count(); got != 0 {
		t.Fatalf("el aviso automático salió %d veces; con NoticeByCaller el cliente recibe SOLO el "+
			"mensaje del dueño", got)
	}
	if got := len(e.sender.messages()); got != 1 {
		t.Fatalf("mensajes al cliente = %d, quiero 1", got)
	}
}

// TestApprove_SinPlantillaDeSeñaSaleLaCotizaciónSola: la decisión de producto que ya
// gobierna el aviso de la seña (notifier.go) manda también aquí. Un tenant sin
// plantilla no manda un «te pedimos una seña» sin decir dónde pagarla — pero su
// cotización sale igual, entera y sin marcadores sueltos.
func TestApprove_SinPlantillaDeSeñaSaleLaCotizaciónSola(t *testing.T) {
	e := nuevaEscenaAprobar(t) // sin SetDepositTemplate

	detail, err := e.svc.Approve(context.Background(), tenantA, intakeDePrueba, laCotización)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}

	msgs := e.sender.messages()
	if len(msgs) != 1 || msgs[0].text != laCotización {
		t.Fatalf("sin plantilla de seña tiene que salir la cotización TAL CUAL; salió %q", msgs)
	}
	if got := últimaRevisión(t, detail.Revisions).RenderedText; got != laCotización {
		t.Fatalf("rendered_text=%q, quiero el texto del dueño tal cual", got)
	}
}

// TestApprove_EmpujaAlCRMConElRevisionNoRealYElEstadoReal mata de una vez los DOS
// literales que el candado del AST de crmpush vigila. La solicitud llega con una
// revisión previa, así que la de la aprobación es la 2: un `1` clavado dejaría al
// puente con el primer estado PARA SIEMPRE (UPSERT por intake_id+revision_no).
func TestApprove_EmpujaAlCRMConElRevisionNoRealYElEstadoReal(t *testing.T) {
	e := nuevaEscenaAprobar(t)
	sembrarRevisión(t, e.store, intakes.RevisionKindInterpreted, `{"version":1,"lines":[]}`)

	if _, err := e.svc.Approve(context.Background(), tenantA, intakeDePrueba, laCotización); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	empujes := e.crm.all()
	if len(empujes) != 1 {
		t.Fatalf("empujes al CRM = %d, quiero 1", len(empujes))
	}
	if empujes[0].revisionNo != 2 {
		t.Fatalf("revision_no empujado = %d, quiero 2 (la aprobación va detrás de la interpretación)",
			empujes[0].revisionNo)
	}
	if empujes[0].status != intakes.StatusConfirmed {
		t.Fatalf("lifecycle_status empujado = %q, quiero el estado REAL tras aprobar", empujes[0].status)
	}
	if empujes[0].items == 0 || empujes[0].total == 0 {
		t.Fatalf("el documento del CRM salió vacío: items=%d total=%v", empujes[0].items, empujes[0].total)
	}
}

// TestApprove_ElEnvíoQueFallaNoDeshaceLaAprobación: notificar no puede tumbar una
// escritura aplicada (notifier.go, regla 1). El dueño ve su 200 y el fallo queda en
// el log; lo que NO puede pasar es que la solicitud se quede sin aprobar porque el
// teléfono del cliente estaba apagado.
func TestApprove_ElEnvíoQueFallaNoDeshaceLaAprobación(t *testing.T) {
	e := nuevaEscenaAprobar(t)
	e.sender.err = errors.New("sesión offline")

	detail, err := e.svc.Approve(context.Background(), tenantA, intakeDePrueba, laCotización)
	if err != nil {
		t.Fatalf("Approve con el envío caído devolvió error: %v", err)
	}
	if detail.Status != intakes.StatusConfirmed {
		t.Fatalf("status=%q, quiero confirmed", detail.Status)
	}
	if len(e.store.RevisionesPersistidas(intakeDePrueba)) != 1 {
		t.Fatalf("la revisión approved tiene que estar escrita aunque el envío falle")
	}
}

// TestApprove_SiLaRevisiónNoSeEscribeNoSeMandaNada FIJA EL ORDEN, que es la decisión
// de diseño de esta tarea (ver la cabecera de approve.go).
//
// La revisión `approved` existe para poder citar lo que se le dijo al cliente el día
// que lo discuta. Con el envío ANTES de la escritura, un fallo al escribir dejaría
// una cotización dicha y no registrada —y el reintento del dueño mandaría una
// segunda—. Con el orden que se eligió, ese fallo deja la solicitud confirmada, sin
// rastro y SIN HABER HABLADO: se le devuelve un error al dueño que lo dice, y el
// cliente no recibe nada que no conste.
//
// Este test es lo único que impide que alguien "arregle" el orden sin darse cuenta:
// los dos órdenes pasan todos los demás tests.
func TestApprove_SiLaRevisiónNoSeEscribeNoSeMandaNada(t *testing.T) {
	st := seedStore(t, intakes.StatusPendingApproval)
	sender := &stubSender{}
	crm := &crmSpy{}
	svc := intakes.NewService(revisiónRota{MemoryStore: st},
		intakes.WithQuoteSender(newNotifier(sender, st, &logSpy{})),
		intakes.WithCRMPusher(crm))

	_, err := svc.Approve(context.Background(), tenantA, intakeDePrueba, laCotización)

	if err == nil {
		t.Fatal("una aprobación sin rastro no es una aprobación: tiene que devolver error")
	}
	if len(sender.messages()) != 0 {
		t.Fatalf("se le mandó al cliente una cotización que NO quedó registrada: %q", sender.messages())
	}
	if len(crm.all()) != 0 {
		t.Fatal("no se empuja al CRM una revisión que no existe")
	}
	// La consecuencia que sí se acepta, y que el error nombra: la solicitud QUEDÓ
	// confirmada. Está en la cabecera de approve.go y en el texto del error, no
	// escondida.
	if got := estadoDe(t, st); got != intakes.StatusConfirmed {
		t.Fatalf("status=%q; el fallo elegido deja la solicitud confirmada, y el error lo dice", got)
	}
	if !strings.Contains(err.Error(), "CONFIRMADA") {
		t.Fatalf("el error no le cuenta al dueño lo que quedó a medias: %v", err)
	}
}

// revisiónRota es el store al que le falla la escritura de la revisión y solo eso.
// Imita el fallo que de verdad puede ocurrir: la transición ya confirmó (transacción
// propia) y el INSERT de la revisión se cae después.
type revisiónRota struct{ *intakes.MemoryStore }

func (revisiónRota) InsertRevision(context.Context, intakes.Revision) (intakes.Revision, error) {
	return intakes.Revision{}, errors.New("se cayó la base al escribir la revisión")
}

// --- las precondiciones -----------------------------------------------------

// TestApprove_LíneasSinPrecioNoSeAprueban es la precondición de la tarea. El
// borrador del pipeline escribe `"unit_price": null` en la línea que el catálogo no
// reconoció (§7.4/§7.5), y aprobarla le cotizaría al cliente un renglón a cero.
//
// Se comprueba además que NO SE ESCRIBIÓ NADA: el rechazo va antes de la transición.
func TestApprove_LíneasSinPrecioNoSeAprueban(t *testing.T) {
	e := nuevaEscenaAprobar(t)
	sembrarRevisión(t, e.store, intakes.RevisionKindInterpreted, `{"version":1,"lines":[
		{"kind":"matched","sku":"torta-v1","label":"Torta 10-12 porciones","qty":1,"unit_price":18000},
		{"kind":"unmatched","label":"Torta vainilla 25-30 porciones","qty":1,"unit_price":null},
		{"kind":"shipping","sku":"_shipping","label":"Envío","qty":1,"unit_price":null}
	]}`)

	_, err := e.svc.Approve(context.Background(), tenantA, intakeDePrueba, laCotización)

	var pendientes *intakes.PendingPriceError
	if !errors.As(err, &pendientes) {
		t.Fatalf("quiero *PendingPriceError, dio %v", err)
	}
	if len(pendientes.Lines) != 2 {
		t.Fatalf("líneas pendientes = %+v, quiero 2 (la unmatched y el envío)", pendientes.Lines)
	}
	if pendientes.Lines[0].Index != 1 || pendientes.Lines[0].Label != "Torta vainilla 25-30 porciones" {
		t.Fatalf("la primera pendiente es %+v; el detalle tiene que decir CUÁL falta", pendientes.Lines[0])
	}
	if got := estadoDe(t, e.store); got != intakes.StatusPendingApproval {
		t.Fatalf("status=%q tras el rechazo, quiero pending_approval: no se puede haber escrito nada", got)
	}
	if len(e.sender.messages()) != 0 {
		t.Fatalf("un rechazo no puede haberle mandado nada al cliente")
	}
	if len(e.store.RevisionesPersistidas(intakeDePrueba)) != 1 {
		t.Fatalf("un rechazo no puede haber escrito una revisión")
	}
}

// TestApprove_LaCorrecciónDelDueñoResuelveLaPrecondición: la salida del 400 anterior
// es el `PUT …/items`, que deja una revisión `corrected` cuyo payload NO PUEDE tener
// líneas sin precio (su unit_price es float64, no nullable). Con esa revisión encima,
// la misma solicitud se aprueba.
func TestApprove_LaCorrecciónDelDueñoResuelveLaPrecondición(t *testing.T) {
	e := nuevaEscenaAprobar(t)
	sembrarRevisión(t, e.store, intakes.RevisionKindInterpreted, `{"version":1,"lines":[
		{"kind":"unmatched","label":"Torta vainilla","qty":1,"unit_price":null}
	]}`)
	sembrarRevisión(t, e.store, intakes.RevisionKindCorrected,
		`{"version":1,"total":18000,"items":[{"sku":"torta-v1","label":"Torta","qty":1,"unit_price":18000}]}`)

	if _, err := e.svc.Approve(context.Background(), tenantA, intakeDePrueba, laCotización); err != nil {
		t.Fatalf("con la corrección encima tiene que aprobarse; dio %v", err)
	}
}

// TestApprove_SinTextoNoSeAprueba: el dueño es el autor de lo que sale, y una
// aprobación muda confirmaría el pedido sin contarle el precio a nadie.
func TestApprove_SinTextoNoSeAprueba(t *testing.T) {
	for nombre, texto := range map[string]string{"vacío": "", "solo espacios": "   \n\t"} {
		t.Run(nombre, func(t *testing.T) {
			e := nuevaEscenaAprobar(t)
			_, err := e.svc.Approve(context.Background(), tenantA, intakeDePrueba, texto)
			if !errors.Is(err, intakes.ErrEmptyQuoteText) {
				t.Fatalf("quiero ErrEmptyQuoteText, dio %v", err)
			}
			if got := estadoDe(t, e.store); got != intakes.StatusPendingApproval {
				t.Fatalf("status=%q, no se puede haber escrito nada", got)
			}
		})
	}
}

// TestApprove_SoloDesdePendingApproval: aprobar es la acción sobre el PRESUPUESTO.
// Una solicitud `open` es un carrito vivo —el cliente todavía le está añadiendo
// líneas y ni siquiera tiene la línea de envío—, aunque la máquina de estados admita
// `open → confirmed` para el cierre del carrito numérico.
func TestApprove_SoloDesdePendingApproval(t *testing.T) {
	for _, estado := range []string{intakes.StatusOpen, intakes.StatusConfirmed, intakes.StatusNeedsInfo} {
		t.Run(estado, func(t *testing.T) {
			st := seedStore(t, estado)
			svc := intakes.NewService(st, intakes.WithQuoteSender(newNotifier(&stubSender{}, st, &logSpy{})))

			_, err := svc.Approve(context.Background(), tenantA, intakeDePrueba, laCotización)

			var no *intakes.NotApprovableError
			if !errors.As(err, &no) {
				t.Fatalf("desde %q quiero *NotApprovableError, dio %v", estado, err)
			}
			if no.Status != estado {
				t.Fatalf("el error dice %q y la solicitud está en %q", no.Status, estado)
			}
		})
	}
}

// TestApprove_SinLíneasNoHayNadaQueCotizar tapa el agujero que deja el pipeline: el
// borrador del 044 vive en la revisión y NO en `intake_items` (stages/draft.go), así
// que una solicitud recién interpretada tiene cero líneas. Sin esta guarda, aprobarla
// confirmaría un pedido de total 0 y le empujaría al CRM un documento vacío, en
// silencio.
func TestApprove_SinLíneasNoHayNadaQueCotizar(t *testing.T) {
	st := intakes.NewMemoryStore()
	st.Add(tenantA, intakes.Intake{
		ID: intakeDePrueba, ContactID: "contacto-opaco-1", SessionID: "sess-a",
		Status: intakes.StatusPendingApproval,
	}, intakes.Item{SKU: "_shipping", Label: "Envío", Qty: 1, UnitPrice: 2000})
	crm := &crmSpy{}
	sender := &stubSender{}
	svc := intakes.NewService(st,
		intakes.WithQuoteSender(newNotifier(sender, st, &logSpy{})),
		intakes.WithCRMPusher(crm))

	_, err := svc.Approve(context.Background(), tenantA, intakeDePrueba, laCotización)

	if !errors.Is(err, intakes.ErrEmptyQuote) {
		t.Fatalf("quiero ErrEmptyQuote (la línea de envío no es un pedido), dio %v", err)
	}
	if len(sender.messages()) != 0 || len(crm.all()) != 0 {
		t.Fatalf("un rechazo no manda ni encola nada")
	}
}

// TestApprove_SolicitudAjena: 404 opaco, nunca una respuesta que confirme que el id
// existe en otro tenant (INV-8).
func TestApprove_SolicitudAjena(t *testing.T) {
	e := nuevaEscenaAprobar(t)
	_, err := e.svc.Approve(context.Background(), "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", intakeDePrueba, laCotización)
	if !errors.Is(err, intakes.ErrNotFound) {
		t.Fatalf("quiero ErrNotFound, dio %v", err)
	}
}

// TestApprove_SinCanalNoAprueba: aprobar es «aprobar y responder». Un servicio sin
// QuoteSender cableado corta ANTES de tocar el estado, en vez de confirmar el pedido
// dejando al cliente sin enterarse.
func TestApprove_SinCanalNoAprueba(t *testing.T) {
	st := seedStore(t, intakes.StatusPendingApproval)
	svc := intakes.NewService(st) // sin WithQuoteSender

	_, err := svc.Approve(context.Background(), tenantA, intakeDePrueba, laCotización)

	if !errors.Is(err, intakes.ErrNoQuoteSender) {
		t.Fatalf("quiero ErrNoQuoteSender, dio %v", err)
	}
	if got := estadoDe(t, st); got != intakes.StatusPendingApproval {
		t.Fatalf("status=%q: sin canal no se puede haber transicionado nada", got)
	}
}

// --- la parte PURA: qué es una línea sin precio ------------------------------

// TestLinesWithoutPrice recorre las formas de payload que existen de verdad en la
// tabla. Es un test INTERNO y no de integración a propósito: la precondición entera
// de T4.3 se decide aquí, y un test que necesite DATABASE_URL se salta sin dar error
// (`--- SKIP` cuenta como rc=0).
func TestLinesWithoutPrice(t *testing.T) {
	casos := map[string]struct {
		payload string
		quiero  []intakes.PendingPriceLine
	}{
		"borrador con una línea sin precio": {
			payload: `{"version":1,"lines":[
				{"label":"Torta","qty":1,"unit_price":18000},
				{"label":"Tequeños","qty":1,"unit_price":null}]}`,
			quiero: []intakes.PendingPriceLine{{Index: 1, Label: "Tequeños"}},
		},
		"la clave AUSENTE también es sin precio": {
			// Un int de Go no distingue «no vino» de «vino 0», y aquí esa diferencia
			// separa «ponle precio» de «es un regalo».
			payload: `{"version":1,"lines":[{"label":"Sorpresa","qty":1}]}`,
			quiero:  []intakes.PendingPriceLine{{Index: 0, Label: "Sorpresa"}},
		},
		"el cero SÍ es un precio": {
			payload: `{"version":1,"lines":[{"label":"Regalo","qty":1,"unit_price":0}]}`,
			quiero:  nil,
		},
		"revisión corrected: su contrato usa items, no lines": {
			payload: `{"version":1,"total":9,"items":[{"sku":"HAMB","label":"Hamburguesa","qty":1,"unit_price":9}]}`,
			quiero:  nil,
		},
		"payload que no es un objeto": {payload: `[1,2,3]`, quiero: nil},
		"payload vacío":               {payload: ``, quiero: nil},
		"lines vacío":                 {payload: `{"version":1,"lines":[]}`, quiero: nil},
	}
	for nombre, c := range casos {
		t.Run(nombre, func(t *testing.T) {
			got := intakes.LinesWithoutPrice(json.RawMessage(c.payload))
			if len(got) != len(c.quiero) {
				t.Fatalf("got=%+v, quiero=%+v", got, c.quiero)
			}
			for i := range got {
				if got[i] != c.quiero[i] {
					t.Fatalf("línea %d: got=%+v, quiero=%+v", i, got[i], c.quiero[i])
				}
			}
		})
	}
}

// TestPendingPriceLines_MiraLaÚLTIMARevisión: el histórico es el rastro de cómo se
// llegó aquí, no lo que se vende. Una línea que nació sin precio y que el dueño
// precificó después está resuelta, y el orden en que el store devuelva la lista no
// puede cambiar esa respuesta.
func TestPendingPriceLines_MiraLaÚLTIMARevisión(t *testing.T) {
	revisiones := []intakes.Revision{
		{RevisionNo: 2, Payload: json.RawMessage(`{"lines":[{"label":"Torta","unit_price":18000}]}`)},
		{RevisionNo: 1, Payload: json.RawMessage(`{"lines":[{"label":"Torta","unit_price":null}]}`)},
	}
	if got := intakes.PendingPriceLines(revisiones); len(got) != 0 {
		t.Fatalf("got=%+v; la rev 2 ya tiene precio, aunque venga primera en la lista", got)
	}
	if got := intakes.PendingPriceLines(nil); len(got) != 0 {
		t.Fatalf("sin revisiones no hay nada pendiente; got=%+v", got)
	}
}

// --- helpers ----------------------------------------------------------------

// sembrarRevisión escribe una revisión sobre la solicitud de la escena por el MISMO
// camino que producción (InsertRevision numera), para que el revision_no que se
// afirma después sea el que la base habría asignado.
func sembrarRevisión(t *testing.T, st *intakes.MemoryStore, kind, payload string) {
	t.Helper()
	if _, err := st.InsertRevision(context.Background(), intakes.Revision{
		IntakeID: intakeDePrueba, Kind: kind, Payload: json.RawMessage(payload),
		CreatedBy: intakes.RevisionBySystem,
	}); err != nil {
		t.Fatalf("sembrar revisión %s: %v", kind, err)
	}
}

// últimaRevisión es la de número más alto, fallando el test si no hay ninguna: un
// `x, _ :=` que se quedara en el cero-valor convertiría «no se escribió la revisión»
// en «la revisión salió vacía», y el test pasaría por la razón equivocada.
func últimaRevisión(t *testing.T, revisiones []intakes.Revision) intakes.Revision {
	t.Helper()
	rev, ok := intakes.LastRevision(revisiones)
	if !ok {
		t.Fatal("la solicitud no tiene ni una revisión: la aprobación no dejó rastro")
	}
	return rev
}

// estadoDe lee el estado persistido de la solicitud de la escena.
func estadoDe(t *testing.T, st *intakes.MemoryStore) string {
	t.Helper()
	detail, err := st.Get(context.Background(), tenantA, intakeDePrueba)
	if err != nil {
		t.Fatalf("leer la solicitud: %v", err)
	}
	return detail.Status
}
