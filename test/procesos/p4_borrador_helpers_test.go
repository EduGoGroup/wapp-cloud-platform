//go:build integracion

package procesos

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// Los helpers REUTILIZABLES de «de mensaje a borrador» (T9.17). Los usan P4 y, en la fase siguiente,
// P5, P6, P7 y P8: todo proceso que necesite una solicitud en `pending_approval` nacida del pipeline
// LLM. La firma es estable:
//
//	sc := draftScenario(t, "p5", "p5-bandeja")       // empresa, Edge conectado y READY, catálogo, flujo, guion
//	id := createDraft(t, sc, "573005550001")         // una ráfaga de ese contacto → el id de su borrador
//	r := sc.Pub.Get(t, "/api/v1/intakes/"+id, nil)   // la API pública, con el token de la administradora
//	sc.Script.Respond(stageP5, scriptQuoteText(t, "…")) // el guion se cambia en caliente
//	sc.Script.Delay(stageP5, 12*time.Second)
//
// ⚠️ Un draftScene NO es concurrente: createDraft y sendDraftBurst leen los textos del Edge en orden
// y mueven los plazos de la ventana de la empresa. Los borradores de una escena se crean de uno en uno,
// y con el `t` del proceso o de un subtest NO paralelo.

const (
	// draftPlan es el plan de la empresa del escenario: `advisor_ai_local`, sembrado por la migración
	// 0074. De los planes sembrados (0039: basic, pro, commerce, advisor_ai, advisor_ai_pro; 0074:
	// advisor_ai_local) es el que trae `llm_intake` y `cart_basic` (y `catalog_import`, para cargar el
	// catálogo) SIN `api_llm`: la captación corre por la vía local —el Edge de prueba— y la vía de pago
	// queda cerrada por plan (cero gasto). Es también el plan por defecto del arnés.
	draftPlan = planTenantPorDefecto

	// draftCatalogRef es la ref de `tenant_content` de la que el pipeline lee el catálogo
	// (internal/intake/catalogo: RefCatalogo).
	draftCatalogRef = "catalogo"

	// draftFlowID es el flujo del escenario y draftKeyword la palabra que abre su evento.
	draftFlowID  = "pedido"
	draftKeyword = "presupuesto"
	// draftEventKind es el tipo del evento conversacional que abre el disparo. Sin evento vivo no hay
	// ventana de captación (intake_jobs.event_id es NOT NULL): un saludo suelto no abre nada.
	draftEventKind = "cart"

	// draftMenuPrompt es lo que el flujo contesta al mensaje que abre el evento, y draftMenuInvalid lo
	// que contesta a cada mensaje siguiente de la ráfaga: el flujo es un menú numerado y el cliente
	// escribe texto libre. Lo que interpreta el pedido no es el flujo: es el pipeline, después.
	draftMenuPrompt  = "Cuéntanos qué necesitas y te pasamos el presupuesto.\n1) Hablar con una persona"
	draftMenuInvalid = "Opción no válida. Responde con el número de una de las opciones.\n\n" + draftMenuPrompt
	// draftMenuExit es el texto del nodo al que lleva la opción 1.
	draftMenuExit = "Te escribe una persona en un momento."

	// draftHoldSeconds es el plazo (silencio y techo) con el que la ventana NO se cierra mientras se
	// manda una ráfaga.
	draftHoldSeconds = 3600
	// draftTimeout es el tope de las esperas del pipeline: barrido cada 5 s, worker cada 5 s (T-6).
	draftTimeout = 60 * time.Second
	// draftPollEvery es cada cuánto se sondea Postgres mientras se espera al pipeline.
	draftPollEvery = 250 * time.Millisecond

	// draftJobQuery lee el job de captación que lleva un wa_message_id en sus referencias.
	draftJobQuery = `SELECT status || '|' || jsonb_array_length(source_refs)::text FROM public.intake_jobs
		WHERE tenant_id = $1 AND source_refs ? $2`
)

// draftCatalog es el catálogo del escenario, en el contrato de POST /api/v1/catalog/import: la carta
// del caso Ámbar (`fusCatalogoDeFusion`, internal/intake/pipeline/guion_ambar_test.go) —la torta de
// chocolate se vende por presentaciones, la de vainilla y los tequeños a precio fijo— más tres
// artículos de SKU adversario (separador repetido, espacio U+00A0, dígitos no ASCII).
func draftCatalog() map[string]any {
	item := func(code, sku, label string, price float64) map[string]any {
		return map[string]any{"code": code, "sku": sku, "label": label, "price": price}
	}
	choc := item("1", "TORTA-CHOC", "Torta de chocolate", 2100)
	choc["variants"] = []any{
		map[string]any{"code": "10", "label": "10 porciones", "price": 2100},
		map[string]any{"code": "12", "label": "12 porciones", "price": 2400},
		map[string]any{"code": "25", "label": "25 porciones", "price": 3900},
	}
	return map[string]any{
		"format": "wapp.catalog_import", "version": 1,
		"catalog": map[string]any{"categories": []any{
			map[string]any{"code": "1", "label": "Tortas", "items": []any{
				choc, item("2", "TORTA-VAIN", "Torta de vainilla", 3900),
			}},
			map[string]any{"code": "2", "label": "Congelados", "items": []any{
				item("1", "TEQ-30", "Tequeños congelados", 490),
			}},
			map[string]any{"code": "3", "label": "Panes", "items": []any{
				item("1", "PAN@@1", "Pan de masa madre", 120),
				item("2", "PAN\u00a02", "Pan\u00a0integral", 130),
				item("3", "PAN-١٢٣", "Pan ١٢٣", 140),
			}},
		}},
	}
}

// draftCatalogItems es cuántos artículos trae draftCatalog.
const draftCatalogItems = 6

// draftBurst es la ráfaga canónica: el caso Ámbar en tres mensajes. El primero lleva la palabra que
// abre el evento; las `evidence` del guion (scriptEvidence…) son subcadenas literales de los tres.
func draftBurst() []string {
	return []string{
		"Hola, buenas! Te quería pedir un presupuesto para el miércoles de la semana que viene",
		"Serían 2 tortas. Una torta sería con decoración infantil, de bizcocho húmedo de chocolate con crema de chocolate, de 10 o 12 porciones",
		"Y la otra de bizcocho de vainilla que tenga lluvia de colores, con dulce de leche y merengue, de 25 o 30 porciones. " +
			"También quería un paquete de tequeños congelados de 30",
	}
}

// draftScene es el escenario: lo de p3Scene (servidor, base, empresa, tokens, Edge conectado y el
// cliente de la API pública de la administradora) más el guion de inferencia del Edge.
type draftScene struct {
	p3Scene
	// Script es el guion que el Edge consulta: nace con el del caso Ámbar (newAmbarScript).
	Script *inferenceScript

	mu   sync.Mutex
	beat int64
}

// draftScenario arranca un servidor con su base para el proceso dado y deja lista una empresa slug
// para captar pedidos con LLM: plan draftPlan, administradora, un Edge enrolado y conectado con lease
// vigente que late READY y contesta la inferencia con el guion del caso Ámbar, el catálogo cargado
// por POST /api/v1/catalog/import, el flujo y su disparo `event_start`, y la sesión en perfil activo.
// Los plazos de la ventana quedan en los de fábrica: los mueve sendDraftBurst/flushDraftWindow. Falla
// (t.Fatalf) si algo de eso no sale. Hay que llamarla con el `t` del proceso (el Edge se ata a él).
func draftScenario(t *testing.T, proceso, slug string) *draftScene {
	t.Helper()
	esc := edgeEscenarioNuevo(t, proceso, slug, true)
	script := newAmbarScript(t)
	e := enrolar(t, esc.S, edgeEmitirCodigo(t, esc.S, esc.TokenStaff, esc.Tenant))
	e.Inferir = script.Infer
	e.conectar(t)
	e.esperarLeases(t, 2, edgeTopeFila)
	if !e.puedeOperar() {
		t.Fatalf("draftScenario: el Edge recién conectado no puede operar")
	}
	e.esperarConfig(t, "filters", edgeTopeFila)
	sc := &draftScene{
		p3Scene: p3Scene{edgeEscenario: esc, Edge: e, Pub: esc.S.Publica(esc.TokenAdmin)},
		Script:  script,
		beat:    5,
	}

	r := sc.Pub.Post(t, "/api/v1/catalog/import?mode=apply&ref="+draftCatalogRef, draftCatalog())
	var imported struct {
		Ref     string `json:"ref"`
		Applied bool   `json:"applied"`
		Items   int    `json:"items"`
	}
	r.JSON(t, &imported)
	if r.Codigo != http.StatusOK || !imported.Applied || imported.Ref != draftCatalogRef || imported.Items != draftCatalogItems {
		t.Fatalf("draftScenario: importar el catálogo: HTTP %d %+v, quería 200 aplicado con %d artículos\ncuerpo: %s",
			r.Codigo, imported, draftCatalogItems, recortar(r.Cuerpo))
	}

	def := map[string]any{
		"flow_id": draftFlowID, "version": 1, "initial": "root",
		"nodes": map[string]any{
			"root": map[string]any{"type": "menu", "prompt": draftMenuPrompt, "options": map[string]string{"1": "fin"}},
			"fin":  map[string]any{"type": "message", "text": draftMenuExit, "next": nil},
		},
	}
	if r = sc.Pub.Post(t, "/api/v1/flows", map[string]any{"definition": def}); r.Codigo != http.StatusCreated {
		t.Fatalf("draftScenario: publicar el flujo: HTTP %d, quería 201\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	r = sc.Pub.Post(t, "/api/v1/triggers", map[string]any{
		"kind": "event_start", "keyword": draftKeyword, "match_type": "contains",
		"event_kind": draftEventKind, "flow_id": draftFlowID,
	})
	if r.Codigo != http.StatusCreated {
		t.Fatalf("draftScenario: crear el disparo event_start: HTTP %d, quería 201\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	sc.setProfile(t, "active")
	return sc
}

// draftWaID es el wa_message_id del mensaje i de la ráfaga de un contacto: único por contacto, así
// que sirve además para encontrar su job (intake_jobs.source_refs). Es OPACO, como los de WhatsApp: se
// deriva del contacto por hash, no lo lleva dentro (un teléfono en un wa_message_id acabaría en el log
// del servidor, que sí escribe las referencias).
func draftWaID(contact string, i int) string {
	sum := sha256.Sum256([]byte(contact))
	return fmt.Sprintf("DRAFT-%X-%d", sum[:6], i)
}

// createDraft lleva la ráfaga canónica (draftBurst) del contacto dado —un número de teléfono en
// dígitos, nuevo en la empresa— hasta un borrador y devuelve el id de la solicitud (`intakes.id`), ya
// en `pending_approval` con su revisión 1. Con el guion del caso Ámbar tarda lo que el barrido y el
// worker (≤ 5 s cada uno). Falla (t.Fatalf) si la ráfaga no se contesta como se espera o el borrador
// no llega en draftTimeout. Deja la ventana de la empresa en 0/0 (ver flushDraftWindow).
func createDraft(t *testing.T, sc *draftScene, contact string) (intakeID string) {
	t.Helper()
	sendDraftBurst(t, sc, contact, draftBurst())
	flushDraftWindow(t, sc)
	_, intakeID = waitDraft(t, sc, contact)
	return intakeID
}

// sendDraftBurst manda texts como una ráfaga del contacto (número en dígitos, nuevo en la empresa;
// el primer texto tiene que casar el disparo: contener draftKeyword) y vuelve cuando todos están en
// UNA ventana de captación abierta. Devuelve sus wa_message_id (draftWaID).
//
// Va mensaje a mensaje, esperando la respuesta del flujo a cada uno: el servidor atiende cada
// entrante en su goroutine, y sin esa espera el orden de la ráfaga no estaría garantizado. Antes de
// empezar deja los plazos de la ventana en draftHoldSeconds (holdDraftWindow), para que el barrido
// —cada 5 s— no la cierre a media ráfaga. El flujo admite a lo sumo tres mensajes por ráfaga: el
// tope de auto-respuestas por conversación (ráfaga de 3) cortaría la cuarta. Falla (t.Fatalf) si una
// respuesta no es la esperada o la ventana no queda con todas las referencias.
func sendDraftBurst(t *testing.T, sc *draftScene, contact string, texts []string) []string {
	t.Helper()
	sc.mu.Lock()
	defer sc.mu.Unlock()
	holdDraftWindow(t, sc.DB, sc.Tenant)
	ids := make([]string, len(texts))
	for i, text := range texts {
		ids[i] = draftWaID(contact, i)
		sc.Edge.sendSealedIncoming(t, sealedIncoming{From: contact + "@s.whatsapp.net", WaID: ids[i], Text: text, FromPn: contact})
		if i == 0 {
			sc.expectText(t, contact, p3Welcome)
			sc.expectText(t, contact, draftMenuPrompt)
			continue
		}
		sc.expectText(t, contact, draftMenuInvalid)
	}
	draftWaitJob(t, sc, ids[0], fmt.Sprintf("aggregating|%d", len(ids)), "la ventana abierta con toda la ráfaga")
	return ids
}

// flushDraftWindow deja la ventana de la empresa en 0 s de silencio y 0 s de techo (ventanaInmediata):
// el siguiente barrido del agregador cierra TODAS sus ventanas abiertas y el pipeline arranca. Los
// plazos se leen en el barrido, no al abrir la ventana, así que vale hacerlo con la ráfaga ya dentro.
func flushDraftWindow(t *testing.T, sc *draftScene) {
	t.Helper()
	sc.mu.Lock()
	defer sc.mu.Unlock()
	ventanaInmediata(t, sc.DB, sc.Tenant)
}

// waitDraft espera, con tope draftTimeout, a que el job de la ráfaga del contacto termine (`done`) con
// su solicitud, y devuelve el id del job y el de la solicitud. Falla (t.Fatalf) con el último estado
// visto («estado/etapa/intentos») si no llega.
func waitDraft(t *testing.T, sc *draftScene, contact string) (jobID, intakeID string) {
	t.Helper()
	const query = `SELECT id::text, coalesce(intake_id::text, ''), status || '/' || coalesce(stage, '-') || '/' || attempts::text
		FROM public.intake_jobs WHERE tenant_id = $1 AND source_refs ? $2`
	last := "sin fila"
	if !draftPoll(t, draftTimeout, func() bool {
		var state string
		if err := sc.DB.QueryRowContext(t.Context(), query, sc.Tenant, draftWaID(contact, 0)).Scan(&jobID, &intakeID, &state); err != nil {
			last = "error: " + err.Error()
			return false
		}
		last = state
		return strings.HasPrefix(state, "done/") && intakeID != ""
	}) {
		t.Fatalf("el borrador de la ráfaga de %s no llegó en %s: el job quedó en %q", contact, draftTimeout, last)
	}
	return jobID, intakeID
}

// draftWaitJob espera, con tope draftTimeout, a que el job que lleva waID en sus referencias valga
// want en draftJobQuery («estado|nº de referencias»). what dice qué se espera, para el mensaje.
func draftWaitJob(t *testing.T, sc *draftScene, waID, want, what string) {
	t.Helper()
	last := ""
	if !draftPoll(t, draftTimeout, func() bool {
		last = p9Scalar(t, sc.DB, draftJobQuery, sc.Tenant, waID)
		return last == want
	}) {
		t.Fatalf("%s: pasaron %s y el job de %s vale %q, quería %q", what, draftTimeout, waID, last, want)
	}
}

// draftPoll sondea cond cada draftPollEvery hasta que dé true o pase timeout (o termine el test).
// Devuelve si llegó a cumplirse. Es la espera de todo lo que depende del barrido y del worker (T-6).
func draftPoll(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(draftPollEvery)
	defer tick.Stop()
	for {
		if cond() {
			return true
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			return cond()
		case <-t.Context().Done():
			return false
		}
	}
}

// holdDraftWindow deja los plazos de la ventana de captación de la empresa (silencio y techo) en
// draftHoldSeconds: mientras valgan eso, el barrido no cierra ninguna ventana. Es un upsert sobre
// tenant_settings, como ventanaInmediata. Falla (t.Fatalf) si la empresa no es un UUID o el SQL falla.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: la misma razón que ventanaInmediata (fixtures_test.go): ninguna ruta
// escribe estos dos plazos; hoy los cambia el operador por SQL.
func holdDraftWindow(t *testing.T, db *sql.DB, tenant string) {
	t.Helper()
	if err := exigirUUID("la empresa", tenant); err != nil {
		t.Fatalf("holdDraftWindow: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), topeFixture)
	defer cancel()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings (tenant_id, aggregation_window_seconds, aggregation_max_seconds)
		VALUES ($1, $2, $2)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET aggregation_window_seconds = $2, aggregation_max_seconds = $2`, tenant, draftHoldSeconds); err != nil {
		t.Fatalf("holdDraftWindow: escribir tenant_settings de %s: %v", tenant, err)
	}
}

// beatReadiness manda un latido con la disponibilidad de inferencia dada (READY o DOWN) y espera a la
// renovación del lease que provoca. Los contadores de lease los lleva la escena. Un latido DOWN seguido
// de uno READY es el flanco que despierta al worker del pipeline sin esperar a su backoff.
func (sc *draftScene) beatReadiness(t *testing.T, r cloudlinkv1.InferenceReadiness) {
	t.Helper()
	sc.mu.Lock()
	counter := sc.beat
	sc.beat += 2
	sc.mu.Unlock()
	before := sc.Edge.Leases()
	hb := edgeLatido(sc.Edge.SessionID, counter)
	hb.GetHeartbeat().InferenceReadiness = r
	if err := sc.Edge.emitir(hb); err != nil {
		t.Fatalf("latido con disponibilidad %s: %v", r, err)
	}
	sc.Edge.esperarLeases(t, before+1, edgeTopeFila)
}

// requireNoJobWithoutLiteral exige que NINGÚN job de la empresa haya muerto por falta de literal: cero
// filas `failed` de intake_jobs cuyo error diga «no trae literal que analizar». Es la huella de D-F7-9:
// el worker reclamó el job de una ventana ya cerrada (`pending`) cuyo sobre todavía no se había
// escrito (o el de un re-análisis recién abierto). Ningún paso de P4, P6 ni P8 —los tres la llaman en
// su cierre— produce esa fila a propósito: toda ventana tiene mensajes de texto del cliente, así que
// todo job que cierra, o que nace de un re-análisis, tiene su sobre.
//
// La aserción vale para los dos binarios y no distingue entre ellos. El nuevo la garantiza: cierra la
// ventana y guarda el sobre en una sola sentencia, y el job de re-análisis nace con el suyo (F8-06b). El viejo conserva la carrera hasta su
// borrado en F10 y puede ponerse rojo aquí de forma intermitente, igual que ya lo hacía por la línea
// ERROR de su log (hallazgo 14 de F8).
func requireNoJobWithoutLiteral(t *testing.T, sc *draftScene) {
	t.Helper()
	const query = `SELECT count(*) FROM public.intake_jobs
		WHERE tenant_id = $1 AND status = 'failed' AND error LIKE '%no trae literal que analizar%'`
	if n := consultaEntero(t, sc.DB, query, sc.Tenant); n != 0 {
		t.Errorf("intake_jobs tiene %d jobs `failed` por «no trae literal que analizar», quería 0: es D-F7-9, el worker reclamó una ventana cerrada antes de que tuviera su sobre", n)
	}
}
