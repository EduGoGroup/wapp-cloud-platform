// Porta internal/intake/stages/draft.go @ 4cd9cfb

package stages

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// draft_events.go — LOS EVENTOS de la etapa `draft` (trozo de draft.go, E-13): las dos
// filas de `flow_events` que publica y el cronómetro de `elapsed_ms`. Quien los ejecuta
// es Draft.Run (paso 5), con la revisión YA escrita.
//
// # LO COMÚN A LAS DOS FILAS
//
// Van por EventWriter con `TenantID` y `ContactID` de la clave de ventana —el contacto es
// el OPACO, nunca un número ni un JID—, `FlowID` IntakeFlowID, `FlowVersion`
// IntakeFlowVersion y `Kind` `"event"` (telemetría, no proyección).
//
// 🔴 SON BEST-EFFORT: si la escritura falla se avisa y la etapa SIGUE. Perder una fila de
// telemetría no puede costar el pedido de un cliente.
//
// 🔴 EN EL PAYLOAD NO ENTRA NI UNA PALABRA DEL CLIENTE: `flow_events` es una tabla en
// claro (ADR-0034). Ni el texto, ni las evidencias, ni las etiquetas de los productos.
//
// # `intake_draft_created` — SIEMPRE, UNA POR PASADA
//
// Cuatro CONTADORES, la forma exacta de design §10
// (`{"elapsed_ms":174000,"lines":4,"matched":2,"unmatched":1}`):
//
//   - `elapsed_ms` (int64): ver abajo;
//   - `lines` (int): todas las líneas del match;
//   - `matched` y `unmatched` (int): las de KindMatched y KindUnmatched. El ENVÍO no es
//     ninguna de las dos: no sale del catálogo, lo pone la plataforma.
//
// Y una QUINTA clave, `requested_by`, SOLO cuando `job.Reanalysis.RequestedBy` no es
// vacío, con ese valor tal cual: en un re-análisis `elapsed_ms` mide horas o días —el job
// hereda el `message_ts` original—, y sin la marca el KPI «tiempo a primer borrador»
// quedaría envenenado de forma invisible. El pipeline normal sale con CUATRO claves,
// byte a byte.
//
// Si falla: `Warn` `draft: no se pudo publicar la métrica del borrador; el borrador SÍ
// está creado`.
//
// # `intake_reanalyzed` — SOLO SI `job.Reanalysis.IsFromOwner()`, DETRÁS DE LA OTRA
//
// Cuatro claves, la forma de design §10
// (`{"via":"api","from_rev":1,"to_rev":2,"source":"event_thread"}`), con valores REALES:
// `via` y `source` (string) y `from_rev` (int) son los del job —`from_rev` 0 = no había
// revisión previa; no se rellena con un 1 plausible—, y `to_rev` (int) es el número que
// el store ACABA de darle a la revisión, no `from_rev + 1`. La clave es `via`, NUNCA
// `provider`. No lleva `requested_by`.
//
// Un job del pipeline normal no publica ni una fila de este evento: no re-analizó nada.
//
// Si falla: `Warn` `draft: no se pudo publicar la métrica del re-análisis; la revisión
// SÍ está escrita`.
//
// # `elapsed_ms`: CUÁNTO ESPERÓ EL CLIENTE
//
// Es el KPI del plan («tiempo a primer borrador < 5 min»): el reloj de la etapa
// (WithClock) menos `job.MessageTS`. Mide la espera del CLIENTE, no el tiempo de proceso:
// un job reanudado tres horas después publica tres horas. Es el mismo número en la
// métrica y en el DraftArtifact.
//
// Son DOS RELOJES —el fin es del cloud, el inicio es del Edge— y se restan igual: la
// magnitud es de minutos y la deriva de segundos. Lo que no se hace con ellos es DECIDIR.
//
//   - `job.MessageTS` cero ⇒ 0, con el `Warn` `draft: el job no trae message_ts;
//     elapsed_ms se publica como 0 y NO mide la espera del cliente`.
//   - Resultado NEGATIVO (relojes desalineados) ⇒ NO se publica: 0, con el `Warn`
//     `draft: elapsed_ms salió NEGATIVO; el reloj del Edge y el del cloud están
//     desalineados` y su `desfase_ms`.

// EventDraftCreated (antes `EventoBorradorCreado`) es el `flow_events.name` de la métrica
// de esta etapa (design §10). Se declara aquí porque aquí está su ÚNICO productor.
const EventDraftCreated = "intake_draft_created"

// EventReanalyzed (antes `EventoReanalizado`) es el `flow_events.name` del RE-ANÁLISIS
// consumado (design §10, D-044.15).
//
// 🔴 SE EMITE AL CERRAR EL JOB, NO AL ABRIRLO: quien recibe el POST /reanalyze solo ABRE
// el job y no puede conocer la revisión que el re-análisis va a escribir. Aquí el hecho
// está consumado.
const EventReanalyzed = "intake_reanalyzed"

// IntakeFlowID (antes `FlujoCaptacion`) e IntakeFlowVersion (antes
// `VersionFlujoCaptacion`) son el `flow_id` / `flow_version` con los que el pipeline
// firma sus filas de `flow_events`.
//
// 🔴 SON SINTÉTICOS: las dos columnas son NOT NULL porque la tabla nació como outbox del
// motor de flujos, y este evento lo emite un worker de cola que no está en ningún flujo.
// El prefijo `_` es el espacio RESERVADO de la plataforma: ningún flujo de ningún tenant
// puede colisionar con él. La versión es 1 y no 0 («desconocida»): es la del contrato de
// payload de este emisor, y sube el día que el payload cambie.
const (
	IntakeFlowID      = "_intake_llm"
	IntakeFlowVersion = 1
)

// EventWriter (antes `EscritorEvento`) es lo ÚNICO que esta etapa necesita del outbox de
// efectos: añadir una fila.
//
// 🔴 PUENTE 1 (05 §4.1, muere en F8): `store.FlowEvent` es del almacén VIEJO de flujos.
// Lo satisfacen `*store.PostgresRepository` y `*store.MemoryRepository`.
type EventWriter interface {
	InsertFlowEvent(ctx context.Context, ev store.FlowEvent) error
}

// flowEventKind (antes `kindEventoFlujo`) es el `flow_events.kind` de una fila de
// TELEMETRÍA ("event", frente a "persist", que además proyecta una tabla tipada). Replica
// el literal que declaran los módulos porque el vocabulario es de la tabla (0009), no de
// ningún paquete de Go.
const flowEventKind = "event"

// elapsed (antes `transcurrido`) es `elapsed_ms`: cuánto esperó el cliente desde que
// escribió hasta que su borrador existió.
//
//	FIN    → `s.now()`: el reloj de GO del proceso que corre el pipeline (cloud).
//	INICIO → `job.MessageTS`: el `ts_unix` del mensaje del CLIENTE que abrió la ventana, o
//	         sea el reloj del EDGE, guardado en `intake_jobs.message_ts`.
//
// Son DOS RELOJES y se restan igual, por dos razones que sí se sostienen: es lo que mide
// el KPI —la espera que interesa es la DEL CLIENTE, y empieza cuando él escribió, no
// cuando nos enteramos—, y la magnitud es de MINUTOS frente a una deriva con NTP de
// milisegundos a segundos. Lo que NO se hace con estos dos instantes es DECIDIR nada:
// comparar «¿antes o después?» entre relojes distintos sí es el defecto silencioso.
func (s *Draft) elapsed(job intake.ClaimedJob) time.Duration {
	if job.MessageTS.IsZero() {
		// Sin instante del cliente no hay nada que restar. Cero es el único valor
		// honesto, y el aviso dice que el número que se publica no mide la espera.
		s.log.Warn("draft: el job no trae message_ts; elapsed_ms se publica como 0 y NO mide la espera del cliente",
			"job_id", job.ID, "stage", intake.StageDraft)
		return 0
	}
	d := s.now().Sub(job.MessageTS)
	if d < 0 {
		// Un `elapsed_ms` negativo en un panel no se lee como «relojes desajustados», se
		// lee como un bug del pipeline: se recorta a 0 y se avisa.
		s.log.Warn("draft: elapsed_ms salió NEGATIVO; el reloj del Edge y el del cloud están desalineados",
			"job_id", job.ID, "stage", intake.StageDraft, "desfase_ms", d.Milliseconds())
		return 0
	}
	return d
}

// publishMetric (antes `publicarMetrica`) escribe la fila de `intake_draft_created`.
// BEST-EFFORT: un fallo se avisa y la etapa sigue.
//
// El `contact_id` que lleva la fila es el OPACO de la clave de ventana (ADR-0010,
// ADR-0017): no podría ser otra cosa aunque alguien quisiera, porque `WindowKey` no
// transporta un número ni un JID.
func (s *Draft) publishMetric(ctx context.Context, job intake.ClaimedJob, art *MatchArtifact, elapsed time.Duration) {
	matched, unmatched := s.tally(art)
	payload := map[string]any{
		"elapsed_ms": elapsed.Milliseconds(),
		"lines":      len(art.Lines),
		"matched":    matched,
		"unmatched":  unmatched,
	}
	// 🔧 LA QUINTA CLAVE, Y SOLO CUANDO APLICA (T4.6): así la métrica del pipeline normal
	// sale byte a byte con la forma de design §10 y ningún panel existente cambia. Es un
	// ROL, sin PII, igual que en `intake_jobs`.
	//
	// ⚠️ EL GATE ES `!= ""` Y NO `IsFromOwner()`, a diferencia del autor, el empuje y el
	// otro evento: se porta tal cual del viejo (`draft.go:852`). Hoy no se distinguen
	// —el único valor que se escribe es `owner`—; el día que haya otro rol, esta clave lo
	// publicará y las otras tres puertas no se abrirán.
	if rb := job.Reanalysis.RequestedBy; rb != "" {
		payload["requested_by"] = rb
	}
	err := s.events.InsertFlowEvent(ctx, store.FlowEvent{
		TenantID:    job.Key.TenantID,
		ContactID:   job.Key.ContactID,
		FlowID:      IntakeFlowID,
		FlowVersion: IntakeFlowVersion,
		Kind:        flowEventKind,
		Name:        EventDraftCreated,
		Payload:     payload,
	})
	if err != nil {
		s.log.Warn("draft: no se pudo publicar la métrica del borrador; el borrador SÍ está creado",
			"job_id", job.ID, "stage", intake.StageDraft, "name", EventDraftCreated, "error", err.Error())
	}
}

// publishReanalysis (antes `publicarReanalisis`) escribe la fila de `intake_reanalyzed`,
// y SOLO si el job lo pidió el dueño. BEST-EFFORT, igual que su hermana y por lo mismo:
// la revisión ya está escrita.
//
// 🔴 EL GATE ES LA MARCA DEL JOB, EL MISMO QUE EL DEL EMPUJE AL CRM. Emitir en todos los
// jobs llenaría el evento de filas con `via`, `source` y `from_rev` vacíos —son
// cero-valor fuera del re-análisis— y el KPI contaría como «el LLM se equivocó» cada
// pedido que salió bien a la primera.
func (s *Draft) publishReanalysis(ctx context.Context, job intake.ClaimedJob, revisionNo int) {
	if !job.Reanalysis.IsFromOwner() {
		return
	}
	err := s.events.InsertFlowEvent(ctx, store.FlowEvent{
		TenantID:    job.Key.TenantID,
		ContactID:   job.Key.ContactID,
		FlowID:      IntakeFlowID,
		FlowVersion: IntakeFlowVersion,
		Kind:        flowEventKind,
		Name:        EventReanalyzed,
		Payload: map[string]any{
			"via":      job.Reanalysis.Via,
			"from_rev": job.Reanalysis.From,
			"to_rev":   revisionNo,
			"source":   job.Reanalysis.Source,
		},
	})
	if err != nil {
		s.log.Warn("draft: no se pudo publicar la métrica del re-análisis; la revisión SÍ está escrita",
			"job_id", job.ID, "stage", intake.StageDraft, "name", EventReanalyzed,
			"revision_no", revisionNo, "error", err.Error())
	}
}

// tally (antes `recuento`) cuenta las líneas por clase. El envío no es ninguna de las
// dos: no casa con el catálogo porque no sale de él —lo pone la plataforma (D-041.11)—,
// así que contarlo como `unmatched` diría que hay un producto que el tenant no vende.
func (*Draft) tally(art *MatchArtifact) (matched, unmatched int) {
	for _, l := range art.Lines {
		switch l.Kind {
		case KindMatched:
			matched++
		case KindUnmatched:
			unmatched++
		}
	}
	return matched, unmatched
}
