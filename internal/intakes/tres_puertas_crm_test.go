// tres_puertas_crm_test.go — el criterio (a) de T4.10 mitad 2, el único que ningún
// otro test de esta ola podía dar por cerrado.
//
// # QUÉ FALTABA, EXACTAMENTE
//
// Las tres puertas que empujan revisiones al puente CRM ya tenían un test CADA UNA
// —`TestApprove_EmpujaAlCRMConElRevisionNoRealYElEstadoReal`,
// `TestCorregir_ElEmpujeCuelgaDeLaRevisiónYNoDelCampo` y, para el re-análisis,
// `TestDraft_ElReanalisisEmpujaAlCRM` más el candado por AST de
// `internal/bootstrap/reanalisis_cableado_test.go`—. Ninguno recorría las tres
// SEGUIDAS sobre la MISMA solicitud, y esa es la única forma de ver lo que el
// criterio pide: que el `revision_no` crezca ESTRICTAMENTE **entre** puertas. Cada
// test por separado solo puede afirmar que su puerta empuja el número que su propia
// escritura acaba de generar; que la puerta siguiente no lo repita, no lo pisa y no
// retrocede es una propiedad del conjunto, y el conjunto no lo miraba nadie.
//
// 🔴 LA AFIRMACIÓN VA SOBRE EL CUERPO ENTREGADO, NO SOBRE EL PUERTO. Los tres tests
// anteriores miran un doble de `intakes.CRMPusher` (`crmSpy`), o sea lo que el
// SERVICE le pasó al adaptador. Eso deja fuera el tramo que de verdad falló dos veces
// en esta ola: `crmpush.Build`, que es donde vivían el `revision_no: 1` literal y el
// `lifecycle_status: "confirmed"` literal. Aquí el `CRMPusher` es el REAL
// (`crmpush.NewRevisionPusher` sobre `crmpush.NewPusher`) y lo que se inspecciona es
// el JSON que entra en `webhook_outbox` — el documento que el puente del cliente va a
// leer. Nada se afirma leyendo un log.
//
// 🔴 Y LOS TRES NÚMEROS SE LEEN DEL STORE, NO SE CUENTAN. Después de cada puerta el
// test pregunta al almacén cuál fue el `revision_no` que ASIGNÓ, y compara el cuerpo
// contra ese número. Un contador del test (1, 2, 3…) volvería a pasar aunque el
// contrato emitiera un literal, que es precisamente el defecto que T4.10 arregló.
package intakes_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations/crmpush"
)

// eventoDeLasTresPuertas es el evento conversacional del que cuelga la solicitud. El
// re-análisis lo necesita porque la etapa `draft` resuelve la cabecera POR EVENTO
// (`AlmacenSolicitudes.GetIntakeByEvent`), no por id de solicitud.
const eventoDeLasTresPuertas = "44444444-4444-4444-4444-444444444444"

// ---------------------------------------------------------------------------
// LA COLA DE VERDAD: LO QUE SE INSPECCIONA ES ESTO
// ---------------------------------------------------------------------------

// encolado es UNA fila de `webhook_outbox` tal como la escribiría el INSERT.
type encolado struct {
	tenantID string
	kind     string
	payload  json.RawMessage
}

// colaDelPuente satisface `crmpush.Queuer`. Guarda el cuerpo TAL CUAL, sin
// interpretarlo: quien lo interpreta es el test, deserializándolo a `crmpush.Payload`
// igual que hará el puente del cliente.
type colaDelPuente struct {
	mu    sync.Mutex
	filas []encolado
}

func (c *colaDelPuente) EnqueueWebhook(_ context.Context, tenantID, kind string,
	payload json.RawMessage) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.filas = append(c.filas, encolado{tenantID: tenantID, kind: kind, payload: payload})
	return int64(len(c.filas)), nil
}

func (c *colaDelPuente) todas() []encolado {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]encolado(nil), c.filas...)
}

// gateDelPuenteAbierto satisface `crmpush.Gate`: el tenant tiene el puente CRM
// activo. El gate CERRADO ya lo cubren los tests de `crmpush`; aquí estorbaría.
type gateDelPuenteAbierto struct{}

func (gateDelPuenteAbierto) Enabled(context.Context, string) (bool, error) { return true, nil }

// stageStoreQueAcepta satisface `stages.StageStore`. Devuelve SIEMPRE `true`: si
// devolviera `false`, la etapa cortaría con `ErrJobFueraDeProcessing` DESPUÉS de haber
// escrito la revisión y empujado, y el test estaría midiendo otra cosa.
type stageStoreQueAcepta struct{}

func (stageStoreQueAcepta) SaveStage(context.Context, string, intake.Artifact) (bool, error) {
	return true, nil
}

// ---------------------------------------------------------------------------
// LA ESCENA
// ---------------------------------------------------------------------------

// escenaTresPuertas monta UNA solicitud y el puente CRM real detrás del Service.
type escenaTresPuertas struct {
	svc    *intakes.Service
	store  *intakes.MemoryStore
	flujos *store.MemoryRepository
	cola   *colaDelPuente
	log    *logSpy
}

func nuevaEscenaTresPuertas(t *testing.T) *escenaTresPuertas {
	t.Helper()
	st := seedStore(t, intakes.StatusPendingApproval)
	cola := &colaDelPuente{}
	log := &logSpy{}

	// 🔴 EL PUSHER ES EL DE PRODUCCIÓN, NO UN DOBLE. Es lo que pone bajo prueba a
	// `crmpush.Build`, que es donde estaban los dos literales que esta tarea mató.
	svc := intakes.NewService(st,
		intakes.WithQuoteSender(newNotifier(&stubSender{}, st, log)),
		intakes.WithCRMPusher(crmpush.NewRevisionPusher(
			crmpush.NewPusher(log, cola, gateDelPuenteAbierto{}), log)))

	// La cabecera del lado de los FLUJOS: es la que consulta la etapa `draft` para
	// saber que el evento YA tiene contenido durable y colgar su revisión de esta
	// solicitud en vez de parir otra.
	flujos := store.NewMemoryRepository()
	if err := flujos.UpsertIntake(context.Background(), store.Intake{
		ID: intakeDePrueba, TenantID: tenantA, ContactID: "contacto-opaco-1",
		SessionID: "sess-a", Status: "open", EventID: eventoDeLasTresPuertas,
	}); err != nil {
		t.Fatalf("sembrar la cabecera del evento: %v", err)
	}

	return &escenaTresPuertas{svc: svc, store: st, flujos: flujos, cola: cola, log: log}
}

// revisiónVigente devuelve el `revision_no` que el ALMACÉN acaba de asignar. Es la
// pieza que impide que este test se vuelva tautológico: el número contra el que se
// compara el cuerpo sale del store, no de una variable que el test incrementa.
func (e *escenaTresPuertas) revisiónVigente(t *testing.T) int {
	t.Helper()
	return últimaRevisión(t, e.store.Revisions(intakeDePrueba)).RevisionNo
}

// reAnalizar recorre la TERCERA puerta con la etapa REAL del pipeline: la `draft` de
// `internal/intake/stages`, cableada con `ConEmpujeCRM(svc)` exactamente como la
// cablea `bootstrap.go`. No hay ningún doble en el camino del empuje.
//
// Lo único fabricado es la ENTRADA de la etapa (el artefacto del match y la marca de
// re-análisis del job), que es lo que en producción traen las etapas anteriores. Eso
// no es simular la puerta: la puerta es lo que va de `Run` a `PushRevisionByID`, y eso
// corre entero.
func (e *escenaTresPuertas) reAnalizar(t *testing.T, desdeRevisión int) {
	t.Helper()
	draft, err := stages.NewDraft(e.log, stageStoreQueAcepta{}, e.flujos, e.store, e.flujos,
		stages.ConEmpujeCRM(e.svc))
	if err != nil {
		t.Fatalf("construir la etapa draft: %v", err)
	}

	precio := 22000.0
	job := intake.ClaimedJob{
		ID: "job-reanalisis-t410",
		Key: intake.WindowKey{
			TenantID: tenantA, SessionID: "sess-a", ContactID: "contacto-opaco-1",
			EventID: eventoDeLasTresPuertas,
		},
		MessageTS: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
		// La marca del dueño: sin ella `empujarAlCRM` sale por la puerta en su primera
		// línea y no habría tercer empuje que mirar.
		Reanalisis: intake.Reanalisis{
			RequestedBy: intake.RequestedByOwner,
			Via:         "local",
			Source:      stages.OrigenHiloDelEvento,
			From:        desdeRevisión,
		},
	}
	if _, err := draft.Run(context.Background(), job, stages.EntradaDraft{
		Match: &stages.ArtefactoMatch{
			Version: llm.ArtifactVersion,
			Lines: []stages.Linea{{
				Kind: stages.KindMatched, SKU: "torta-v1", Label: "Torta 15 porciones",
				Qty: 1, UnitPrice: &precio,
			}},
		},
	}); err != nil {
		t.Fatalf("re-analizar: %v\nlog:\n%s", err, e.log.all())
	}
}

// cuerpos deserializa lo encolado. Comprueba de paso el `kind` del enrutado, porque un
// documento perfecto bajo un kind equivocado no lo entrega nadie.
func (e *escenaTresPuertas) cuerpos(t *testing.T) []crmpush.Payload {
	t.Helper()
	filas := e.cola.todas()
	out := make([]crmpush.Payload, 0, len(filas))
	for i, f := range filas {
		if f.kind != crmpush.Kind {
			t.Fatalf("el empuje %d se encoló con kind %q y el worker enruta por %q", i+1, f.kind, crmpush.Kind)
		}
		if f.tenantID != tenantA {
			t.Fatalf("el empuje %d se encoló al tenant %q", i+1, f.tenantID)
		}
		var p crmpush.Payload
		if err := json.Unmarshal(f.payload, &p); err != nil {
			t.Fatalf("el cuerpo %d no es el documento del contrato: %v\n%s", i+1, err, f.payload)
		}
		out = append(out, p)
	}
	return out
}

// ---------------------------------------------------------------------------
// EL TEST
// ---------------------------------------------------------------------------

// TestT410_LasTresPuertasEmpujanRevisionesEstrictamenteCrecientes es el criterio (a)
// de T4.10 mitad 2, de extremo a extremo y sobre la MISMA solicitud: aprobar, corregir
// y re-analizar dejan TRES documentos en la cola del puente, con `revision_no`
// estrictamente creciente y con el `lifecycle_status` que la solicitud tenía en cada
// momento.
//
// # POR QUÉ EL ORDEN LLEVA UNA REAPERTURA EN MEDIO, Y NO ES UN APAÑO DEL TEST
//
// Corregir exige `pending_approval` (`intakes.EditableStatus`, D-041.26) y aprobar
// deja la solicitud en `confirmed`. El camino del dueño para corregir un pedido ya
// confirmado es devolverlo al presupuesto por el selector de estado —transición legal,
// y el propio `edit.go` la describe como EL camino—, así que eso es lo que hace el
// test. `SetStatus` NO empuja al puente (no llama a `PushRevisionToCRM`), de modo que
// la reapertura no añade un cuarto cuerpo: los tres que se cuentan son los de las tres
// puertas y nada más.
//
// Y esa reapertura es además lo que hace que el estado del primer empuje difiera del
// de los otros dos, que es la mitad del criterio que mata al literal.
//
// 🔬 MUTACIONES QUE ESTE TEST TIENE QUE CAZAR (las dos que la Ola 4 encontró en campo):
//
//	`crmpush.Build`: `RevisionNo: in.RevisionNo` → `RevisionNo: 1`
//	`crmpush.Build`: `LifecycleStatus: intakes.NormalizeStatus(...)` → `"confirmed"`
func TestT410_LasTresPuertasEmpujanRevisionesEstrictamenteCrecientes(t *testing.T) {
	e := nuevaEscenaTresPuertas(t)
	ctx := context.Background()

	// La revisión que la máquina ya había interpretado: la solicitud llega al dueño
	// con ella, y es la que hace que la numeración de abajo no empiece por casualidad
	// en el mismo sitio que un contador ingenuo.
	sembrarRevisión(t, e.store, intakes.RevisionKindInterpreted, `{"version":1,"lines":[]}`)

	// ── PUERTA 1: APROBAR (T4.3) ──────────────────────────────────────────────
	if _, err := e.svc.Approve(ctx, tenantA, intakeDePrueba, laCotización); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	revAprobada := e.revisiónVigente(t)
	estadoAlAprobar := estadoDe(t, e.store)

	// ── la reapertura, que no empuja nada ─────────────────────────────────────
	if _, err := e.svc.SetStatus(ctx, tenantA, intakeDePrueba,
		intakes.StatusPendingApproval, intakes.NoticeByCaller); err != nil {
		t.Fatalf("devolver la solicitud a pending_approval: %v", err)
	}

	// ── PUERTA 2: CORREGIR (T4.4, el PUT …/items con as_correction) ───────────
	if _, err := e.svc.ReplaceItems(ctx, tenantA, intakeDePrueba,
		líneaCorregida, intakes.EditAsCorrection); err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}
	revCorregida := e.revisiónVigente(t)
	estadoAlCorregir := estadoDe(t, e.store)

	// ── PUERTA 3: RE-ANALIZAR (T4.6, la etapa draft del pipeline) ─────────────
	e.reAnalizar(t, revCorregida)
	revReanalizada := e.revisiónVigente(t)
	estadoAlReanalizar := estadoDe(t, e.store)

	// ── LOS TRES CUERPOS ──────────────────────────────────────────────────────
	cuerpos := e.cuerpos(t)
	if len(cuerpos) != 3 {
		t.Fatalf("el puente tiene que recibir UN documento por puerta (aprobar, corregir, re-analizar); "+
			"recibió %d\nlog:\n%s", len(cuerpos), e.log.all())
	}

	exigeCuerposDeLaMismaSolicitud(t, cuerpos)
	exigeElNumeroYElEstadoReales(t, cuerpos, []puertaEsperada{
		{"aprobar", revAprobada, estadoAlAprobar},
		{"corregir", revCorregida, estadoAlCorregir},
		{"re-analizar", revReanalizada, estadoAlReanalizar},
	})
	exigeCrecimientoEstricto(t, cuerpos)
	exigeQueElEstadoNoSeaUnLiteral(t, cuerpos)
}

// puertaEsperada es lo que el test SABE de cada empuje antes de mirar su cuerpo: de
// qué puerta salió, qué revisión lo motivó y en qué estado estaba la solicitud. Los
// dos números vienen del almacén; ninguno lo inventa el test.
type puertaEsperada struct {
	puerta string
	revNo  int
	estado string
}

// exigeCuerposDeLaMismaSolicitud vuelca lo entregado y comprueba las dos cosas sin las
// cuales el resto pasaría por casualidad: que los tres hablan del MISMO pedido —tres
// pushes de tres solicitudes distintas también saldrían «crecientes»— y que ninguno
// lleva el `revision_no: 0` que el schema del contrato rechaza (`minimum: 1`), que
// `crmpush` solo denuncia en un log que este test no mira a propósito.
func exigeCuerposDeLaMismaSolicitud(t *testing.T, cuerpos []crmpush.Payload) {
	t.Helper()
	for i, c := range cuerpos {
		// A la vista: es lo primero que hay que mirar cuando una mutación pone rojo
		// este test, y ahorra volver a instrumentarlo para saber qué llegó al puente.
		t.Logf("empuje %d entregado: intake=%s revision_no=%d lifecycle_status=%s",
			i+1, c.IntakeID, c.RevisionNo, c.LifecycleStatus)
		if c.IntakeID != intakeDePrueba {
			t.Fatalf("el empuje %d habla de la solicitud %q y no de la que se tocó", i+1, c.IntakeID)
		}
		if c.RevisionNo < 1 {
			t.Fatalf("el empuje %d viaja con revision_no %d, que el schema del contrato rechaza",
				i+1, c.RevisionNo)
		}
	}
}

// exigeElNumeroYElEstadoReales compara cada cuerpo contra lo que el ALMACÉN dice que
// pasó: el `revision_no` que asignó y el estado en el que quedó la solicitud.
func exigeElNumeroYElEstadoReales(t *testing.T, cuerpos []crmpush.Payload, esperados []puertaEsperada) {
	t.Helper()
	for i, q := range esperados {
		if cuerpos[i].RevisionNo != q.revNo {
			t.Fatalf("el empuje de %s viaja con revision_no %d y la revisión que lo motivó es la %d: "+
				"el puente hace UPSERT por (intake_id, revision_no) y descartaría el par repetido en silencio",
				q.puerta, cuerpos[i].RevisionNo, q.revNo)
		}
		if cuerpos[i].LifecycleStatus != q.estado {
			t.Fatalf("el empuje de %s viaja con lifecycle_status %q y la solicitud estaba en %q",
				q.puerta, cuerpos[i].LifecycleStatus, q.estado)
		}
	}
}

// exigeCrecimientoEstricto es LA afirmación que ningún test por puerta podía hacer:
// que el número crece ENTRE puertas y no solo dentro de cada una.
func exigeCrecimientoEstricto(t *testing.T, cuerpos []crmpush.Payload) {
	t.Helper()
	if cuerpos[0].RevisionNo >= cuerpos[1].RevisionNo || cuerpos[1].RevisionNo >= cuerpos[2].RevisionNo {
		t.Fatalf("los revision_no entregados no crecen estrictamente: aprobar=%d, corregir=%d, re-analizar=%d",
			cuerpos[0].RevisionNo, cuerpos[1].RevisionNo, cuerpos[2].RevisionNo)
	}
}

// exigeQueElEstadoNoSeaUnLiteral va aparte de la comparación de arriba a propósito:
// allí un `lifecycle_status` congelado en `confirmed` fallaría sin decir por qué, y
// este es el segundo defecto que la ola encontró en campo.
func exigeQueElEstadoNoSeaUnLiteral(t *testing.T, cuerpos []crmpush.Payload) {
	t.Helper()
	if cuerpos[0].LifecycleStatus == cuerpos[1].LifecycleStatus {
		t.Fatalf("los tres empujes viajan con el MISMO lifecycle_status (%q): el estado se está inventando, "+
			"no leyendo (es el literal `confirmed` que acertaba por casualidad mientras hubo un solo productor)",
			cuerpos[0].LifecycleStatus)
	}
}
