package intakes_test

// requestinfo_test.go — PEDIR MÁS INFORMACIÓN (Plan 044 · Ola 4 · T4.4, D-044.49 §2).
//
// Lo que se prueba aquí:
//   · el cliente recibe UN mensaje y es la PREGUNTA DEL DUEÑO, byte a byte;
//   · el aviso genérico de `needs_info` —«Nos falta un dato… Te escribimos enseguida
//     por aquí»— NO sale por este camino, y SÍ sigue saliendo por el del 041;
//   · sin pregunta no se toca nada («jamás sale sola»);
//   · esta puerta no escribe revisión y, por tanto, no empuja al CRM.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// laPregunta es lo que escribe el dueño. Lleva acentos, emoji, dos renglones y un
// espacio final a propósito: «byte a byte» solo se demuestra con un texto que un
// recorte o una normalización estropearían (mismo criterio que laCotización).
const laPregunta = "Hola 👋 ¿la torta la querías de 15 porciones o de 20?\n¿Y para qué día? "

// escenaPedirInfo arma la solicitud por aprobar con el notificador REAL —el que
// compone y entrega—, el espía del aviso automático y el del CRM.
type escenaPedirInfo struct {
	svc    *intakes.Service
	store  *intakes.MemoryStore
	sender *stubSender
	crm    *crmSpy
	aviso  *avisoSpy
}

func nuevaEscenaPedirInfo(t *testing.T, status string) *escenaPedirInfo {
	t.Helper()
	st := seedStore(t, status)
	sender := &stubSender{}
	crm := &crmSpy{}
	aviso := &avisoSpy{}
	svc := intakes.NewService(st,
		intakes.WithQuoteSender(newNotifier(sender, st, &logSpy{})),
		intakes.WithNotifier(aviso),
		intakes.WithCRMPusher(crm))
	return &escenaPedirInfo{svc: svc, store: st, sender: sender, crm: crm, aviso: aviso}
}

// --- el camino feliz --------------------------------------------------------

// TestRequestInfo_ElClienteRecibeUnSoloMensajeYEsLaPregunta es D-044.49 §2 entero: el
// genérico de `needs_info` es LITERALMENTE el anuncio del mensaje siguiente, así que
// mandarlo pegado a la pregunta le cuenta al cliente que le vamos a escribir y acto
// seguido le escribimos.
func TestRequestInfo_ElClienteRecibeUnSoloMensajeYEsLaPregunta(t *testing.T) {
	e := nuevaEscenaPedirInfo(t, intakes.StatusPendingApproval)

	detail, err := e.svc.RequestInfo(context.Background(), tenantA, intakeDePrueba, laPregunta)
	if err != nil {
		t.Fatalf("RequestInfo: %v", err)
	}

	msgs := e.sender.messages()
	if len(msgs) != 1 {
		t.Fatalf("mensajes enviados = %d, quiero exactamente 1 (la pregunta del dueño)", len(msgs))
	}
	if msgs[0].text != laPregunta {
		t.Fatalf("la pregunta se alteró antes de salir.\nenviado=%q\nescrito =%q", msgs[0].text, laPregunta)
	}
	if msgs[0].sessionID != "sess-a" {
		t.Fatalf("salió por la sesión %q; tiene que salir por la de la solicitud", msgs[0].sessionID)
	}
	if got := e.aviso.count(); got != 0 {
		t.Fatalf("el aviso automático salió %d veces; con NoticeByCaller el cliente recibe SOLO la "+
			"pregunta del dueño", got)
	}
	if detail.Status != intakes.StatusNeedsInfo {
		t.Fatalf("status=%q, quiero needs_info", detail.Status)
	}
	if got := estadoDe(t, e.store); got != intakes.StatusNeedsInfo {
		t.Fatalf("status persistido=%q, quiero needs_info", got)
	}
}

// TestRequestInfo_NoLeAdjuntaLaPlantillaDeSeña: SendQuote compone con la plantilla de
// seña del tenant y SendQuestion no, y esa diferencia es el motivo de que sean dos
// métodos. Adjuntarle instrucciones de pago a una pregunta sería pedirle la seña a
// quien todavía no sabe qué va a costar.
func TestRequestInfo_NoLeAdjuntaLaPlantillaDeSeña(t *testing.T) {
	e := nuevaEscenaPedirInfo(t, intakes.StatusPendingApproval)
	e.store.SetDepositTemplate(tenantA, "Para reservar, abona el 50% a la cuenta 123-4.", 3)

	if _, err := e.svc.RequestInfo(context.Background(), tenantA, intakeDePrueba, laPregunta); err != nil {
		t.Fatalf("RequestInfo: %v", err)
	}

	msgs := e.sender.messages()
	if len(msgs) != 1 {
		t.Fatalf("mensajes enviados = %d, quiero 1", len(msgs))
	}
	if strings.Contains(msgs[0].text, "cuenta 123-4") {
		t.Fatalf("la pregunta salió con la plantilla de seña pegada: %q", msgs[0].text)
	}
}

// TestRequestInfo_ElAvisoAutomáticoDeNeedsInfoSIGUEVivo es la mitad que D-044.49
// marca como «lo primero que se construye»: suprimir el aviso en ESTE camino no puede
// apagarlo para el `<select>` de estado del 041, donde es lo ÚNICO que el cliente
// recibe. Vaciar statusTemplates sería una regresión del 041.
//
// 🔴 CABLEA EL NOTIFICADOR REAL Y MIRA EL MENSAJE, no un espía que cuente llamadas, y
// esa diferencia es el test entero: una mutación lo demostró. Con avisoSpy —que
// satisface StatusNotifier sin consultar las plantillas— borrar la entrada
// `needs_info` de statusTemplates dejaba este test EN VERDE, porque SetStatus llama al
// notificador igual y es el notificador quien decide que no hay nada que decir. Lo que
// hay que afirmar es que al cliente le LLEGA algo.
//
// No se afirma el LITERAL del texto a propósito: el copy del aviso es del 041 y puede
// cambiar sin que esta promesa cambie. Lo que se fija es que sale, y por la sesión de
// la solicitud.
func TestRequestInfo_ElAvisoAutomáticoDeNeedsInfoSIGUEVivo(t *testing.T) {
	st := seedStore(t, intakes.StatusPendingApproval)
	sender := &stubSender{}
	svc := intakes.NewService(st, intakes.WithNotifier(newNotifier(sender, st, &logSpy{})))

	if _, err := svc.SetStatus(context.Background(), tenantA, intakeDePrueba,
		intakes.StatusNeedsInfo, intakes.NoticeToClient); err != nil {
		t.Fatalf("SetStatus por el camino del 041: %v", err)
	}

	msgs := sender.messages()
	if len(msgs) != 1 {
		t.Fatalf("mensajes al cliente = %d por el `<select>` del 041, quiero 1: T4.4 apaga el genérico "+
			"en SU camino, no en el de todos — sin plantilla de `needs_info` el dueño mueve el estado "+
			"a mano y el cliente no se entera de nada", len(msgs))
	}
	if msgs[0].text == "" {
		t.Fatal("el aviso salió vacío")
	}
	if msgs[0].sessionID != "sess-a" {
		t.Fatalf("el aviso salió por la sesión %q, quiero la de la solicitud", msgs[0].sessionID)
	}
}

// --- lo que NO hace ---------------------------------------------------------

// TestRequestInfo_NoEscribeRevisiónNiEmpujaAlCRM. Una revisión retrata el
// PRESUPUESTO, y preguntar no cambia ni una línea ni el total; sin revisión nueva no
// hay nada que empujarle al puente, que recibe revisiones con su `revision_no` y no
// estados sueltos.
func TestRequestInfo_NoEscribeRevisiónNiEmpujaAlCRM(t *testing.T) {
	e := nuevaEscenaPedirInfo(t, intakes.StatusPendingApproval)
	sembrarRevisión(t, e.store, intakes.RevisionKindInterpreted, `{"version":1,"lines":[]}`)

	detail, err := e.svc.RequestInfo(context.Background(), tenantA, intakeDePrueba, laPregunta)
	if err != nil {
		t.Fatalf("RequestInfo: %v", err)
	}

	if len(detail.Revisions) != 1 {
		t.Fatalf("revisiones=%d, quiero 1: pedir información no escribe revisión", len(detail.Revisions))
	}
	después, err := e.store.Get(context.Background(), tenantA, intakeDePrueba)
	if err != nil {
		t.Fatalf("releer la solicitud: %v", err)
	}
	if len(después.Revisions) != 1 {
		t.Fatalf("revisiones persistidas=%d, quiero 1", len(después.Revisions))
	}
	if empujes := e.crm.all(); len(empujes) != 0 {
		t.Fatalf("empujes al CRM=%d, quiero 0: no nació ninguna revisión que empujar", len(empujes))
	}
}

// --- las precondiciones -----------------------------------------------------

// TestRequestInfo_SinPreguntaNoTocaNada: «jamás sale sola» (criterio explícito de
// T4.4). El blanco cuenta como ausencia, y el rechazo ocurre ANTES de escribir: la
// solicitud sigue por aprobar y no salió ningún mensaje.
func TestRequestInfo_SinPreguntaNoTocaNada(t *testing.T) {
	for nombre, pregunta := range map[string]string{"vacía": "", "en blanco": "  \n\t "} {
		t.Run(nombre, func(t *testing.T) {
			e := nuevaEscenaPedirInfo(t, intakes.StatusPendingApproval)

			_, err := e.svc.RequestInfo(context.Background(), tenantA, intakeDePrueba, pregunta)

			if !errors.Is(err, intakes.ErrEmptyQuestion) {
				t.Fatalf("err=%v, quiero ErrEmptyQuestion", err)
			}
			if got := len(e.sender.messages()); got != 0 {
				t.Fatalf("salieron %d mensajes con una pregunta vacía", got)
			}
			if got := estadoDe(t, e.store); got != intakes.StatusPendingApproval {
				t.Fatalf("status=%q; una petición rechazada no puede mover la solicitud", got)
			}
		})
	}
}

// TestRequestInfo_SoloDesdePendingApproval: a `needs_info` solo se llega desde el
// presupuesto por aprobar, y eso lo dice la máquina de estados —esta puerta no la
// duplica—. El 422 que sale trae los destinos legales, que es lo que el dueño necesita
// para no adivinar.
func TestRequestInfo_SoloDesdePendingApproval(t *testing.T) {
	for _, estado := range []string{intakes.StatusConfirmed, intakes.StatusNeedsInfo, intakes.StatusSettled} {
		t.Run(estado, func(t *testing.T) {
			e := nuevaEscenaPedirInfo(t, estado)

			_, err := e.svc.RequestInfo(context.Background(), tenantA, intakeDePrueba, laPregunta)

			var transición *intakes.TransitionError
			if !errors.As(err, &transición) {
				t.Fatalf("err=%v, quiero *TransitionError", err)
			}
			if transición.From != estado || transición.To != intakes.StatusNeedsInfo {
				t.Fatalf("el error dice %q→%q y la solicitud está en %q", transición.From, transición.To, estado)
			}
			if got := len(e.sender.messages()); got != 0 {
				t.Fatalf("salieron %d mensajes sobre una solicitud que no admitía la transición", got)
			}
			if got := estadoDe(t, e.store); got != estado {
				t.Fatalf("status=%q, quiero %q intacto", got, estado)
			}
		})
	}
}

// TestRequestInfo_404OtroTenant: una solicitud ajena no existe (INV-8). Nunca un 403,
// que confirmaría que existe.
func TestRequestInfo_404OtroTenant(t *testing.T) {
	e := nuevaEscenaPedirInfo(t, intakes.StatusPendingApproval)

	_, err := e.svc.RequestInfo(context.Background(), tenantB, intakeDePrueba, laPregunta)

	if !errors.Is(err, intakes.ErrNotFound) {
		t.Fatalf("err=%v, quiero ErrNotFound", err)
	}
	if got := len(e.sender.messages()); got != 0 {
		t.Fatalf("salieron %d mensajes por una solicitud de otro tenant", got)
	}
}

// TestRequestInfo_SinCanalNoTocaNada: un servicio sin QuoteSender no puede preguntar,
// y corta ANTES de mover el estado. Sin esta guarda, la solicitud quedaría en
// `needs_info` esperando la respuesta a una pregunta que nunca salió.
func TestRequestInfo_SinCanalNoTocaNada(t *testing.T) {
	st := seedStore(t, intakes.StatusPendingApproval)
	svc := intakes.NewService(st) // sin WithQuoteSender

	_, err := svc.RequestInfo(context.Background(), tenantA, intakeDePrueba, laPregunta)

	if !errors.Is(err, intakes.ErrNoQuoteSender) {
		t.Fatalf("err=%v, quiero ErrNoQuoteSender", err)
	}
	if got := estadoDe(t, st); got != intakes.StatusPendingApproval {
		t.Fatalf("status=%q; sin canal no se mueve nada", got)
	}
}
