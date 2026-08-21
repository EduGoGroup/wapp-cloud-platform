package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// selfPnA es un número propio (E.164 sin '+') que pertenece al tenant bajo prueba.
const selfPnA = "573001110000"

// fakeSelfNumbers es un doble de runtime.SelfNumberChecker: responde por igualdad
// EXACTA contra los números sembrados por tenant (aislamiento estricto). Un tenant
// sin entrada nunca casa. El filtrado por PERFIL (excluir sesiones passive) y el
// cálculo del índice ciego son responsabilidad de la implementación real
// (PostgresSelfNumbers) y se cubren en self_numbers_integration_test.go; aquí el
// doble representa el conjunto YA filtrado.
//
// 🔑 Los números sembrados están en forma NORMALIZADA (solo dígitos) y la
// comparación es literal, a propósito: así este doble se comporta como el HMAC del
// índice ciego, que tampoco perdona un '+' ni un espacio. Si el runtime dejara de
// normalizar antes de preguntar, aquí dejaría de casar — igual que en producción.
type fakeSelfNumbers struct {
	byTenant map[string][]string
	err      error
	// visto, si no es nil, recoge el valor EXACTO que el runtime pasó al puerto.
	// Sirve para afirmar la normalización sin depender del veredicto.
	visto *string
}

func (f fakeSelfNumbers) IsSelfNumber(_ context.Context, tenantID, numeroNormalizado string) (bool, error) {
	if f.visto != nil {
		*f.visto = numeroNormalizado
	}
	if f.err != nil {
		return false, f.err
	}
	for _, n := range f.byTenant[tenantID] {
		if n == numeroNormalizado {
			return true, nil
		}
	}
	return false, nil
}

// incomingPn arma un entrante con from_pn EXPLÍCITO (el campo que consume la guarda
// anti-self-loop), a diferencia del helper incoming() que solo pone From.
func incomingPn(fromPn, text, waID string) *cloudlinkv1.IncomingMessage {
	return &cloudlinkv1.IncomingMessage{FromPn: fromPn, Text: text, WaMessageId: waID}
}

// newSelfLoopRuntime arma un runtime con el flujo de menú, una regla keyword→testFlow
// y el checker de números propios dado (Plan 020 · T2). El resolver fija tenantID
// con perfil activo.
func newSelfLoopRuntime(t *testing.T, tenantID string, self runtime.SelfNumberChecker) (*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), tenantID, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	if _, err := ts.Insert(context.Background(), trigger.Rule{
		TenantID: tenantID, Kind: trigger.KindKeyword,
		Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: testFlow, Enabled: true,
	}); err != nil {
		t.Fatalf("insert regla: %v", err)
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: tenantID}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithSelfNumbers(self))
	return rt, repo, sender, contacts
}

// TestHandleIncoming_SelfLoop_NoAutoResponde (caso a): un entrante cuyo from_pn es
// un self_pn del tenant NO dispara el trigger ni auto-responde ni crea estado.
func TestHandleIncoming_SelfLoop_NoAutoResponde(t *testing.T) {
	self := fakeSelfNumbers{byTenant: map[string][]string{testTenant: {selfPnA}}}
	rt, repo, sender, contacts := newSelfLoopRuntime(t, testTenant, self)
	ctx := context.Background()

	// from_pn == self_pn del tenant, texto que casaría la keyword "pedido".
	if err := rt.HandleIncoming(ctx, testSession, incomingPn(selfPnA, "pedido", "wamid.self")); err != nil {
		t.Fatalf("HandleIncoming self-loop: %v", err)
	}
	if sender.count() != 0 {
		t.Fatalf("un entrante de un número propio no debería auto-responder, envió %d", sender.count())
	}
	// Ninguna conversación creada: la guarda cortó antes del motor reactivo.
	key := store.Key{TenantID: testTenant, SessionID: testSession, ContactID: resolveID(t, contacts, selfPnA)}
	if _, ok, lerr := repo.Load(ctx, key); lerr != nil || ok {
		t.Fatalf("self-loop no debería crear estado (ok=%v err=%v)", ok, lerr)
	}
}

// TestHandleIncoming_Externo_DisparaNormal (caso b): un entrante de un número
// EXTERNO (no self_pn del tenant) se comporta como el 019 — arranca el flujo.
func TestHandleIncoming_Externo_DisparaNormal(t *testing.T) {
	self := fakeSelfNumbers{byTenant: map[string][]string{testTenant: {selfPnA}}}
	rt, repo, sender, contacts := newSelfLoopRuntime(t, testTenant, self)
	ctx := context.Background()

	const externo = "573009998877"
	if err := rt.HandleIncoming(ctx, testSession, incomingPn(externo, "pedido", "wamid.ext")); err != nil {
		t.Fatalf("HandleIncoming externo: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("un entrante externo debería arrancar el flujo (1 envío), envió %d", sender.count())
	}
	st := loadState(t, repo, resolveID(t, contacts, externo))
	if st.CurrentNode != "root" {
		t.Fatalf("estado tras entrante externo inesperado: %+v", st)
	}
}

// TestHandleIncoming_SelfLoop_AisladoPorTenant (caso c): el self_pn del tenant A NO
// bloquea entrantes del tenant B — con ese MISMO número, B dispara normal.
func TestHandleIncoming_SelfLoop_AisladoPorTenant(t *testing.T) {
	const tenantB = "tenant-2"
	// El lister SOLO conoce el self_pn bajo el tenant de prueba (A); B no tiene entrada.
	self := fakeSelfNumbers{byTenant: map[string][]string{testTenant: {selfPnA}}}
	rt, repo, sender, contacts := newSelfLoopRuntime(t, tenantB, self)
	ctx := context.Background()

	// from_pn == selfPnA (propio de A) pero la sesión resuelve al tenant B: NO es
	// self-loop para B ⇒ arranca normal.
	if err := rt.HandleIncoming(ctx, testSession, incomingPn(selfPnA, "pedido", "wamid.iso")); err != nil {
		t.Fatalf("HandleIncoming aislamiento: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("el self_pn de otro tenant no debería bloquear al tenant B, envió %d", sender.count())
	}
	st := loadStateTenant(t, repo, tenantB, resolveIDTenant(t, contacts, tenantB, selfPnA))
	if st.CurrentNode != "root" {
		t.Fatalf("estado del tenant B inesperado: %+v", st)
	}
}

// selfPnAAdornado es EL MISMO número que selfPnA escrito como lo manda WhatsApp en
// la vida real: con '+', espacios y guiones. Normaliza a selfPnA dígito por dígito.
const selfPnAAdornado = "+57 300-111 0000"

// TestHandleIncoming_SelfLoop_NormalizaAntesDePreguntar (Plan 046 · T4.1): el mismo
// número escrito con '+', espacios y guiones tiene que dar EL MISMO veredicto que su
// forma canónica.
//
// 🔑 ES EL CASO QUE CAZA EL FALLO DE NO NORMALIZAR, y por eso afirma DOS cosas:
//   - que el valor que llega al puerto es exactamente el normalizado (si el runtime
//     dejara de normalizar, el puerto recibiría "+57 300-111 0000", el índice ciego
//     saldría distinto y el número dejaría de casar CONSIGO MISMO);
//   - que el veredicto es el mismo que con la forma canónica: no auto-responde ni
//     crea estado.
//
// El fallo que previene es MUDO —no hay error, no hay log, la guarda simplemente
// deja de bloquear—, así que solo un test puede verlo.
func TestHandleIncoming_SelfLoop_NormalizaAntesDePreguntar(t *testing.T) {
	var visto string
	self := fakeSelfNumbers{byTenant: map[string][]string{testTenant: {selfPnA}}, visto: &visto}
	rt, repo, sender, contacts := newSelfLoopRuntime(t, testTenant, self)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incomingPn(selfPnAAdornado, "pedido", "wamid.norm")); err != nil {
		t.Fatalf("HandleIncoming self-loop adornado: %v", err)
	}
	if visto != selfPnA {
		t.Fatalf("el puerto debe recibir el número YA NORMALIZADO (%s); recibió %q — sin esa normalización el índice ciego no casaría y la guarda quedaría muda",
			selfPnA, visto)
	}
	if sender.count() != 0 {
		t.Fatalf("el MISMO número con '+', espacios y guiones debe bloquear igual que su forma canónica, envió %d", sender.count())
	}
	key := store.Key{TenantID: testTenant, SessionID: testSession, ContactID: resolveID(t, contacts, selfPnA)}
	if _, ok, lerr := repo.Load(ctx, key); lerr != nil || ok {
		t.Fatalf("self-loop (forma adornada) no debería crear estado (ok=%v err=%v)", ok, lerr)
	}
}

// TestHandleIncoming_SelfLoop_ErrorDelCheckerNoBloquea: si el checker falla —BD
// caída, KeyProvider ausente— la guarda se OMITE y el entrante se procesa. Es la
// política conservadora hacia procesar: un fallo de infraestructura no puede
// convertirse en un tenant que deja de atender a sus clientes.
func TestHandleIncoming_SelfLoop_ErrorDelCheckerNoBloquea(t *testing.T) {
	self := fakeSelfNumbers{
		byTenant: map[string][]string{testTenant: {selfPnA}},
		err:      errors.New("fleet_sessions no disponible"),
	}
	rt, _, sender, _ := newSelfLoopRuntime(t, testTenant, self)

	if err := rt.HandleIncoming(context.Background(), testSession, incomingPn(selfPnA, "pedido", "wamid.err")); err != nil {
		t.Fatalf("HandleIncoming con checker en error: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("un fallo del checker debe OMITIR la guarda (procesar), no silenciar el tráfico; envió %d", sender.count())
	}
}

// loadStateTenant es loadState parametrizado por tenant (los helpers base fijan testTenant).
func loadStateTenant(t *testing.T, repo *store.MemoryRepository, tenantID, contactID string) model.Conversation {
	t.Helper()
	st, ok, err := repo.Load(context.Background(), store.Key{TenantID: tenantID, SessionID: testSession, ContactID: contactID})
	if err != nil || !ok {
		t.Fatalf("Load(%s): ok=%v err=%v", contactID, ok, err)
	}
	return st
}

// resolveIDTenant resuelve el contact_id de un número bajo un tenant explícito.
func resolveIDTenant(t *testing.T, contacts *contact.MemoryResolver, tenantID, phone string) string {
	t.Helper()
	id, err := contacts.Resolve(context.Background(), tenantID, []contact.Ref{phoneRef(t, phone)}, "")
	if err != nil {
		t.Fatalf("Resolve(%s): %v", phone, err)
	}
	return id
}
