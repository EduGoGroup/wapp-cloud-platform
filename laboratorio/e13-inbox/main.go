//go:build laboratorio

// e13-inbox es el escalón E13 del laboratorio (Plan 058 · Bloque D): LA BANDEJA, SIN LLM.
//
// Parte de un borrador `pending_approval` que deja el pipeline REAL (el camino de E11 sin la
// ventana, con -frame script por defecto) y recorre, con el servicio REAL de solicitudes
// (intakes.NewService + WithNotifier/WithQuoteSender sobre el MISMO Notifier, como el arranque),
// lo que la dueña puede hacer con él: intentar aprobar sin precio, corregir líneas, pedir
// información, volver a la revisión, aprobar con su texto, rechazar, descartar y exportar.
//
//   - REAL: intakes.Service y su máquina de estados, el Notifier (la voz de la dueña y los avisos
//     de la plataforma), la telemetría, el export HTTP (apipublica.MountIntakeReports con su JWT y
//     su gate de feature), y todo el pipeline que fabrica el borrador.
//
//   - DOBLE: el Sender (runtimehelpertest: apunta lo que el Edge habría mandado), los almacenes en
//     memoria, los derechos, el reloj y la inferencia del borrador.
//
//   - COSTURA: common.IntakeTable, para que el borrador y la bandeja vean la MISMA fila.
//
//     GOTOOLCHAIN=go1.26.5 GOWORK=off go run -tags laboratorio ./laboratorio/e13-inbox
//     … -frame hybrid -script-stages p2     (el borrador con P3 y P4 de Gemma; la bandeja no cambia)
package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

const (
	sessionA = "lab-session-e13"
	phoneAna = "56 9 0000 0013"
	phoneBea = "56 9 0000 0031"
	phoneCar = "56 9 0000 0113"
)

// depositTemplate es la plantilla de la seña del tenant (tenant_settings.deposit_template): la
// escribe la dueña en la consola y QuoteText la adjunta a su cotización al aprobar.
const depositTemplate = "Para reservar la fecha te pedimos una seña del 50 % en los próximos {plazo} días. El total del pedido es {total}."

var errUnexpected = errors.New("e13: el montaje no se comportó como se esperaba")

// lab es el montaje del escalón.
type lab struct {
	n        *common.Narrator
	ctx      context.Context
	box      *common.Inbox
	spy      *common.SpyFrame
	sends    int
	evs      int
	noTpl    bool
	carEvent string
}

func main() {
	flags := common.RegisterFlags(nil)
	noTemplate := flag.Bool("no-template", false, "el tenant sin plantilla de seña: la cotización sale sola")
	flag.Parse()
	if err := run(flags, *noTemplate); err != nil {
		os.Exit(1)
	}
}

func run(flags *common.Flags, noTemplate bool) error {
	n := common.NewNarrator(nil)
	n.Title("E13 · La bandeja, sin LLM",
		"Un borrador pending_approval y todo lo que la dueña puede hacer con él, por el servicio real de solicitudes.",
		"Frame (solo para FABRICAR el borrador; la bandeja no llama al modelo): "+flags.Describe())
	l := &lab{n: n, ctx: context.Background(), noTpl: noTemplate}
	if err := l.setup(flags); err != nil {
		return err
	}
	ana, err := l.draft()
	if err != nil {
		return err
	}
	l.approveTooSoon(ana)
	if err := l.correct(ana); err != nil {
		return err
	}
	if err := l.requestInfo(ana); err != nil {
		return err
	}
	if err := l.backToReview(ana); err != nil {
		return err
	}
	if err := l.approve(ana); err != nil {
		return err
	}
	bea, car, err := l.otherIntakes()
	if err != nil {
		return err
	}
	l.discard(ana, bea, car)
	if err := l.export(); err != nil {
		return err
	}
	l.trail(ana)
	if h, ok := l.spy.Inner().(*common.HybridFrame); ok {
		n.Note("-frame hybrid · quién contestó al fabricar los borradores: %s.", h.Tally())
	}
	return nil
}

func (l *lab) setup(flags *common.Flags) error {
	l.n.Step("El montaje")
	var err error
	if l.spy, err = common.FrameFromFlags(flags); err != nil {
		l.n.Fail(err, "banderas")
		return err
	}
	if l.box, err = common.NewInbox(l.spy, flags.Verbose); err != nil {
		l.n.Fail(err, "bandeja")
		return err
	}
	if !l.noTpl {
		l.box.Intakes.SetDepositTemplate(common.LabTenant, depositTemplate, 2)
	}
	l.n.Note("intakes.NewService(store, WithNotifier(n), WithQuoteSender(n), WithMetrics(telemetria.New(flow)), WithClock…):")
	l.n.Note("  el MISMO Notifier para el aviso de la plataforma y para la voz de la dueña (internal/arranque/fase6_solicitudes.go).")
	l.n.Note("El Notifier: Sender DOBLE (runtimehelpertest) · Destinations = contact.MemoryResolver (la vía custodiada, en memoria)")
	l.n.Note("  · SettingsReader = el mismo intakes.MemoryStore (tenant_settings: zonas de envío y plantilla de seña).")
	if l.noTpl {
		l.n.Note("-no-template: el tenant NO tiene plantilla de seña.")
	} else {
		l.n.Note("Plantilla de seña del tenant: %q (plazo 2 días).", depositTemplate)
	}
	l.n.Note("Sin CRM (WithCRMPusher) ni recordatorios perezosos: no son la pieza bajo estudio.")
	return nil
}

// draft fabrica el borrador de Ana con el pipeline real.
func (l *lab) draft() (string, error) {
	l.n.Step("El borrador: el pipeline real, sin ventana")
	l.n.Note("Por qué no se siembra a mano: el borrador es un payload §7.4 (líneas con unit_price null, preguntas, evidencias)")
	l.n.Note("  que escribe la etapa draft y que la bandeja LEE (LinesWithoutPrice). Una siembra a mano podría divergir sin avisar.")
	l.n.Note("La ventana se salta (E09–E11 ya la enseñaron): los mensajes van al hilo y el compositor real sella el literal.")
	contactID, err := l.box.NewCustomer(l.ctx, phoneAna, "Ana")
	if err != nil {
		l.n.Fail(err, "contacto")
		return "", err
	}
	l.n.Note("Ana escribe desde %s; el resolver le da el contact_id OPACO %s. Es lo único que guardan job y solicitud.", phoneAna, short(contactID))
	d, err := l.box.DraftFromMessages(l.ctx, sessionA, contactID, common.AmberBurst(), func(r common.Round) {
		l.n.Note("RunOnce %d · job %s · %q · etapa %q · %s de pared", r.N, r.Row.ID, r.Row.Status, r.Row.Stage, r.Wall.Round(time.Millisecond))
	})
	if err != nil {
		l.n.Fail(err, "pipeline")
		return "", err
	}
	if d.IntakeID == "" {
		l.n.Fail(nil, "el job terminó %q sin borrador: %s", d.Row.Status, d.Row.Error)
		return "", errUnexpected
	}
	l.n.OK("Borrador %s (UUID v5 del evento %s).", short(d.IntakeID), short(d.EventID))
	det := l.mustGet(d.IntakeID)
	l.showIntake(det)
	if rev, ok := intakes.LastRevision(det.Revisions); ok {
		l.showInterpreted(rev)
	}
	l.n.Note("🔑 El borrador vive en la REVISIÓN, no en intake_items: la solicitud nace con 0 líneas y total 0.")
	l.sends = l.showSends(0, "nacer no es una transición: la etapa draft no pasa por SetStatus y no avisa")
	l.evs = l.showMetrics(0)
	return d.IntakeID, nil
}

// approveTooSoon intenta aprobar sin texto y luego sin precios: las guardas, en su orden.
func (l *lab) approveTooSoon(id string) {
	l.n.Step("Aprobar YA: las guardas de Approve, en orden")
	_, err := l.box.Service.Approve(l.ctx, common.LabTenant, id, "   ")
	l.explain(err, "Approve con el texto en blanco")
	l.n.Note("La guarda 3 (texto vacío) va ANTES de leer la solicitud: no se toca la base.")
	_, err = l.box.Service.Approve(l.ctx, common.LabTenant, id, "Hola Ana, te confirmo el pedido.")
	l.explain(err, "Approve con texto, sobre el borrador recién nacido")
	l.n.Note("La guarda 6: la revisión vigente (interpreted) tiene líneas con unit_price null. Se listan TODAS.")
	l.n.Note("Si no las tuviera, caería la 7 (ErrEmptyQuote): 0 líneas de cliente en intake_items.")
	l.sends = l.showSends(l.sends, "ninguna guarda escribe ni manda nada")
}

// correct pone precio a las líneas: la revisión `corrected`.
func (l *lab) correct(id string) error {
	l.n.Step("Corregir líneas: ReplaceItems (la revisión corrected)")
	bad := []intakes.Item{{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1, UnitPrice: 0}, {SKU: "", Label: "", Qty: 0}}
	_, err := l.box.Service.ReplaceItems(l.ctx, common.LabTenant, id, bad, intakes.EditAsCorrection)
	l.explain(err, "ReplaceItems con la línea de envío y una línea vacía")
	var invalid *intakes.InvalidItemsError
	if errors.As(err, &invalid) {
		for _, d := range invalid.Defects {
			l.n.Note("  defecto línea %d · %s · %s", d.Index, d.Field, d.Message)
		}
	}
	items := []intakes.Item{
		{SKU: "TORTA-CHOC", Label: "Torta de chocolate 10 porciones", Customization: "decoración infantil, bizcocho húmedo", Qty: 1, UnitPrice: 2100},
		{SKU: "TORTA-VAIN", Label: "Torta de vainilla", Customization: "lluvia de colores, dulce de leche y merengue", Qty: 1, UnitPrice: 3900},
		{SKU: "TEQ-30", Label: "Tequeños congelados (paquete de 30)", Qty: 1, UnitPrice: 490},
	}
	l.box.Clock.Advance(10 * time.Minute) // la dueña abre la bandeja diez minutos después
	det, err := l.box.Service.ReplaceItems(l.ctx, common.LabTenant, id, items, intakes.EditAsCorrection)
	if err != nil {
		l.explain(err, "ReplaceItems")
		return err
	}
	l.n.OK("La dueña elige 10 porciones para la de chocolate y pone los precios del catálogo (EditAsCorrection).")
	l.showIntake(det)
	if rev, ok := intakes.LastRevision(det.Revisions); ok {
		l.n.Show("payload de la revisión corrected", rev.Payload)
	}
	l.n.Note("La revisión vigente ya es corrected, con precio NO nullable: PendingPriceLines no encuentra nada.")
	l.n.Note("as_correction deja la señal few-shot (corrects_revision_no/kind) apuntando al borrador que corrige.")
	l.sends = l.showSends(l.sends, "corregir es trabajo interno de la dueña")
	l.evs = l.showMetrics(l.evs)
	return nil
}

// requestInfo le pregunta algo al cliente: needs_info.
func (l *lab) requestInfo(id string) error {
	l.n.Step("Pedir información: RequestInfo (needs_info)")
	_, err := l.box.Service.RequestInfo(l.ctx, common.LabTenant, id, "")
	l.explain(err, "RequestInfo sin pregunta")
	question := "¡Hola Ana! Para la torta de chocolate, ¿te sirve la de 10 porciones o prefieres la de 12?"
	det, err := l.box.Service.RequestInfo(l.ctx, common.LabTenant, id, question)
	if err != nil {
		l.explain(err, "RequestInfo")
		return err
	}
	l.n.OK("pending_approval → needs_info, con NoticeByCaller: sale UN mensaje, el de la dueña, sin el aviso genérico.")
	l.showIntake(det)
	l.sends = l.showSends(l.sends, "")
	l.n.Note("NO escribe revisión: preguntar no cambia ni una línea ni el total. La pregunta queda en el hilo de WhatsApp.")
	l.evs = l.showMetrics(l.evs)
	_, err = l.box.Service.ReplaceItems(l.ctx, common.LabTenant, id, det.Items, intakes.EditPlain)
	l.explain(err, "ReplaceItems en needs_info")
	_, err = l.box.Service.Approve(l.ctx, common.LabTenant, id, "Te confirmo.")
	l.explain(err, "Approve en needs_info")
	return nil
}

// backToReview: el cliente contestó y la dueña vuelve a poner la solicitud por aprobar.
func (l *lab) backToReview(id string) error {
	l.n.Step("Volver a la revisión: SetStatus(pending_approval) desde la consola")
	l.n.Note("Ana contesta «la de 10 está perfecta» (entraría por el flujo normal: E05–E11). La dueña mueve el estado a mano.")
	l.box.Clock.Advance(20 * time.Minute)
	_, err := l.box.Service.SetStatus(l.ctx, common.LabTenant, id, intakes.StatusSettled, intakes.NoticeToClient)
	l.explain(err, "SetStatus(settled) desde needs_info")
	in, err := l.box.Service.SetStatus(l.ctx, common.LabTenant, id, intakes.StatusPendingApproval, intakes.NoticeToClient)
	if err != nil {
		l.explain(err, "SetStatus(pending_approval)")
		return err
	}
	l.n.OK("needs_info → pending_approval con NoticeToClient: es el selector de estado, y la plataforma SÍ avisa.")
	l.showIntake(l.mustGet(in.ID))
	l.n.Note("UpdateStatus a pending_approval garantiza la línea de envío en la MISMA escritura (D-041.11): sin zonas de")
	l.n.Note("  envío configuradas, nace a precio 0 («por confirmar»). En la revisión corrected no estaba.")
	l.sends = l.showSends(l.sends, "")
	return nil
}

// approve aprueba con el texto de la dueña.
func (l *lab) approve(id string) error {
	l.n.Step("Aprobar con rendered_text: Approve (confirmed)")
	l.box.Clock.Advance(5 * time.Minute)
	owner := "¡Hola Ana! Te confirmo tu pedido para el miércoles 22: torta de chocolate de 10 porciones $2100, " +
		"torta de vainilla $3900 y un paquete de 30 tequeños $490. Total $6490."
	det, err := l.box.Service.Approve(l.ctx, common.LabTenant, id, owner)
	if err != nil {
		l.explain(err, "Approve")
		return err
	}
	l.n.OK("pending_approval → confirmed. El orden: componer → transición (CAS) → revisión approved → envío → CRM → métrica.")
	l.showIntake(det)
	if rev, ok := intakes.LastRevision(det.Revisions); ok {
		l.n.Show("rendered_text de la revisión approved (lo que salió por el cable, byte a byte)", rev.RenderedText)
	}
	l.sends = l.showSends(l.sends, "")
	l.n.Note("Con NoticeByCaller: el genérico «✅ Tu pedido quedó confirmado…» NO sale. Un solo mensaje, el de la dueña.")
	l.evs = l.showMetrics(l.evs)
	_, err = l.box.Service.Approve(l.ctx, common.LabTenant, id, owner)
	l.explain(err, "Approve otra vez (doble clic)")
	l.sends = l.showSends(l.sends, "la segunda aprobación no escribe ni manda nada")
	return nil
}
