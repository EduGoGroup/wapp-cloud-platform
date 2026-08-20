package runtime_test

// Plan 053 · Ola 7 — el Start NO reinicia: 409 y punto.
//
// Este fichero REEMPLAZA a owner_flow_guard_test.go y restart_inherits_pointers_test.go
// (887 líneas entre los dos), que probaban la rama del reinicio por Start. Esa rama se
// retiró: llevaba sin poder ejecutarse desde el Plan 054 · T2.3, y la decisión de
// producto que la habría gobernado ya estaba tomada en sentido contrario (D-054.5).
//
// ── QUÉ VIGILA, y por qué es MENOS código pero MÁS filo ──────────────────────────
//
// La invariante es ahora de una sola línea: un Start sobre una clave con conversación
// viva devuelve ErrConversationExists **sin consultar la ResumePolicy de nadie**. La
// segunda mitad es la que importa y la que ningún test de error puede dar: mientras
// existió la rama, el 409 salía IGUAL tanto si la guarda de posesión mordía como si la
// política contestaba «no reinicies» — dos caminos internos muy distintos con la misma
// salida. Por eso aquí, como allí, hay un ESPÍA MORTAL: una ResumePolicy que MUERE si
// alguien la llama. Solo que ahora vigila más, porque ya no hay excepción legítima
// alguna: desde Start, esa política no debe consultarse JAMÁS.
//
// Si alguien repone el reinicio, este test se pone rojo. Verificado por mutación.
//
// ⚠️ NO CONFUNDIR con prepareResume, que sigue VIVO y consulta la MISMA
// rt.resumePolicies por el camino del ENTRANTE (el cliente escribe y su pedido anterior
// ya había terminado). Lo que se retiró fue un consumidor, no el mecanismo — y de eso se
// encargan los tests de cart_resume_test.go, que no se tocaron.

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// o7ClaveVars/o7MarcaVars marcan las Vars de la fila que YA está ahí. Sirven para una
// sola aserción, y no es decorativa: un 409 no puede dejar rastro en el estado del
// dueño legítimo.
const (
	o7ClaveVars = "o7_marca"
	o7MarcaVars = "vars-de-la-conversacion-viva"
)

// o7PoliticaMortal es el espía: no cuenta llamadas, MUERE si le llaman. Patrón heredado
// de t51FatalResolver (Plan 043 · T5.1) y del que vigilaba la guarda de posesión.
//
// Matan los DOS métodos del puerto. Antes de la Ola 7, Restart podía invocarse
// legítimamente desde Start (era el reinicio) y Seed no; ahora ninguno de los dos tiene
// por qué verse por esta puerta, y por eso el espía puede ser total.
type o7PoliticaMortal struct{ t *testing.T }

func (p *o7PoliticaMortal) Restart(_ context.Context, tenantID, contactID string, vars map[string]any) (bool, string, []modules.Effect, error) {
	p.t.Fatalf("Plan 053 · Ola 7: Start consultó ResumePolicy.Restart (tenant=%q contacto=%q vars=%+v). Esa rama SE RETIRÓ: un Start sobre una conversación viva devuelve 409 y no reinicia nada. Si has repuesto el reinicio a propósito, la pregunta que hay que contestar ANTES es de producto —¿debe un /start por API reiniciar una conversación en curso?— y para el carrito y la encuesta D-054.5 ya la contestó que NO",
		tenantID, contactID, vars)
	return false, "", nil, nil
}

func (p *o7PoliticaMortal) Seed(_ context.Context, tenantID string, vars map[string]any) error {
	p.t.Fatalf("Plan 053 · Ola 7: Start consultó ResumePolicy.Seed (tenant=%q vars=%+v); por esta puerta no se consulta política alguna", tenantID, vars)
	return nil
}

var _ modules.ResumePolicy = (*o7PoliticaMortal)(nil)

// o7Runtime arma el runtime con el espía REGISTRADO bajo el tipo del nodo inicial de
// sampleFlow. Registrarlo es el punto: si el reinicio volviera, encontraría política y
// el espía moriría. Sin registrarla, este test no probaría nada.
func o7Runtime(t *testing.T) (*runtime.Runtime, *store.MemoryRepository, *contact.MemoryResolver, *fakeSender) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar la definición del flujo que se arranca: %v", err)
	}
	contacts := contact.NewMemoryResolver(repo)
	sender := &fakeSender{}
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithResumePolicy(model.NodeTypeMenu, &o7PoliticaMortal{t: t}))
	return rt, repo, contacts, sender
}

// TestStart_SobreConversacionViva_Da409SinConsultarPolitica es el test que protege el
// borrado de la rama. Tres aserciones, y las tres por separado:
//
//   - hacia fuera: ErrConversationExists, el 409 determinista;
//   - hacia dentro: la ResumePolicy NO se consulta (el espía);
//   - y la fila queda INTACTA — un rechazo no puede mover el flujo, ni las Vars, ni los
//     punteros de quien estaba ahí.
//
// La tercera cubre lo que la Ola 6 arregló y la Ola 7 hace innecesario: mientras hubo
// reinicio, el Save pisaba la fila y apagaba sus dos punteros a evento (DEUDA-053.1).
// Sin reinicio no hay Save que pise, así que esa deuda ya no puede volver por aquí — y
// esta aserción es lo que lo mantiene cierto.
func TestStart_SobreConversacionViva_Da409SinConsultarPolitica(t *testing.T) {
	ctx := context.Background()
	rt, repo, contacts, sender := o7Runtime(t)
	cid := resolveID(t, contacts, testContact)

	// La conversación que ya está ahí, con sus dos punteros a evento puestos. Los ids
	// son opacos a propósito: esta prueba no necesita un plano de eventos cableado, solo
	// que lo que había siga estando.
	const activo, dueno = "ev-activo-o7", "ev-dueno-o7"
	if err := repo.Save(ctx, model.Conversation{
		TenantID: testTenant, SessionID: testSession, ContactID: cid,
		FlowID: testFlow, FlowVersion: 1, CurrentNode: "root",
		Vars: map[string]any{o7ClaveVars: o7MarcaVars}, EventID: activo, OwnerEventID: dueno,
	}); err != nil {
		t.Fatalf("sembrar la conversación viva: %v", err)
	}

	_, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact))
	if !errors.Is(err, runtime.ErrConversationExists) {
		t.Fatalf("un Start sobre una conversación viva debe dar ErrConversationExists (409 determinista); dio: %v", err)
	}

	st := loadState(t, repo, cid)
	if st.EventID != activo || st.OwnerEventID != dueno {
		t.Fatalf("el rechazo no puede tocar los punteros a evento: activo=%q (quiero %q) dueño=%q (quiero %q). Si esto falla es que alguien repuso el reinicio: su Save escribe un estado fresco y APAGA los dos punteros, que es DEUDA-053.1",
			st.EventID, activo, st.OwnerEventID, dueno)
	}
	if st.FlowID != testFlow || st.CurrentNode != "root" {
		t.Fatalf("el rechazo no puede mover el flujo guardado: flow=%q nodo=%q", st.FlowID, st.CurrentNode)
	}
	if got, ok := st.Vars[o7ClaveVars].(string); !ok || got != o7MarcaVars {
		t.Fatalf("las Vars de la conversación viva deben quedar intactas tras el 409; %s = %v", o7ClaveVars, st.Vars[o7ClaveVars])
	}
	if sender.count() != 0 {
		t.Fatalf("un Start rechazado con 409 no le habla al cliente; envió %d mensajes: %q", sender.count(), sender.texts())
	}
}

// TestStart_SobreClaveLibre_AbreLaConversacionSinTocarPolitica es la otra mitad, y cubre
// al 99 % del tráfico: quitar la rama no puede haber roto el arranque normal.
//
// El espía vale aquí igual que arriba, y por una razón distinta: fija que el arranque
// limpio NO paga una consulta a la política. Antes de la Ola 7 eso lo garantizaba que el
// bloque viviera dentro del `if exists`; ahora lo garantiza que el bloque no exista. La
// aserción sobrevive a las dos implementaciones porque mira la conducta, no la forma.
func TestStart_SobreClaveLibre_AbreLaConversacionSinTocarPolitica(t *testing.T) {
	ctx := context.Background()
	rt, repo, contacts, sender := o7Runtime(t)

	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("un Start sobre una clave sin conversación debe abrirla sin más; dio: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("el arranque limpio debe enviar el nodo inicial (1 mensaje), envió %d", sender.count())
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.CurrentNode != "root" || st.FlowID != testFlow {
		t.Fatalf("el arranque limpio debe dejar la conversación en el nodo inicial; flow=%q nodo=%q", st.FlowID, st.CurrentNode)
	}
	// Un arranque por esta puerta no pertenece a ningún evento (E-6: el arranque por API
	// no pare fila en conversation_events), y ahora tampoco puede heredar punteros de
	// nadie porque ya no hay de dónde.
	if st.EventID != "" || st.OwnerEventID != "" {
		t.Fatalf("un arranque limpio no pertenece a ningún evento: activo=%q dueño=%q deberían estar vacíos", st.EventID, st.OwnerEventID)
	}
}
