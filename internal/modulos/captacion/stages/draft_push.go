// Porta internal/intake/stages/draft.go @ 4cd9cfb

package stages

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// draft_push.go — EL EMPUJE AL CRM de la etapa `draft` (trozo de draft.go, E-13; Plan
// 044 · T4.6, cierre de T4.10 mitad 2). Quien lo ejecuta es Draft.Run (paso 4), con la
// revisión YA escrita y ANTES de los eventos.
//
// # 🔴 R-07 / D-044.19: EL GATE ES LA MARCA DEL JOB
//
// Se empuja SOLO si `job.Reanalysis.IsFromOwner()` (`intake_jobs.requested_by` es de la
// dueña). El pipeline NORMAL no empuja NUNCA, aunque haya puente cableado: D-044.19 pide
// que salga al CRM «toda revisión POSTERIOR AL CIERRE», y la primera revisión de un
// pedido interpretado no es posterior a nada. Empujar en toda revisión convertiría el
// pipeline en un productor de `intake.push` que el integrador vería como pedidos nuevos
// que el dueño ni ha mirado. Por eso la pregunta es por la marca y no por «¿he escrito
// una revisión?».
//
// # EN UN RE-ANÁLISIS DE LA DUEÑA
//
//   - Con puente: UNA llamada `PushRevisionByID(job.Key.TenantID, <solicitud>,
//     <revision_no>)`, con el número que el store le dio a la revisión recién escrita.
//   - Sin puente cableado: NO es mudo y NO tumba nada. Un `Error` (no un `Warn`: el
//     integrador se queda con una versión del pedido que ya no es verdad)
//     `draft: el re-análisis escribió su revisión y NO hay puente CRM cableado; el CRM se
//     queda con la versión vieja`.
//   - El puente falla: BEST-EFFORT, como la métrica y por lo mismo —la revisión YA está
//     escrita—. Un `Error` `draft: no se pudo encolar la revisión del re-análisis para el
//     puente CRM`, y la etapa sigue: publica sus eventos y deja su artefacto.

// CRMPusher (antes `EmpujadorCRM`) encola para el puente CRM del tenant la revisión que
// esta etapa acaba de escribir. Lo satisface `*intakes.Service` con PushRevisionByID.
//
// 🔴 PIDE EL PAR (solicitud, revisión) Y NO UN `Detail`: un pipeline que tuviera que
// construir el detalle completo para empujarlo estaría leyendo la bandeja del dueño desde
// un worker. La lectura la hace quien ya sabe leerla.
//
// 🔴 ES OPCIONAL (WithCRMPush) Y NO UN PARÁMETRO MÁS DEL CONSTRUCTOR: las cinco piezas
// posicionales son las que SIN ELLAS la etapa produce una solicitud rota, y ésta no lo
// es. Para que la ausencia no sea muda está el `Error` de arriba.
type CRMPusher interface {
	PushRevisionByID(ctx context.Context, tenantID, intakeID string, revisionNo int) error
}

// CRMPusherFunc (antes `EmpujadorCRMFunc`) adapta una función al puerto. Existe por un
// NUDO DE CONSTRUCCIÓN real: en el arranque esta etapa se construye ANTES que el Service
// de solicitudes que la satisface, así que el cable se corta con una clausura que se
// resuelve AL LLAMAR. La alternativa era un setter público sobre la etapa.
type CRMPusherFunc func(ctx context.Context, tenantID, intakeID string, revisionNo int) error

// PushRevisionByID implementa CRMPusher: llama a la función con los mismos cuatro
// argumentos y devuelve su error tal cual.
func (f CRMPusherFunc) PushRevisionByID(ctx context.Context, tenantID, intakeID string, revisionNo int) error {
	panic(pendiente.Implementar("stages.CRMPusherFunc.PushRevisionByID"))
}

// WithCRMPush (antes `ConEmpujeCRM`) cablea el puente CRM de la dueña. Sin él la etapa
// NO empuja y lo dice en un `Error` cuando el job era un re-análisis. Pasar nil no hace
// nada: es el mismo estado que no llamar a la opción.
func WithCRMPush(crm CRMPusher) DraftOption {
	panic(pendiente.Implementar("stages.WithCRMPush"))
}
