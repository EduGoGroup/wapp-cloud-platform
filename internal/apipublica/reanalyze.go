// Porta internal/publicapi/reanalyze.go @ 4f79bbf (268 líneas, entero) y el registro de H1
// (internal/publicapi/publicapi.go @ 4f79bbf, registerIntakes, líneas 704-743).
//
// reanalyze.go — LA PUERTA HTTP DEL RE-ANÁLISIS (Plan 044 · Ola 4 · T4.6, D-044.15; contrato
// completo en design §8.1; mapa §2.8, H1): POST /api/v1/intakes/{id}/reanalyze. El dueño pide
// que la máquina vuelva a leer el pedido DESDE EL ORIGEN.
//
// Vive en su propio fichero y no junto a sus hermanas `approve` y `request-info`
// (intakes_approve.go) porque este endpoint tiene SIETE desenlaces con cuerpo propio y otro
// dueño: el dominio vive en internal/modulos/captacion/reanalisis, no en solicitudes. Aquí solo
// se traduce: la política de CÓDIGOS es de esta capa y la de QUÉ pasó es del dominio.
//
// En el rojo solo existían el puerto, ReanalyzeDeps y MountReanalyze; el handler y sus cuerpos
// nacen con el verde (05 E-4, P6).

package apipublica

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/reanalisis"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ReanalysisService es el puerto del caso de uso del re-análisis. Lo satisface
// *reanalisis.Service del módulo captación NUEVO.
//
// UN método, y no puede tener más: desde aquí no se puede listar jobs, ni cancelar
// uno, ni consultar el estado del pipeline. Cada una de esas cosas sería un endpoint
// con su propio contrato, y el §8.1 no publica ninguno.
type ReanalysisService interface {
	// Reanalyze (Reanalizar en la cara vieja) abre el job del re-análisis y devuelve el acuse
	// del §8.1. Los dos gates de feature (`llm_intake`, `api_llm`), la forma de `via` y el saneo
	// de `text` los decide ÉL, en su orden; la cara no adelanta ninguno.
	Reanalyze(ctx context.Context, req reanalisis.Request) (reanalisis.Result, error)
}

// ReanalyzeDeps es lo que H1 necesita. En la cara vieja era el campo Reanalysis de
// publicapi.Deps.
//
// 🔴 UN SOLO CAMPO, Y ESA AUSENCIA ES LA REGLA (trampa T-8): aquí NO hay resolver de derechos.
// Los gates del re-análisis viven dentro del servicio; una cara que tuviera con qué preguntar
// por una feature acabaría preguntando antes de tiempo.
type ReanalyzeDeps struct {
	// Reanalysis es el caso de uso. nil ⇒ la ruta no se monta.
	Reanalysis ReanalysisService
}

// MountReanalyze registra en c la ruta H1, "POST /api/v1/intakes/{id}/reanalyze", solo si
// d.Reanalysis no es nil. Sin el servicio la ruta NO existe (404 de ruta inexistente; no se
// registra ningún patrón): mejor eso que una puerta que responde 500 a medio camino. No depende
// de que la bandeja (MountIntakes) esté montada.
//
// Cadena W de Common, permiso "intakes.write", recurso de auditoría "intake": 401 sin token; 403
// {"error":"permiso denegado"} sin el permiso o con un token sin empresa; ningún registro en
// esos dos; y UN registro por petición que pasa la cadena —"success" con el 200, "failure" con
// Meta {"status":<código>} en cualquier 4xx o 5xx del handler—. Consta quién lo disparó: abre
// trabajo en la cola del pipeline y acaba en una revisión nueva del pedido.
//
// 🔴 ES LA ÚNICA RUTA DE LA BANDEJA SIN GATE DE FEATURE EN LA CADENA, Y ES DELIBERADO (T-8). Ni
// `cart_basic` ni `llm_intake` ni `api_llm` se preguntan aquí: un tenant SIN NINGUNA feature
// LLEGA al servicio, y es el servicio quien decide. Las dos razones:
//
//  1. un middleware corre antes del handler por definición, así que un gate en la cadena
//     respondería 403 a un `{"via":"chatgpt"}` que el contrato §8.1 manda rechazar con 400;
//  2. el gate de `api_llm` depende de la vía EFECTIVA, que solo se conoce tras leer
//     `tenant_llm`: preguntarlo aquí cerraría la puerta a todo tenant de vía local (ADR-0044 ·
//     D-044.28).
//
// `cart_basic` tampoco aplica, y no es un olvido (D-044.49): aprobar y corregir son del OBJETO;
// re-analizar es literalmente la máquina que se vende aparte.
//
// La cara NO promete un orden entre desenlaces (ni «todo 400 antes de todo 403»): promete que no
// pone gate y que traduce UN error que el servicio ya decidió.
//
// LA PETICIÓN. Cuerpo JSON de hasta 8 KiB con DOS campos, los dos OPCIONALES: {"via","text"}.
//
//   - ilegible (vacío, JSON roto, un tipo que no casa, o más de 8 KiB) ⇒ 400 {"error":"cuerpo
//     JSON inválido"} y el servicio NO se toca;
//   - `{}` y `null` son válidos y son el caso normal («regenera otra vez, según el origen»):
//     llegan al servicio con Via y Text vacíos;
//   - `via` y `text` llegan al servicio TAL CUAL, sin recortar, sanear ni validar: la forma de
//     la vía y el tope de runas del texto son del dominio;
//   - un campo que el contrato no publica se IGNORA sin ruido. 🔴 No hay campo `provider` (el
//     proveedor sale SIEMPRE de `tenant_llm`, D-044.28 §a) ni como nombre viejo de `via`:
//     `{"provider":"api"}` corre por la vía del tenant, que es el desenlace seguro. Tampoco hay
//     `tenant_id` (INV-7).
//
// El servicio se llama UNA vez con reanalisis.Request{TenantID: el del TOKEN (INV-7/INV-8; uno
// en la query o en el cuerpo no cuenta), IntakeID: el {id} de la ruta tal cual, Via, Text} y con
// el contexto de la petición, sin plazo propio.
//
// LOS DESENLACES. El 200 es un ACUSE y no el detalle de la solicitud —cuando se contesta, la
// revisión nueva todavía no existe: la escribirá el pipeline—:
//
//   - 200 {"intake_id","revision_no","job_id","via","status"}, en ese orden, con los cinco
//     valores del Result TAL CUAL (`status` lo pone el servicio; la cara no lo fija). Ninguna
//     respuesta lleva el tenant.
//
// El error del servicio se traduce así, leyendo los tipos con errors.As (son tipos VALOR y
// pueden llegar envueltos) y los centinelas con errors.Is. Si un error casara con varios, gana
// el PRIMERO de esta lista (es el orden del §8.1 y el de la cara vieja):
//
//  1. reanalisis.InvalidViaError ⇒ 400 {"error":"invalid_via","via":<Via>} con `via` SIEMPRE,
//     vacío incluido, y "configured_via":<Configured> SOLO si no es vacío (es la misma forma que
//     el `invalid_via` de PUT /api/v1/tenant-llm, con ese campo aditivo);
//  2. intakes.NoteTooLongError (del módulo solicitudes; el servicio lo entrega ENVUELTO) ⇒ 400
//     {"error":"text_too_long","runes":<Runes>,"max":<Max>}. No está en la tabla del §8.1: es la
//     puerta de saneo del Plan 041 (REQ-33e), que rechaza en vez de truncar;
//  3. reanalisis.FeatureMissingError ⇒ 403 {"error":"feature_not_enabled","feature":<Feature>}:
//     LITERALMENTE el cuerpo del middleware de entitlements (D-040.5), para que la UI lo trate
//     en un solo sitio. Son dos casos con dos claves (`llm_intake`, `api_llm`);
//  4. reanalisis.CredentialsMissingError ⇒ 422 {"error":"llm_credentials_missing","via":<Via>}.
//     🔴 Cuerpo DISTINTO del 403: aquel es el paywall, éste manda a los ajustes de `tenant-llm`;
//  5. intakes.ErrNotFound ⇒ 404 {"error":"solicitud no encontrada"}. Nunca 403: confirmaría que
//     el id existe (INV-8);
//  6. reanalisis.InProgressError ⇒ 422 {"error":"reanalysis_in_progress","job_id":<JobID>};
//  7. reanalisis.SourceUnavailableError ⇒ 422 {"error":"source_unavailable","reason":<Reason>}:
//     la razón (`purged` | `never_stored`) viaja tal cual; la prosa es de la UI;
//  8. cualquier otro —también reanalisis.ErrNotWired— ⇒ 500 {"error":"no se pudo pedir el
//     re-análisis de la solicitud"}, SIN repetir el error (el de un driver puede llevar el DSN).
//     🔴 No se alcanza con el proveedor LLM caído (INV-10): este endpoint no llama al modelo.
//
// 🔴 CERO SALIENTES AL CLIENTE POR ESTE CAMINO (INV-1 / INV-12): la puerta no tiene gateway ni
// notificador; un re-análisis es una operación interna del dueño sobre su propio pedido.
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una identidad
// sin empresa que llegara al handler recibiría 401 {"error":"autenticación requerida"} sin tocar
// el servicio.
//
// Fallo de cableado: k.MW nil con d.Reanalysis presente hace panic AL MONTAR (ver Common). Sin
// servicio no se monta nada y k ni se mira.
func MountReanalyze(c *Cara, k Common, d ReanalyzeDeps) {
	panic(pendiente.Implementar("apipublica.MountReanalyze"))
}
