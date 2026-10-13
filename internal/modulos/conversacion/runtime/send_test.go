package runtime_test

// send_test.go prueba, por Start y por HandleIncoming y leyendo el doble del Sender, el
// contrato de send.go (SD-1…SD-13): el destino, el despacho en orden (RT-4), la prefirma de
// los adjuntos, el limitador (RT-8), la racha (RT-11) y el texto fijo del sistema.

import (
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/media"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

const (
	sendFlowID   = "brochure-zzq"
	sendMediaKey = "wapp/media/price-list-zzq.pdf"
	sendStreak   = "runtime: auto-respuesta emitida (racha del episodio)"
	sendLimited  = "runtime: auto-respuesta limitada por rate-limit de conversación"
)

// sendMediaFlow dice un texto, manda un adjunto y dice otro texto: tres salidas en un arranque.
func sendMediaFlow() model.Flow {
	doc, bye := "doc", "bye"
	return model.Flow{
		FlowID:  sendFlowID,
		Initial: "intro",
		Nodes: map[string]model.Node{
			"intro": {Type: model.NodeTypeMessage, Text: "Va la lista-zzq", Next: &doc},
			"doc": {Type: media.NodeTypeMedia, Next: &bye, Content: &model.ContentRef{
				Source: "static", Key: sendMediaKey, Filename: "Lista de precios-zzq.pdf",
				Mime: "application/pdf", Kind: media.KindDocument, Caption: "Acá va la lista-zzq",
			}},
			"bye": {Type: model.NodeTypeMessage, Text: "Hasta luego-zzq"},
		},
	}
}

// sendMediaHarness monta un guion con el flujo del adjunto publicado.
func sendMediaHarness(t *testing.T, extra ...runtime.Option) *harness {
	t.Helper()
	h := newHarness(t, withOptions(func(*harness) []runtime.Option { return extra }))
	h.seedFlow(sendMediaFlow())
	return h
}

// sendKinds resume los intentos de envío como "text"/"media" con "!" si fallaron.
func sendKinds(h *harness) []string {
	attempts := h.sender.Attempts()
	out := make([]string, 0, len(attempts))
	for _, a := range attempts {
		kind := "text"
		if a.Media {
			kind = "media"
		}
		if a.Err != nil {
			kind += "!"
		}
		out = append(out, kind)
	}
	return out
}

// sendLiveHarness monta la sonda con una conversación viva sembrada en su raíz.
func sendLiveHarness(t *testing.T, probe resumeProbe) *harness {
	t.Helper()
	h := resumeHarness(t, probe, nil)
	resumeSeed(h, nil)
	return h
}

// SD-1 · El destino sale del contact_id opaco, no del JID con que llegó el entrante.
func TestSend_DestinationComesFromTheContact(t *testing.T) {
	h := sendLiveHarness(t, resumeProbe{})

	h.say("wa-1", "hola")

	sends := h.sender.Sends()
	if len(sends) != 1 || sends[0].To != harnessPhone || sends[0].SessionID != harnessSession {
		t.Errorf("envíos = %+v, quería uno a %s (el número resuelto, no el JID %s) por la sesión del guion", sends, harnessPhone, jid(harnessPhone))
	}
}

// SD-1 / SD-4 · Los dos fallos del destino, con su prefijo; el estado ya estaba guardado.
func TestSend_DestinationFailures(t *testing.T) {
	username := contact.Ref{Kind: contact.KindWAUsername, Value: "someone-zzq"}
	cases := []struct {
		name     string
		contacts func(h *harness) startContacts
		prefix   string
		cause    error
	}{
		{"destination cannot be resolved", func(h *harness) startContacts {
			return startContacts{Resolver: h.contacts, destinoErr: errResumeCause}
		}, "runtime: resolver destino: ", errResumeCause},
		{"destination is not addressable", func(h *harness) startContacts {
			return startContacts{Resolver: h.contacts, destinoRef: &username}
		}, "runtime: destino no direccionable: ", contact.ErrNoDestino},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.seedFlow(menuFlow(startFlowID))
			rt := startRuntimeWith(h, tc.contacts(h))

			_, err := rt.Start(t.Context(), harnessTenant, startFlowID, harnessSession, startRef(t, harnessPhone))

			if err == nil || !strings.Contains(err.Error(), tc.prefix) || !errors.Is(err, tc.cause) {
				t.Errorf("error = %v, quería %q envolviendo la causa", err, tc.prefix)
			}
			if _, found := h.state(); !found {
				t.Error("el estado no estaba guardado cuando falló el destino (RT-4)")
			}
			if got := h.sender.Attempts(); len(got) != 0 {
				t.Errorf("intentos de envío = %+v, sin destino no se llama al Sender", got)
			}
		})
	}
}

// SD-2 / SD-5 · Las salidas salen EN ORDEN; el adjunto se prefirma y va por SendMedia con su
// URL, nombre, mime, caption y kind; se devuelve el Ack de la última.
func TestSend_OutputsInOrderAndMediaPresigned(t *testing.T) {
	h := sendMediaHarness(t)

	ack, err := startAPI(h, sendFlowID)

	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := sendKinds(h); !resumeEqual(got, []string{"text", "media", "text"}) {
		t.Errorf("envíos = %v, quería texto, adjunto y texto, en ese orden", got)
	}
	if ack.GetAckedCommandId() != "cmd-3" {
		t.Errorf("Ack = %v, quería el de la última salida (cmd-3)", ack)
	}
	if got := h.presigner.Keys(); !resumeEqual(got, []string{sendMediaKey}) {
		t.Errorf("claves prefirmadas = %v, quería la del adjunto", got)
	}
	sent := h.sender.Media()
	if len(sent) != 1 {
		t.Fatalf("adjuntos = %+v, quería uno", sent)
	}
	want := runtimehelpertest.Send{
		Media: true, SessionID: harnessSession, To: harnessPhone, URL: harnessMediaURL,
		Filename: "Lista de precios-zzq.pdf", Mime: "application/pdf", Caption: "Acá va la lista-zzq",
		Kind: media.KindDocument, CommandID: "cmd-2",
	}
	if sent[0] != want {
		t.Errorf("adjunto = %+v, quería %+v", sent[0], want)
	}
}

// SD-3 / SD-5 · Ante el primer error se corta: no se intentan las siguientes y se devuelve el
// error (con la clave del adjunto) junto al último Ack logrado.
func TestSend_FirstErrorCutsTheDispatch(t *testing.T) {
	cases := []struct {
		name    string
		breakIt func(h *harness)
		prefix  string
		kinds   []string
	}{
		{"presign fails", func(h *harness) { h.presigner.Fail(errResumeCause) },
			`runtime: presignar media "` + sendMediaKey + `": `, []string{"text"}},
		{"media send fails", func(h *harness) { h.sender.FailMedia(errResumeCause) },
			`runtime: enviar media "` + sendMediaKey + `": `, []string{"text", "media!"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := sendMediaHarness(t)
			tc.breakIt(h)

			ack, err := startAPI(h, sendFlowID)

			if err == nil || !strings.Contains(err.Error(), tc.prefix) || !errors.Is(err, errResumeCause) {
				t.Errorf("error = %v, quería %q envolviendo la causa", err, tc.prefix)
			}
			if ack.GetAckedCommandId() != "cmd-1" {
				t.Errorf("Ack = %v, quería el último logrado (cmd-1)", ack)
			}
			if got := sendKinds(h); !resumeEqual(got, tc.kinds) {
				t.Errorf("envíos = %v, quería %v: tras el error no se intenta nada más", got, tc.kinds)
			}
			if _, found := h.state(); !found {
				t.Error("el fallo del envío no puede revertir el estado ya guardado (SD-4)")
			}
			if got := h.rt.MaxAutoreplyStreak(); got != 0 {
				t.Errorf("racha = %d, una emisión que falla a medias no suma (SD-10)", got)
			}
		})
	}
}

// SD-6 / RT-4 · Sin Presigner un adjunto es un error CONTROLADO, nunca un pánico; no se llama
// al Sender para esa salida y el estado ya quedó guardado.
func TestSend_MediaWithoutPresignerIsAControlledError(t *testing.T) {
	h := sendMediaHarness(t, runtime.WithPresignClient(nil))

	_, err := startAPI(h, sendFlowID)

	if err == nil || err.Error() != "runtime: nodo media sin PresignClient configurado (usa WithPresignClient)" {
		t.Errorf("error = %v, quería el error controlado de configuración, byte a byte", err)
	}
	if got := sendKinds(h); !resumeEqual(got, []string{"text"}) {
		t.Errorf("envíos = %v, quería solo el texto previo: el adjunto no llega al Sender", got)
	}
	if _, found := h.state(); !found {
		t.Error("el estado no estaba guardado cuando falló el adjunto")
	}
	if _, err := startAPI(h, sendFlowID); !errors.Is(err, runtime.ErrConversationExists) {
		t.Errorf("segundo Start = %v, quería ErrConversationExists", err)
	}
}

// SD-7 · Sin salidas no se envía nada y no se gasta token; el turno se guarda igual.
func TestSend_NoOutputsSpendNoToken(t *testing.T) {
	h := sendLiveHarness(t, resumeProbe{silent: true})

	h.say("wa-1", "hola")

	if len(h.sender.Attempts()) != 0 || len(h.limiter.Calls()) != 0 {
		t.Errorf("envíos = %+v, tokens = %+v; sin salidas no hay ni lo uno ni lo otro", h.sender.Attempts(), h.limiter.Calls())
	}
	if st, _ := h.state(); st.LastWaMessageID != "wa-1" {
		t.Errorf("last_wa_message_id = %q, el turno tenía que guardarse", st.LastWaMessageID)
	}
	if got := h.rt.MaxAutoreplyStreak(); got != 0 {
		t.Errorf("racha = %d, una emisión sin salidas no suma (SD-10)", got)
	}
}

// SD-8 · Con el cupo agotado no se envía (el estado ya avanzó), se cuenta rate_limit y el WARN
// lleva SOLO ids opacos. El token se cobra ANTES de resolver el destino.
func TestSend_RateLimitedReplyIsNotSent(t *testing.T) {
	h := resumeHarness(t, resumeProbe{}, nil)
	rt := startRuntimeWith(h, startContacts{Resolver: h.contacts, destinoErr: errResumeCause})
	resumeSeed(h, nil)
	h.limiter.Limit(0)

	if err := rt.HandleIncoming(t.Context(), harnessSession, h.incoming("wa-1", "texto secreto-zzq")); err != nil {
		t.Fatalf("HandleIncoming = %v; con el cupo agotado no se llega a resolver el destino", err)
	}

	if got := h.sender.Attempts(); len(got) != 0 {
		t.Errorf("intentos de envío = %+v, sin token no se responde", got)
	}
	st, _ := h.state()
	if st.LastWaMessageID != "wa-1" {
		t.Errorf("last_wa_message_id = %q, el estado avanza y se guarda aunque no se responda", st.LastWaMessageID)
	}
	if calls := h.limiter.Calls(); len(calls) != 1 || calls[0].Key != h.key().String() {
		t.Errorf("peticiones al limitador = %+v, quería una con store.Key.String()", calls)
	}
	if got := h.blockedReasons(); !resumeEqual(got, []string{"rate_limit"}) {
		t.Errorf("motivos de corte = %v, quería [rate_limit]", got)
	}
	line, ok := resumeLogLine(h, "warn", sendLimited)
	if !ok {
		t.Fatalf("falta el WARN del rate-limit\nlog:\n%s", h.log.dump())
	}
	if len(line.fields) != 3 || line.fields["tenant_id"] != harnessTenant || line.fields["session_id"] != harnessSession || line.fields["contact_id"] != st.ContactID {
		t.Errorf("campos = %v, quería tenant_id, session_id y contact_id y NADA más", line.fields)
	}
	if dump := h.log.dump(); strings.Contains(dump, "texto secreto-zzq") || strings.Contains(dump, harnessPhone) {
		t.Errorf("el log lleva el texto o el número del cliente:\n%s", dump)
	}
}

// sendTwoTextsFlow es un menú cuya opción 1 contesta con DOS textos.
func sendTwoTextsFlow() model.Flow {
	second := "second"
	return model.Flow{
		FlowID:  "two-texts-zzq",
		Initial: "root",
		Nodes: map[string]model.Node{
			"root":   {Type: model.NodeTypeMenu, Prompt: "Elige-zzq", Options: map[string]string{"1": "first"}},
			"first":  {Type: model.NodeTypeMessage, Text: "Primero-zzq", Next: &second},
			"second": {Type: model.NodeTypeMessage, Text: "Segundo-zzq"},
		},
	}
}

// sendTwoTextsHarness deja una conversación viva en el menú de sendTwoTextsFlow.
func sendTwoTextsHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.seedFlow(sendTwoTextsFlow())
	st := model.Conversation{
		TenantID: harnessTenant, SessionID: harnessSession, ContactID: h.contactID(harnessPhone),
		FlowID: "two-texts-zzq", FlowVersion: 1, CurrentNode: "root",
	}
	if err := h.repo.Save(t.Context(), st); err != nil {
		t.Fatalf("sembrar la conversación: %v", err)
	}
	return h
}

// SD-9 / SD-11 · Un token y un punto de racha por EMISIÓN, no por mensaje.
func TestSend_OneTokenAndOneStreakPointPerEmission(t *testing.T) {
	h := sendTwoTextsHarness(t)

	h.say("wa-1", "1")

	if got := h.texts(); !resumeEqual(got, []string{"Primero-zzq", "Segundo-zzq"}) {
		t.Fatalf("textos = %q, quería las dos salidas del turno", got)
	}
	if got := len(h.limiter.Calls()); got != 1 {
		t.Errorf("tokens cobrados = %d, quería 1 por la emisión entera", got)
	}
	if got := h.rt.MaxAutoreplyStreak(); got != 1 {
		t.Errorf("racha = %d, quería 1 por la emisión entera", got)
	}
}

// SD-10 · Una emisión que falla A MEDIAS —una salida enviada y la siguiente con error— no suma.
func TestSend_HalfFailedEmissionDoesNotCount(t *testing.T) {
	h := sendTwoTextsHarness(t)
	h.sender.OnSend(func(runtimehelpertest.Send) { h.sender.FailText(errResumeCause) })

	err := h.handle(h.incoming("wa-1", "1"))

	if err == nil || !strings.Contains(err.Error(), "runtime: enviar texto: ") {
		t.Errorf("error = %v, quería «runtime: enviar texto: …»", err)
	}
	if got := sendKinds(h); !resumeEqual(got, []string{"text", "text!"}) {
		t.Errorf("envíos = %v, quería uno despachado y el segundo fallido", got)
	}
	if got := h.rt.MaxAutoreplyStreak(); got != 0 {
		t.Errorf("racha = %d, una emisión que falla a medias no suma", got)
	}
	if st, _ := h.state(); !st.Finished() {
		t.Errorf("estado = %+v, el fallo del envío no revierte el avance ya guardado (SD-4)", st)
	}
}

// SD-10 / SD-12 · Cada emisión que salió suma 1 a la racha de su conversación y deja rastro a
// DEBUG con la racha y solo ids opacos.
func TestSend_EachEmissionCountsAndLogsTheStreak(t *testing.T) {
	h := sendLiveHarness(t, resumeProbe{})

	h.say("wa-1", "hola")
	h.say("wa-2", "hola otra vez")

	if got := h.rt.MaxAutoreplyStreak(); got != 2 {
		t.Errorf("racha = %d, quería 2 tras dos emisiones", got)
	}
	var streaks []any
	for _, line := range h.log.at("debug") {
		if line.msg != sendStreak {
			continue
		}
		streaks = append(streaks, line.fields["racha"])
		if line.fields["tenant_id"] != harnessTenant || line.fields["session_id"] != harnessSession || line.fields["contact_id"] != h.key().ContactID {
			t.Errorf("campos = %v, quería tenant_id, session_id y contact_id de la conversación", line.fields)
		}
	}
	if len(streaks) != 2 || streaks[0] != 1 || streaks[1] != 2 {
		t.Errorf("rachas logueadas = %v, quería 1 y 2", streaks)
	}
	if got := h.closedStreaks(); len(got) != 0 {
		t.Errorf("rachas cerradas = %v, observar no cierra ni corta nada", got)
	}
}

// SD-13 · El texto fijo del sistema (la bienvenida) sale por el mismo despacho pero NO cobra
// token ni suma a la racha.
func TestSend_SystemTextIsOutsideTheLimiterAndTheStreak(t *testing.T) {
	h := newHarness(t)
	h.enableFeature(entitlements.FeatureLLMIntake)
	h.limiter.Limit(0)

	h.say("wa-1", "hola") // sin reglas de disparo: solo la bienvenida

	sends := h.sender.Sends()
	if len(sends) != 1 || sends[0].Text != store.DefaultWelcomeText || sends[0].To != harnessPhone {
		t.Fatalf("envíos = %+v, quería solo la bienvenida al número del contacto\nlog:\n%s", sends, h.log.dump())
	}
	if got := h.limiter.Calls(); len(got) != 0 {
		t.Errorf("peticiones al limitador = %+v, la bienvenida no cobra token", got)
	}
	if got := h.rt.MaxAutoreplyStreak(); got != 0 {
		t.Errorf("racha = %d, la bienvenida no suma", got)
	}
	if _, logged := resumeLogLine(h, "debug", sendStreak); logged {
		t.Error("la bienvenida dejó el rastro de racha de una auto-respuesta")
	}
}
