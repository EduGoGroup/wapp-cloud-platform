// Porta internal/intake/stages/draft.go @ 4cd9cfb

package stages

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
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
