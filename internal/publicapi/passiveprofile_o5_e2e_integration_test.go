// passiveprofile_o5_e2e_integration_test.go — T5.1 (Plan 046 · Ola 5, REQ-08/12/20):
// el perfil pasivo de punta a punta EN LA NUBE, con sus dos caras enfrentadas en el
// mismo guion y sobre la MISMA sesión.
//
// ── 🔴 LO PRIMERO: EL CRITERIO (a) DEL PLAN NO ES EJECUTABLE AQUÍ, Y SE DICE ──────
// T5.1 pedía tres superficies para el caso «la pasiva recibe y no sube nada»: la
// COLA del Edge, el OUTBOX del Edge y la nube. Las dos primeras viven en otro repo y
// las cierra T2.2, que está en `[x]`. La tercera —«la nube no vio nada»— NO se puede
// afirmar desde este repo, y no por falta de esfuerzo:
//
//	· Aquí no hay Edge. Los doce e2e de este paquete doblan el transporte (un
//	  sender falso), así que no existe nada que pueda «subir» o «dejar de subir».
//	· Un test que no envíe y luego compruebe que la nube no recibió nada es
//	  TAUTOLÓGICO: mide su propio silencio.
//	· Y la mutación prescrita —«quitar el corte del paso 1.5 de T2.2 ⇒ rojo por la
//	  tercera»— es imposible: ese código está en wapp-edge-agent y este binario no
//	  lo enlaza. Cambiarlo no puede poner rojo nada de aquí.
//
// ⇒ La tercera superficie es UNA PRUEBA DE CAMPO, y se registra como tal en
// `docs/pruebas-de-campo-pendientes.md` (**PC-15**): hace falta un Edge real con el
// filtro puesto, un teléfono que le escriba y una nube a la que mirar. Nada de eso
// se fabrica en Go.
//
// ── QUÉ SÍ CIERRA ESTE FICHERO, Y POR QUÉ VALE ────────────────────────────────────
// El CONTRASTE, que es el corazón del perfil pasivo y que hoy no prueba nadie: la
// MISMA sesión, en el MISMO guion, NO reacciona a lo que le llega y SÍ envía lo que
// el dueño le manda enviar. Las dos mitades existen sueltas —passive_guard_test.go
// prueba la primera con dobles; la ruta de envío se prueba sin mirar el perfil— y
// separadas no dicen lo que el dueño compró. Una pasiva que no pudiera enviar no
// serviría para nada, y una que reaccionara no sería pasiva.
//
// 🔴 Y ESTE E2E NO RE-PRUEBA EL CORTE DEL EDGE. Lo que su fase 1 afirma es que el
// corte CLOUD (reactiveBlocked) sigue vivo como defensa en profundidad (D-046.7).
// Confundir las dos cosas sería creer que el filtro del Edge está probado cuando lo
// que se probó es la red de debajo: con el filtro del Edge BORRADO, esta fase seguiría
// verde —el mensaje subiría y el corte cloud lo pararía igual—. Por eso la prueba del
// filtro es PC-15 y no este archivo.
//
// Todo lo que se puede es de PRODUCCIÓN: el repositorio de flota real (la sesión nace
// y cambia de perfil por MarkOnline/SetProfile, no por INSERT a mano), el resolver de
// tenant real (que es quien lee `profile` de la tabla), el runtime real, el handler
// público real montado por publicapi.Register, el store de acuses real y el sink de
// acuses real. Los dobles son el TRANSPORTE (sender) y la PII de contactos, los mismos
// dos que ya doblan sus doce hermanos.
//
// Corre contra WAPP_TEST_DB_DSN (se omite sin ella; WAPP_TEST_REQUIRE_DB la exige,
// como sus hermanos). Datos con prefijo o5p- y limpieza por t.Cleanup.
package publicapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/receipts"
)

// o5pSender dobla el transporte hacia el Edge y GUARDA lo que salió. Es el único
// punto donde este e2e no ejecuta producción, y es inevitable: al otro lado hay un
// socket de WhatsApp.
type o5pSender struct {
	mu       sync.Mutex
	enviados []struct{ session, to, text string }
}

func (s *o5pSender) SendText(_ context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enviados = append(s.enviados, struct{ session, to, text string }{sessionID, to, text})
	return &cloudlinkv1.Ack{AckedCommandId: "o5p-cmd-1", Ok: true}, nil
}

func (s *o5pSender) SendMedia(_ context.Context, _, _, _, _, _, _, _ string) (*cloudlinkv1.Ack, error) {
	return &cloudlinkv1.Ack{AckedCommandId: "o5p-cmd-media", Ok: true}, nil
}

func (s *o5pSender) cuantos() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.enviados)
}

func (s *o5pSender) ultimo() (session, to, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.enviados) == 0 {
		return "", "", ""
	}
	u := s.enviados[len(s.enviados)-1]
	return u.session, u.to, u.text
}

// o5pKP construye el KeyProvider del e2e. Escritor y lector del self_pn comparten
// este objeto, como en producción comparten el keyring del proceso.
func o5pKP(t *testing.T) crypto.KeyProvider {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: "o5p-kek:ERERERERERERERERERERERERERERERERERERERERERE=",
		CurrentID:  "o5p-kek",
		IndexB64:   "RERERERERERERERERERERERERERERERERERERERERES=",
	})
	if err != nil {
		t.Fatalf("KeyProvider del e2e: %v", err)
	}
	return kp
}

// o5pSeedTenant crea el tenant del guion y registra su borrado (que arrastra
// fleet_sessions por ON DELETE CASCADE).
func o5pSeedTenant(ctx context.Context, t *testing.T, db *sql.DB) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`,
		"o5p-"+uuid.NewString()[:8], "O5P perfil pasivo e2e").Scan(&id); err != nil {
		t.Fatalf("sembrar tenant: %v", err)
	}
	//nolint:contextcheck // context.Background() a propósito: este cleanup corre DESPUÉS
	// del `defer cancelar()` del test, así que el ctx del guion ya está cancelado y la
	// limpieza no llegaría a ejecutarse. Un tenant huérfano contamina la base compartida.
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenants WHERE id = $1`, id); err != nil {
			t.Logf("limpiando tenant %s: %v", id, err)
		}
	})
	return id
}

// o5pFlow es el flujo que la regla keyword arrancaría SI la sesión reaccionara. Que
// exista y esté publicado es parte del montaje: si el flujo faltara, la fase 1 saldría
// verde por la razón equivocada (no arranca porque no hay qué arrancar).
func o5pFlow() model.Flow {
	return model.Flow{
		FlowID: "o5p-menu", Version: 1, Initial: "root",
		Nodes: map[string]model.Node{
			"root":   {Type: model.NodeTypeMenu, Prompt: "Hola\n1) Ventas", Options: map[string]string{"1": "ventas"}},
			"ventas": {Type: model.NodeTypeMessage, Text: "Ventas"},
		},
	}
}

// TestE2E_O5_PerfilPasivo_NoReaccionaPeroSiEnviaYAcusa es el guion entero.
//
// Fase 1 (la cara receptora, defensa en profundidad): un entrante que casaría la
// keyword del tenant NO crea conversación, NO auto-responde y NO deja evento.
// Fase 2 (la cara emisora, REQ-08): POST /api/v1/messages por ESA MISMA sesión sale.
// Fase 3 (el acuse, REQ-12): el DELIVERED de ese saliente vuelve, se persiste y se
// contabiliza.
// Fase 4 (REQ-09): el trabajo de flota del Heartbeat sigue corriendo sobre la pasiva
// y la sesión sigue siendo visible y operable — la privacidad es del CONTENIDO, no de
// la operación de la flota.
//
// 💥 MUTACIÓN QUE ENROJECE LA FASE 1: hacer que reactiveBlocked devuelva false para el
// perfil pasivo (incoming.go) ⇒ la sesión arranca el flujo y auto-responde.
// 💥 MUTACIÓN QUE ENROJECE LA FASE 2: meter una guarda de perfil en messagesHandler
// —la «simetría» que parece coherente y rompe el producto— ⇒ el 200 se vuelve 403 y
// el dueño se queda sin poder escribir desde su propio número.
func TestE2E_O5_PerfilPasivo_NoReaccionaPeroSiEnviaYAcusa(t *testing.T) {
	db := e2eOpenDB(t)
	ctx, cancelar := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelar()

	tenantID := o5pSeedTenant(ctx, t, db)
	kp := o5pKP(t)
	fleetRepo := fleet.NewPostgresRepository(db, crypto.NewFieldCipher(kp), kp)
	const edgeID, sessionID, telefonoPropio = "o5p-edge", "o5p-sess", "56984460000"
	const destinatario = "573004660000"

	// La sesión nace por el camino REAL y se pone pasiva por el camino REAL. Un
	// INSERT a mano dejaría fuera justo lo que decide el guion.
	if err := fleetRepo.MarkOnline(ctx, tenantID, edgeID, sessionID); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}
	if _, err := fleetRepo.SetProfile(ctx, tenantID, sessionID, fleet.ProfilePassive); err != nil {
		t.Fatalf("SetProfile(passive): %v", err)
	}

	sender := &o5pSender{}
	o5pFase1NoReacciona(ctx, t, db, tenantID, sessionID, sender)
	api := o5pAPI(t, fleetRepo, sender, tenantID)
	o5pFase2Envia(t, api, sender, sessionID, destinatario)
	o5pFase3Acusa(ctx, t, db, sessionID)
	o5pFase4Flota(ctx, t, api, fleetRepo, tenantID, edgeID, sessionID, telefonoPropio)
}

// o5pFase1NoReacciona monta el runtime de PRODUCCIÓN con el resolver de tenant que
// lee `profile` de fleet_sessions y le mete un entrante que casa la keyword.
//
// 🔴 EL RESOLVER ES EL REAL, y esa es la diferencia con passive_guard_test.go. Allí el
// perfil lo dicta un fakeResolver: el test afirma «si el resolver dice passive, el
// motor no reacciona». Aquí nadie dicta nada — el perfil sale de la FILA que SetProfile
// escribió, agregado por la consulta de producción. Si ese SELECT dejara de mirar la
// columna, allí no se enteraría nadie y aquí sí.
func o5pFase1NoReacciona(
	ctx context.Context, t *testing.T, db *sql.DB, tenantID, sessionID string, sender *o5pSender,
) {
	t.Helper()
	repo := flowstore.NewMemoryRepository()
	if _, err := repo.InsertDefinition(ctx, tenantID, o5pFlow()); err != nil {
		t.Fatalf("publicar el flujo del guion: %v", err)
	}
	reglas := trigger.NewMemoryStore()
	if _, err := reglas.Insert(ctx, trigger.Rule{
		TenantID: tenantID, Kind: trigger.KindKeyword, Keyword: "pedido",
		MatchType: trigger.MatchExact, FlowID: "o5p-menu", Enabled: true,
	}); err != nil {
		t.Fatalf("sembrar la regla keyword: %v", err)
	}

	reg := modules.NewRegistry()
	reg.Register(menu.New())
	contacts := contact.NewMemoryResolver(repo)
	rt := flowruntime.New(repo, engine.New(reg), sender,
		flowruntime.NewPostgresTenantResolver(db), // ← el de producción: lee la columna
		contacts, e2eLogger(),
		flowruntime.WithTriggerResolver(trigger.NewConfigResolver(reglas)))

	entrante := &cloudlinkv1.IncomingMessage{
		FromPn: "573004670000", Text: "pedido", WaMessageId: "wamid.o5p." + uuid.NewString()[:8],
	}
	antes := sender.cuantos()
	if err := rt.HandleIncoming(ctx, sessionID, entrante); err != nil {
		t.Fatalf("HandleIncoming sobre la sesión pasiva: %v", err)
	}

	if sender.cuantos() != antes {
		_, _, texto := sender.ultimo()
		t.Fatalf("la sesión PASIVA auto-respondió (%d → %d envíos, último %q). El perfil pasivo "+
			"promete que el motor no actúa: el corte cloud reactiveBlocked (D-046.7) dejó de cortar",
			antes, sender.cuantos(), texto)
	}
	cid, err := contacts.Resolve(ctx, tenantID, []contact.Ref{o5pRef(t, "573004670000")}, "")
	if err != nil {
		t.Fatalf("resolver el contacto del entrante: %v", err)
	}
	if _, vivo, lerr := repo.Load(ctx, flowstore.Key{
		TenantID: tenantID, SessionID: sessionID, ContactID: cid,
	}); lerr != nil || vivo {
		t.Fatalf("la sesión PASIVA creó conversación (vivo=%v err=%v): no basta con no responder, "+
			"tampoco debe quedar estado que reviva al reactivarla", vivo, lerr)
	}
	var eventos int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.flow_events WHERE tenant_id = $1`, tenantID).Scan(&eventos); err != nil {
		t.Fatalf("contar flow_events: %v", err)
	}
	if eventos != 0 {
		t.Fatalf("la sesión PASIVA dejó %d filas en public.flow_events, quiero 0: el motor entró "+
			"aunque no llegara a enviar", eventos)
	}
}

// o5pAPI monta el mux público real con el repositorio de flota REAL como SessionLister
// —para que la guarda de aislamiento por tenant consulte la tabla de verdad— y el
// sender doble como transporte.
func o5pAPI(t *testing.T, fleetRepo *fleet.PostgresRepository, sender *o5pSender, tenantID string) *testAPI {
	t.Helper()
	return newAPI(publicapi.Deps{
		Sender: sender,
		SessionDeps: publicapi.SessionDeps{
			Sessions:        fleetRepo,
			SessionProfiles: fleetRepo,
		},
	}, map[string]testIdentity{
		"o5p-operador": {Subject: "o5p-op", TenantID: tenantID, Grants: []string{"*"}},
	})
}

// o5pFase2Envia es REQ-08: el envío por una sesión pasiva SALE. Es «el caso que la
// gente se salta» y es la mitad del valor del perfil: una pasiva que no pudiera enviar
// no serviría para nada — el dueño la usa justo para escribir desde su número personal
// sin que el bot conteste por él.
func o5pFase2Envia(t *testing.T, api *testAPI, sender *o5pSender, sessionID, destinatario string) {
	t.Helper()
	antes := sender.cuantos()
	cuerpo := fmt.Sprintf(`{"session_id":%q,"to":%q,"text":%q}`, sessionID, destinatario, "hola desde la pasiva")
	rec := call(api, "o5p-operador", http.MethodPost, "/api/v1/messages", cuerpo)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/messages por la sesión pasiva = %d, quiero 200. Cuerpo: %s\n"+
			"El perfil pasivo gobierna el motor REACTIVO, no la emisión: si esta ruta empieza a "+
			"mirar el perfil, el dueño se queda sin poder escribir desde su propio número (REQ-08)",
			rec.Code, rec.Body.String())
	}
	if sender.cuantos() != antes+1 {
		t.Fatalf("el 200 llegó pero el comando NO salió hacia el Edge (%d → %d envíos): "+
			"el handler respondió sin empujar nada", antes, sender.cuantos())
	}
	gotSesion, gotTo, gotTexto := sender.ultimo()
	if gotSesion != sessionID || gotTo != destinatario || gotTexto != "hola desde la pasiva" {
		t.Fatalf("el SendText salió con otros datos: sesión=%q to=%q texto=%q", gotSesion, gotTo, gotTexto)
	}
}

// o5pFase3Acusa es REQ-12: el acuse DELIVERED del saliente vuelve, SE PERSISTE en
// public.message_receipts y SE CONTABILIZA en la métrica de negocio.
//
// 🔴 SE AFIRMAN LAS DOS COSAS, y no es redundancia: el sink llama al hook DESPUÉS de
// guardar, así que un fallo de persistencia que se tragara el error dejaría la métrica
// subiendo sobre una tabla vacía. Un contador que cuenta lo que no se guardó es peor
// que no contar.
func o5pFase3Acusa(ctx context.Context, t *testing.T, db *sql.DB, sessionID string) {
	t.Helper()
	var contados []string
	sink := receipts.NewSink(receipts.NewPostgresStore(db), func(status string) {
		contados = append(contados, status)
	})
	const messageID = "wamid.o5p.saliente"
	//nolint:contextcheck // context.Background() a propósito, por lo mismo que el cleanup
	// del tenant: corre tras el `defer cancelar()`. Y aquí importa el doble — los acuses
	// no cuelgan del tenant, así que ningún CASCADE los barrería.
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.message_receipts WHERE session_id = $1`, sessionID); err != nil {
			t.Logf("limpiando message_receipts: %v", err)
		}
	})

	if err := sink.Record(ctx, &cloudlinkv1.MessageReceipt{
		SessionId:  sessionID,
		CommandId:  "o5p-cmd-1",
		MessageIds: []string{messageID},
		Status:     cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED,
		Timestamp:  time.Now().Unix(),
	}); err != nil {
		t.Fatalf("Record del acuse DELIVERED: %v", err)
	}

	var status string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM public.message_receipts WHERE session_id = $1 AND message_id = $2`,
		sessionID, messageID).Scan(&status); err != nil {
		t.Fatalf("el acuse del saliente de la pasiva NO llegó a public.message_receipts: %v.\n"+
			"El acuse cierra el lazo: sin él, el dueño envía a ciegas y no sabe si su mensaje "+
			"salió del teléfono (REQ-12)", err)
	}
	if status != "delivered" {
		t.Fatalf("acuse persistido con status %q, quiero delivered", status)
	}
	if len(contados) != 1 || contados[0] != "delivered" {
		t.Fatalf("la métrica de negocio no contó el acuse: %v. El hook corre DESPUÉS de guardar, "+
			"así que persistido-y-no-contado significa que alguien lo desenchufó", contados)
	}
}

// o5pFase4Flota es REQ-09: el trabajo que el Heartbeat dispara en la nube sigue
// corriendo sobre una sesión pasiva, y la sesión sigue siendo visible y operable.
//
// Se ejercen las dos escrituras de flota que hace un latido —SetSelfPn (el número
// propio) y SaveHealth (la salud)— y se lee el resultado por la API PÚBLICA, que es
// donde lo ve el dueño. No se dobla el stream gRPC: lo que se prueba es que ninguna de
// esas rutas consulta el perfil, no el transporte que las invoca.
//
// 🔴 LA PRIVACIDAD ES DEL CONTENIDO, NO DE LA OPERACIÓN. Una pasiva que desapareciera
// del dashboard o dejara de reportar salud sería inoperable: el dueño no sabría si su
// teléfono sigue conectado. Si alguien «endureciera» el perfil pasivo apagando también
// la telemetría de flota, esta fase es lo que se pone rojo.
func o5pFase4Flota(
	ctx context.Context, t *testing.T, api *testAPI, fleetRepo *fleet.PostgresRepository,
	tenantID, edgeID, sessionID, telefonoPropio string,
) {
	t.Helper()
	if err := fleetRepo.SetSelfPn(ctx, tenantID, edgeID, sessionID, telefonoPropio); err != nil {
		t.Fatalf("SetSelfPn sobre la sesión pasiva (trabajo del Heartbeat): %v", err)
	}
	if err := fleetRepo.SaveHealth(ctx, tenantID, edgeID, sessionID, fleet.HealthSnapshot{
		WhatsappState: "connected", BinaryVersion: "o5p-test", UptimeS: 42,
	}); err != nil {
		t.Fatalf("SaveHealth sobre la sesión pasiva (trabajo del Heartbeat): %v", err)
	}

	rec := call(api, "o5p-operador", http.MethodGet, "/api/v1/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/sessions = %d, quiero 200: %s", rec.Code, rec.Body.String())
	}
	// El listado es un ARRAY pelado, no un objeto con clave: se decodifica como tal.
	var sesiones []struct {
		SessionID string `json:"session_id"`
		Profile   string `json:"profile"`
		State     string `json:"state"`
		SelfPn    string `json:"self_pn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sesiones); err != nil {
		t.Fatalf("decodificar el listado de sesiones: %v — %s", err, rec.Body.String())
	}
	for _, s := range sesiones {
		if s.SessionID != sessionID {
			continue
		}
		if s.Profile != "passive" {
			t.Fatalf("la sesión aparece con profile=%q tras el guion, quiero passive: algo del "+
				"camino de envío o de acuse le cambió el eje", s.Profile)
		}
		if s.State != "online" {
			t.Fatalf("la sesión pasiva aparece con state=%q, quiero online: el trabajo de flota "+
				"del Heartbeat dejó de mantenerla viva y el dueño la ve caída", s.State)
		}
		if s.SelfPn != telefonoPropio {
			t.Fatalf("el listado devuelve self_pn=%q, quiero el número que persistió el Heartbeat. "+
				"Vacío significa que el sobre cifrado no abrió (Plan 046 · T4.1)", s.SelfPn)
		}
		return
	}
	t.Fatalf("la sesión pasiva NO aparece en GET /api/v1/sessions (%d listadas). Una pasiva que "+
		"desaparece del dashboard es inoperable: el dueño no sabe si su teléfono sigue conectado",
		len(sesiones))
}

// o5pRef arma la referencia de teléfono del contacto del guion.
func o5pRef(t *testing.T, valor string) contact.Ref {
	t.Helper()
	ref, err := contact.NewRef(contact.KindPhoneE164, valor)
	if err != nil {
		t.Fatalf("contact.NewRef(%s): %v", valor, err)
	}
	return ref
}
