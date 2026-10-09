// Porta internal/intake/stages/draft.go @ 4cd9cfb

package stages

import (
	"context"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/anclaje"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// draft.go — LA ETAPA `draft` (Plan 044 · Ola 3 · T3.4): lo que el match dejó en líneas
// se convierte en la SOLICITUD que el dueño ve en la bandeja.
//
// Es la última etapa del pipeline y la primera que escribe FUERA de `intake_jobs`: a
// partir de aquí hay un objeto de negocio con su ciclo de vida propio (`intakes`,
// D-041.10) y su rastro de negociación (`intake_revisions`, ADR-0031 §3).
//
// El fichero viejo (1.038 líneas) se parte por tema (E-13, D-F7-6):
//
//   - draft.go          — LA CABECERA: la etapa, su cableado, `Run` y la solicitud.
//   - draft_revision.go — LA REVISIÓN: el contrato §7.4 del payload y su puerto.
//   - draft_events.go   — LOS EVENTOS: las dos filas de `flow_events` y `elapsed_ms`.
//   - draft_push.go     — EL EMPUJE: el puente CRM del re-análisis.
//
// # LAS CUATRO ESCRITURAS Y POR QUÉ EN ESTE ORDEN
//
//  1. `intakes` en `pending_approval` — la cabecera. Va primera porque las otras tres
//     cuelgan de su id. 🔄 CON UNA PREGUNTA DELANTE (D-044.46): si el evento YA tiene
//     contenido durable, esta escritura NO ocurre.
//  2. `intake_revisions`, `kind='interpreted'` — el borrador entero. Es LA entrega de
//     esta etapa: la cabecera sin revisión sería una solicitud vacía.
//  3. `flow_events` — la métrica. BEST-EFFORT.
//  4. `artifacts.draft` en el job — la marca de «esta etapa ya corrió». Va la ÚLTIMA a
//     propósito: es el único orden en el que la marca no puede afirmar un trabajo que no
//     se hizo.
//
// # 🔴 LO QUE ESTA ETAPA NO HACE, Y NO ES UN OLVIDO
//
//   - NO CIFRA NADA, y eso no significa que el literal viaje en claro: construye el
//     payload §7.4 ENTERO, con su `source_text` y sus `evidence`, y quien lo sella es el
//     store de revisiones (R-06, ver RevisionWriter).
//   - NO ESCRIBE `intake_items`: las líneas viven en la REVISIÓN. La mitad de las líneas
//     de un borrador no tienen precio y `intake_items.unit_price` es NOT NULL.
//   - NO ESCRIBE `intakes.customer_note`: el puerto de la cabecera no sabe escribirla.
//   - NO PONE TOTAL: `intakes.total` es la SUMA de `intake_items`, y aquí no hay ninguna.
//   - NO LLAMA AL MODELO y por eso NO ACEPTA Option (R-03): ver DraftOption.
//   - NO REPARTE ADJUNTOS: el reparto llega hecho en DraftInput.Media (deuda D-6: nadie
//     llama hoy a `anclaje.Distribute` por esta etapa).

// ErrDraftNotWired (antes `ErrDraftSinCablear`) es la etapa a la que le falta una pieza.
// Las cinco son obligatorias: un draft sin escritor de revisiones crearía solicitudes
// vacías y nadie lo notaría hasta que un dueño abriera la bandeja.
var ErrDraftNotWired = errors.New("stages: la etapa draft necesita log, store, escritor de solicitudes, de revisiones y de eventos")

// ErrNoMatch (antes `ErrSinMatch`) es «llegó un job sin el artefacto del match». Distinto
// de un artefacto con CERO líneas, que sería un borrador legítimo y vacío, no un fallo.
var ErrNoMatch = errors.New("stages: el draft necesita el artefacto del match")

// ErrJobWithoutEvent (antes `ErrJobSinEvento`) es el job cuya ventana no declara evento
// conversacional. Se comprueba en Go aunque la BD ya lo impida, y el motivo es el
// mensaje: sin esto el fallo sería un NOT NULL de `intakes.event_id` con el texto de
// Postgres, que no dice ni qué job era ni que el problema está en la CLAVE DE VENTANA.
var ErrJobWithoutEvent = errors.New("stages: el job no trae el evento conversacional del que cuelga el borrador")

// DraftArtifact (antes `ArtefactoDraft`) es lo que la etapa persiste bajo
// `artifacts.draft`. No repite el borrador —ése ya está en `intake_revisions`—: guarda el
// RESULTADO del acto, que es lo que una reanudación necesita para no repetirlo y lo que
// el worker necesita para cerrar el job con su `intake_id`.
type DraftArtifact struct {
	// Version es la del artefacto (`intakes.RevisionPayloadVersion`, hoy 1), que exige
	// `intake.Artifact.Validate`.
	Version int `json:"version"`
	// IntakeID es la solicitud de la que cuelga la revisión: la creada o la que ya había.
	IntakeID string `json:"intake_id"`
	// RevisionNo es el número que el STORE le dio a la revisión: 1 en el camino normal;
	// 2, 3… cuando la solicitud ya tenía historia.
	RevisionNo int `json:"revision_no"`
	// Lines es cuántas líneas tiene el borrador (`len(Match.Lines)`).
	Lines int `json:"lines"`
	// ElapsedMS es el mismo número que se publicó en la métrica (draft_events.go).
	ElapsedMS int64 `json:"elapsed_ms"`
}

// DraftInput (antes `EntradaDraft`) es todo lo que la etapa necesita; cada campo lo
// produce una etapa distinta:
//
//   - Match es el artefacto del match: las líneas, la nota del pedido y los avisos;
//   - SourceText es el literal del cliente YA DESCIFRADO, el mismo que vieron P2–P4;
//   - DeliveryDate (antes `FechaEntrega`) es la que dejó P4; vacía = sin fecha;
//   - Media es el reparto de adjuntos. 🔴 `ByLine` va indexado por la POSICIÓN de la
//     línea en `Match.Lines`;
//   - Analysis (antes `Analisis`) es el rastro de quién interpretó; lo rellena el worker.
type DraftInput struct {
	Match        *MatchArtifact
	SourceText   string
	DeliveryDate string
	Media        anclaje.Distribution
	Analysis     Analysis
}

// IntakeStore (antes `AlmacenSolicitudes`) es lo ÚNICO que esta etapa necesita de
// `public.intakes`: PREGUNTAR si el evento ya tiene contenido durable y, solo si no lo
// tiene, PARIRLO. Dos métodos, ni uno más: el pipeline no lista solicitudes, no las
// transiciona y —R-06— NO ESCRIBE REVISIONES por aquí.
//
// 🔴 PUENTE 1 (05 §4.1, muere en F8): `store.Intake` es del almacén VIEJO de flujos, que
// aún no tiene gemelo en `conversacion`. Lo satisfacen `*store.PostgresRepository` y
// `*store.MemoryRepository`. La lectura NO filtra por estado (D-044.46).
type IntakeStore interface {
	GetIntakeByEvent(ctx context.Context, tenantID, eventID string) (store.Intake, bool, error)
	UpsertIntake(ctx context.Context, o store.Intake) error
}

// Draft es la etapa del BORRADOR (Plan 044 · T3.4). Sus piezas: el log, el store de
// artefactos, los tres puertos de escritura, el reloj y —opcional— el puente CRM.
type Draft struct{}

// DraftOption (antes `OpciónDraft`) configura la etapa.
//
// 🔴 ES UN TIPO APARTE DE Option Y DE MatchOption, Y ESA ES LA PROMESA (R-03): los tres
// no son asignables entre sí, así que un draft «con plazo por llamada»
// —`NewDraft(…, WithCallTimeout(…))`— NO COMPILA. Aquí un plazo por llamada no significa
// nada: la etapa no llama al modelo.
type DraftOption func(*Draft)

// WithClock (antes `ConReloj`) sustituye el reloj de la etapa. Existe para los tests:
// `elapsed_ms` es la resta de dos instantes, y con el reloj de verdad no se puede afirmar
// un número. El reloj entra SOLO por aquí: la etapa no lo lee por otro camino, y sin la
// opción usa `time.Now`. Pasar nil no hace nada: la etapa no se queda sin reloj.
func WithClock(now func() time.Time) DraftOption {
	panic(pendiente.Implementar("stages.WithClock"))
}

// NewDraft construye la etapa. Devuelve ErrDraftNotWired (y etapa nil) si `log`, `st`,
// `intakeStore`, `revisions` o `events` es nil. Sin opciones, el reloj es `time.Now` y no
// hay puente CRM.
//
// 🔴 R-06: `revisions` tiene que ser el store de solicitudes, que es el ÚNICO que cifra
// el literal; el almacén de flujos que sirve `intakeStore` y `events` no sabe escribir
// revisiones y este constructor no le da por dónde.
func NewDraft(log logger.Logger, st StageStore, intakeStore IntakeStore,
	revisions RevisionWriter, events EventWriter, opts ...DraftOption) (*Draft, error) {
	panic(pendiente.Implementar("stages.NewDraft"))
}

// Run deja el borrador de un job YA RECLAMADO: resuelve la solicitud, le cuelga la
// revisión interpretada, publica la métrica y marca la etapa en el job.
//
// # ERRORES DE ENTRADA (artefacto nil; no se lee ni se escribe NADA)
//
//   - `in.Match == nil` ⇒ ErrNoMatch; se mira ANTES que el evento.
//   - `job.Key.EventID == ""` ⇒ ErrJobWithoutEvent envuelto, con `: job_id=<id>` detrás.
//
// # EN ESTE ORDEN
//
//  1. **La cabecera.** Pregunta `GetIntakeByEvent(tenant, evento)`. Si falla:
//     `draft: leer la solicitud del evento del job %s: %w`, y no se escribe nada.
//     - El evento YA tiene solicitud (la pudo parir el carrito, 54 ms antes en UAT) ⇒ el
//     borrador cuelga de ELLA: su id es el del artefacto, NO se llama a `UpsertIntake`
//     y su `status` no se toca, sea el que sea (un carrito `open` sigue comprando). Deja
//     el `Info` `draft: el evento YA tenía contenido durable; la revisión se cuelga de
//     él y su estado no se toca`.
//     - No la tiene ⇒ `UpsertIntake` con el id DERIVADO del evento —UUIDv5 de
//     `job.Key.EventID` en el espacio `6f8f5b2e-3d61-5a4c-9a1e-0b7c4d2f8a13`, que no
//     cambia NUNCA—, tenant, contacto (OPACO, tal como llegó), sesión y evento de la
//     clave de ventana, y `status = pending_approval`: es un NACIMIENTO, no pasa por
//     `open`. `Total` 0 y `CustomerNote` vacía. Si falla:
//     `draft: crear la solicitud del job %s: %w`, y no hay revisión.
//  2. **La nota del pedido** (`Match.CustomerNote`) NO se persiste. Si no es vacía, un
//     `Warn` `draft: el borrador trae nota de pedido y esta etapa NO la puede persistir
//     (intakes.customer_note solo la escribe el cierre del carrito)` con `runas` —su
//     longitud— y sin citarla (ADR-0034).
//  3. **La revisión** (draft_revision.go): UNA llamada a `InsertRevision` con el
//     RevisionPayload serializado. Si falla:
//     `draft: revisión interpretada de la solicitud %s: %w`; no hay empuje, ni eventos,
//     ni artefacto. La solicitud ya creada se queda.
//  4. **El empuje al CRM** (draft_push.go), solo si el job lo pidió la dueña.
//  5. **Los eventos** (draft_events.go): `intake_draft_created` siempre y, detrás,
//     `intake_reanalyzed` solo en un re-análisis.
//  6. **El artefacto**: UNA llamada `SaveStage(job.ID, …)` de etapa `intake.StageDraft`
//     con el DraftArtifact. Error ⇒ `draft: persistir el artefacto: %w`; `(false, nil)` ⇒
//     ErrJobNotProcessing. En los dos casos el artefacto devuelto es nil y la solicitud y
//     su revisión YA están escritas.
//  7. El `Info` `draft: borrador creado y esperando al dueño` con `job_id`, `stage`,
//     `intake_id`, `revision_no`, `status` (el de la solicitud: el suyo si ya existía),
//     `lineas`, `casadas`, `sin_casar`, `preguntas`, `avisos` y `elapsed_ms`.
//
// # LO QUE NO ES ERROR
//
// Nada de lo que traigan las líneas: un ítem que el match degradó (DEUDA-044.16) llega
// como línea más su Warning y el borrador sale entero. Tampoco un artefacto con CERO
// líneas, ni la falta de vía de análisis, ni el fallo de una métrica o del empuje.
//
// # DOS PASADAS SOBRE EL MISMO EVENTO
//
// Dan la MISMA solicitud —nunca dos— y DOS revisiones: `InsertRevision` no es idempotente
// a propósito. Lo que evita la segunda pasada en producción es el artefacto de esta
// etapa, que hace que el worker se la salte al reanudar. Un re-análisis (job nuevo sobre
// el mismo evento) aterriza así en la misma solicitud y deja intacta la revisión previa.
//
// # LO QUE NUNCA SALE POR EL LOG
//
// Ni una palabra del cliente: ni el literal, ni una evidencia, ni la nota, ni una
// etiqueta. Solo ids, contadores y posiciones.
func (s *Draft) Run(ctx context.Context, job intake.ClaimedJob, in DraftInput) (*DraftArtifact, error) {
	panic(pendiente.Implementar("stages.Draft.Run"))
}
