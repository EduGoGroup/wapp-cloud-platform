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
// nacieron con el verde (05 E-4, P6).

package apipublica

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/reanalisis"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
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
	// Sin el caso de uso la ruta NO se monta: es preferible un 404 de ruta inexistente a una
	// puerta que responde 500 a medio camino (trampa T-11).
	if d.Reanalysis == nil {
		return
	}
	mustHaveMW(k, "MountReanalyze")

	// 🔴 SIN `RequireFeature(...)` ALREDEDOR DEL HANDLER, Y NO FALTA (T-8): los dos gates están
	// DENTRO, en reanalisis.Service. Lo que sostiene que no se pierdan es un test de conducta de
	// aquel paquete, y lo que sostiene que aquí no se añada uno es
	// TestMountReanalyze_FaceHasNoFeatureGate.
	//
	// Auditada como escritura (`intakes.write`, recurso `intake`) como sus hermanas G3–G6.
	c.Handle("POST /api/v1/intakes/{id}/reanalyze", protect(k,
		"intakes.write", "intake", reanalyzeHandler(d.Reanalysis)))
}

// reanalyzeMaxBytes (maxReanalyzeBytes en la cara vieja) acota el cuerpo. Son dos campos: una vía
// de cinco caracteres y una transcripción que el saneo del dominio recorta a 280 RUNAS. 8 KiB
// deja sitio de sobra para 280 runas en UTF-8 (4 bytes por runa en el peor caso son 1120) y para
// el ruido de un JSON mal formado, y existe para que un cliente roto no empuje memoria por una
// puerta de configuración. Mismo orden de magnitud que el tope de PUT /api/v1/tenant-llm.
const reanalyzeMaxBytes = 1 << 13

// reanalyzeRequest es el cuerpo de POST /api/v1/intakes/{id}/reanalyze (§8.1).
//
// DOS campos y los dos OPCIONALES. `via` ausente ⇒ la del tenant; `text` ausente ⇒
// re-análisis puro del origen, que es el caso de Jhoan («regenera otra vez, según el
// origen») y el que se espera que sea mayoría.
//
// 🔴 NO HAY CAMPO `provider`, Y ESA AUSENCIA ES EL MECANISMO (D-044.28 §a). El
// proveedor —`anthropic` | `gemini`— sale SIEMPRE de `tenant_llm`: aceptarlo aquí
// dejaría que una llamada suelta se saltara la configuración del tenant. Un cuerpo
// que lo mande lo descarta encoding/json sin ruido, exactamente igual que el
// `tenant_id` que tampoco existe (INV-7): no hay dónde guardarlo.
//
// 🔴 NI HAY CAMPO `provider` COMO NOMBRE VIEJO DE `via`. El contrato lo renombró en
// T1.5-2 ANTES de que este endpoint existiera, así que aquí no hay compatibilidad
// que mantener: `{"provider":"api"}` se ignora entero y la petición corre por la vía
// del tenant, que es el desenlace SEGURO —nunca manda texto a un tercero por un
// campo que el servidor no conoce—. Lo custodia
// TestMountReanalyze_UnknownFieldsAreIgnored.
type reanalyzeRequest struct {
	Via  string `json:"via"`
	Text string `json:"text"`
}

// reanalyzeResponse es el 200 del §8.1.
//
// `status` es SIEMPRE `processing` y significa «tu petición se aceptó y hay trabajo
// en marcha» — NO el `intake_jobs.status` de la fila, que nace en `pending`. Lo fija el
// servicio (reanalisis.StatusInProgress), no esta capa. El endpoint no bloquea esperando al
// LLM: la revisión aparece por la bandeja (o por el polling de la app, D-045.4) cuando el job
// termine.
type reanalyzeResponse struct {
	IntakeID   string `json:"intake_id"`
	RevisionNo int    `json:"revision_no"`
	JobID      string `json:"job_id"`
	Via        string `json:"via"`
	Status     string `json:"status"`
}

// reanalyzeInvalidViaResponse (invalidViaResponse en la cara vieja) es el cuerpo del 400
// `invalid_via`.
//
// `via` va SIEMPRE, vacío incluido: `{"via":""}` dice «no mandaste vía», que es
// distinto de «mandaste una que no existe» y se lee igual de bien en un log. Es el
// MISMO código de error y la misma forma que usa `PUT /api/v1/tenant-llm` para su
// `via` (tenantllm.go), a propósito: la UI trata ese «no» en un solo sitio.
//
// `configured_via` es ADITIVO y solo aparece cuando el rechazo es por contradecir la
// vía elegida por el tenant. Sin él, la UI recibiría «tu vía no vale» sin poder decir
// cuál sí — y el usuario tendría que ir a otra pantalla a averiguarlo. Con
// `omitempty`, el rechazo de vocabulario sale EXACTAMENTE con la forma del contrato.
type reanalyzeInvalidViaResponse struct {
	Error         string `json:"error"`
	Via           string `json:"via"`
	ConfiguredVia string `json:"configured_via,omitempty"`
}

// reanalyzeFeatureDeniedResponse (featureDeniedResponse en la cara vieja) es el cuerpo del 403,
// y es LITERALMENTE el del middleware de entitlements
// (`{"error":"feature_not_enabled","feature":"…"}`, design §D-040.5).
//
// Se declara aquí en vez de usar el middleware porque este gate NO puede ser un
// middleware — ver el contrato de MountReanalyze—, pero la FORMA no se
// reinventa: quien consuma este 403 tiene que poder tratarlo con el mismo código que
// trata el de las demás rutas de pago.
type reanalyzeFeatureDeniedResponse struct {
	Error   string `json:"error"`
	Feature string `json:"feature"`
}

// reanalyzeCredentialsMissingResponse (credentialsMissingResponse en la cara vieja) es el cuerpo
// del 422 `llm_credentials_missing`.
//
// 🔴 ES UN CUERPO DISTINTO DEL 403, Y ESA SEPARACIÓN ES CRITERIO EXPLÍCITO DE T4.6.
// El 403 es «tu plan no lo incluye» ⇒ la UI muestra el paywall del add-on. Este 422
// es «configura tus credenciales» ⇒ la UI lleva a los ajustes de `tenant-llm`.
// Fundirlos dejaría a un tenant que YA PAGÓ mirando una pantalla de venta.
type reanalyzeCredentialsMissingResponse struct {
	Error string `json:"error"`
	Via   string `json:"via"`
}

// reanalyzeSourceUnavailableResponse (sourceUnavailableResponse en la cara vieja) es el cuerpo
// del 422 `source_unavailable`, con la razón que decide qué se le dice al dueño. Los dos textos
// los fija el §8.1 y se copian literales para que quien pinte la pantalla no los reinvente:
//
//	purged       → «el texto original de esta conversación ya venció por la política
//	               de retención; no se puede regenerar desde el origen»
//	never_stored → «esta conversación es anterior a tu plan con IA; no hay original
//	               guardado»
//
// Son dos mensajes distintos para dos hechos distintos —uno se tuvo y se destruyó, el
// otro nunca existió— y por eso la razón viaja en el cuerpo en vez de resolverse aquí:
// la prosa es de la UI, el hecho es del contrato.
type reanalyzeSourceUnavailableResponse struct {
	Error  string `json:"error"`
	Reason string `json:"reason"`
}

// reanalyzeInProgressResponse (reanalysisInProgressResponse en la cara vieja) es el cuerpo del
// 422 `reanalysis_in_progress`. Lleva el `job_id` para que quien llame pueda seguirlo en vez de
// reintentar a ciegas.
type reanalyzeInProgressResponse struct {
	Error string `json:"error"`
	JobID string `json:"job_id"`
}

// reanalyzeNoteTooLongResponse (noteTooLongResponse en la cara vieja) es el cuerpo del 400 del
// `text` que no cabe. Lleva las dos cifras por la misma razón que intakes.NoteTooLongError las
// lleva: quien se pasa tiene que poder decirle al usuario cuánto sobra («312 de 280»), no un «es
// muy largo» que no ayuda a arreglarlo.
//
// ⚠️ NO ESTÁ EN LA TABLA DEL §8.1, y se añade a propósito. El contrato enumera los
// desenlaces del re-análisis, no los del saneo del texto libre — que es una puerta
// del Plan 041 (REQ-33e) que este endpoint REUSA. El alternativo era truncar en
// silencio, que es justo lo que REQ-33e prohíbe.
type reanalyzeNoteTooLongResponse struct {
	Error string `json:"error"`
	Runes int    `json:"runes"`
	Max   int    `json:"max"`
}

// reanalyzeHandler (reanalyzeIntakeHandler en la cara vieja) sirve POST
// /api/v1/intakes/{id}/reanalyze: el dueño pide que la máquina vuelva a leer el pedido DESDE EL
// ORIGEN (Plan 044 · T4.6).
//
// A diferencia de `approve` y `request-info`, NO responde el detalle de la solicitud:
// responde el acuse del §8.1. Y no es una asimetría gratuita — cuando este handler
// contesta, la revisión nueva TODAVÍA NO EXISTE (la escribirá el pipeline minutos
// después), así que devolver el detalle enseñaría el estado viejo y una consola que
// repintara con él creería que no pasó nada.
//
// 🔴 CERO SALIENTES AL CLIENTE POR ESTE CAMINO (INV-1 / INV-12), y es estructural:
// mira las dependencias del handler y del servicio que llama — no hay Gateway, no hay
// Notifier, no hay SendText. Un re-análisis es una operación interna del dueño sobre
// su propio pedido y el cliente ni se entera.
func reanalyzeHandler(svc ReanalysisService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		var req reanalyzeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, reanalyzeMaxBytes)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "cuerpo JSON inválido")
			return
		}

		out, err := svc.Reanalyze(r.Context(), reanalisis.Request{
			TenantID: id.TenantID,
			IntakeID: r.PathValue("id"),
			Via:      req.Via,
			Text:     req.Text,
		})
		if err != nil {
			reanalyzeWriteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, reanalyzeResponse{
			IntakeID:   out.IntakeID,
			RevisionNo: out.RevisionNo,
			JobID:      out.JobID,
			Via:        out.Via,
			Status:     out.Status,
		})
	})
}

// reanalyzeWriteError (writeReanalyzeError en la cara vieja) traduce el fallo del dominio al
// código y al cuerpo del §8.1.
//
// ════════════════════════════════════════════════════════════════════════════
// EL ORDEN DE ESTE `switch` NO ES EL ORDEN DE LOS CHEQUEOS
// ════════════════════════════════════════════════════════════════════════════
//
// Quien decide en qué orden se comprueban las cosas es el dominio
// (`reanalisis.Service.Reanalyze`, donde está escrito y razonado). Aquí solo llega
// UN error ya decidido, así que el orden de los `case` es indiferente en producción y se
// escribe en el del contrato para que se lea al lado de la tabla del §8.1. Se conserva el de la
// cara vieja, y lo fija TestMountReanalyze_FirstMatchWins.
//
// 🔴 EL `default` ES 500 Y NO SE PUEDE ALCANZAR CON EL PROVEEDOR CAÍDO, que es el
// criterio INV-10 de T4.6. Este endpoint NO llama al modelo: abre un job y vuelve. Un
// proveedor muerto se descubre minutos después, en el worker, y allí lo que pasa es
// que el job se reintenta o muere con su causa escrita — la revisión anterior queda
// intacta, el intake no cambia de estado y el cliente no recibe nada. Por aquí solo
// se cae al 500 si se cae Postgres, que es un 500 honesto, o si el servicio está sin cablear
// (reanalisis.ErrNotWired), que es un fallo del arranque y no del que llama.
func reanalyzeWriteError(w http.ResponseWriter, err error) {
	var (
		invalidVia  reanalisis.InvalidViaError
		featureGone reanalisis.FeatureMissingError
		credentials reanalisis.CredentialsMissingError
		source      reanalisis.SourceUnavailableError
		inProgress  reanalisis.InProgressError
		tooLong     intakes.NoteTooLongError
	)
	switch {
	case errors.As(err, &invalidVia):
		writeJSON(w, http.StatusBadRequest, reanalyzeInvalidViaResponse{
			Error: "invalid_via", Via: invalidVia.Via, ConfiguredVia: invalidVia.Configured,
		})
	case errors.As(err, &tooLong):
		writeJSON(w, http.StatusBadRequest, reanalyzeNoteTooLongResponse{
			Error: "text_too_long", Runes: tooLong.Runes, Max: tooLong.Max,
		})
	case errors.As(err, &featureGone):
		writeJSON(w, http.StatusForbidden, reanalyzeFeatureDeniedResponse{
			Error: "feature_not_enabled", Feature: featureGone.Feature,
		})
	case errors.As(err, &credentials):
		writeJSON(w, http.StatusUnprocessableEntity, reanalyzeCredentialsMissingResponse{
			Error: "llm_credentials_missing", Via: credentials.Via,
		})
	case errors.Is(err, intakes.ErrNotFound):
		// 404 y NUNCA 403: un 403 confirmaría que el id existe. «No existe» y «es de
		// otro tenant» tienen que ser la misma respuesta (INV-8).
		writeError(w, http.StatusNotFound, "solicitud no encontrada")
	case errors.As(err, &inProgress):
		writeJSON(w, http.StatusUnprocessableEntity, reanalyzeInProgressResponse{
			Error: "reanalysis_in_progress", JobID: inProgress.JobID,
		})
	case errors.As(err, &source):
		writeJSON(w, http.StatusUnprocessableEntity, reanalyzeSourceUnavailableResponse{
			Error: "source_unavailable", Reason: source.Reason,
		})
	default:
		writeError(w, http.StatusInternalServerError, "no se pudo pedir el re-análisis de la solicitud")
	}
}
