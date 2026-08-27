package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/reanalisis"
)

// reanalyze.go es la puerta HTTP del RE-ANÁLISIS (Plan 044 · Ola 4 · T4.6,
// D-044.15; contrato completo en design §8.1): `POST /api/v1/intakes/{id}/reanalyze`.
//
// Vive en su propio fichero y no dentro de intakes.go —donde están sus hermanas
// `approve` y `request-info`— por una razón que se ve en el tamaño de
// writeReanalyzeError: este endpoint tiene SEIS desenlaces con cuerpo propio, que es
// más que los de las otras tres puertas del dueño juntas. Mezclarlos con el resto
// habría dejado intakes.go con dos temas.
//
// El dominio vive en `internal/reanalisis`; aquí solo se traduce. La política de
// códigos es de esta capa y la de QUÉ pasó es del dominio, que es el mismo reparto
// que writeApproveError y writeEditItemsError.

// ReanalysisService es el puerto del caso de uso del re-análisis. Lo satisface
// *reanalisis.Servicio.
//
// UN método, y no puede tener más: desde aquí no se puede listar jobs, ni cancelar
// uno, ni consultar el estado del pipeline. Cada una de esas cosas sería un endpoint
// con su propio contrato, y el §8.1 no publica ninguno.
type ReanalysisService interface {
	Reanalizar(ctx context.Context, req reanalisis.Solicitud) (reanalisis.Resultado, error)
}

// maxReanalyzeBytes acota el cuerpo. Son dos campos: una vía de cinco caracteres y
// una transcripción que `cart.SanitizeNote` recorta a 280 RUNAS. 8 KiB deja sitio de
// sobra para 280 runas en UTF-8 (4 bytes por runa en el peor caso son 1120) y para
// el ruido de un JSON mal formado, y existe para que un cliente roto no empuje
// memoria por una puerta de configuración. Mismo orden de magnitud que
// maxTenantLLMBytes.
const maxReanalyzeBytes = 1 << 13

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
// TestReanalyze_CampoProviderNoSeAceptaEnSilencio.
type reanalyzeRequest struct {
	Via  string `json:"via"`
	Text string `json:"text"`
}

// reanalyzeResponse es el 200 del §8.1.
//
// `status` es SIEMPRE `processing` y significa «tu petición se aceptó y hay trabajo
// en marcha» — NO el `intake_jobs.status` de la fila, que nace en `pending`. El
// endpoint no bloquea esperando al LLM: la revisión aparece por la bandeja (o por el
// polling de la app, D-045.4) cuando el job termine.
type reanalyzeResponse struct {
	IntakeID   string `json:"intake_id"`
	RevisionNo int    `json:"revision_no"`
	JobID      string `json:"job_id"`
	Via        string `json:"via"`
	Status     string `json:"status"`
}

// invalidViaResponse es el cuerpo del 400 `invalid_via`.
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
type invalidViaResponse struct {
	Error         string `json:"error"`
	Via           string `json:"via"`
	ConfiguredVia string `json:"configured_via,omitempty"`
}

// featureDeniedResponse es el cuerpo del 403, y es LITERALMENTE el del middleware de
// entitlements (`{"error":"feature_not_enabled","feature":"…"}`, design §D-040.5).
//
// Se declara aquí en vez de usar el middleware porque este gate NO puede ser un
// middleware — ver el comentario del montaje en publicapi.go—, pero la FORMA no se
// reinventa: quien consuma este 403 tiene que poder tratarlo con el mismo código que
// trata el de las demás rutas de pago.
type featureDeniedResponse struct {
	Error   string `json:"error"`
	Feature string `json:"feature"`
}

// credentialsMissingResponse es el cuerpo del 422 `llm_credentials_missing`.
//
// 🔴 ES UN CUERPO DISTINTO DEL 403, Y ESA SEPARACIÓN ES CRITERIO EXPLÍCITO DE T4.6.
// El 403 es «tu plan no lo incluye» ⇒ la UI muestra el paywall del add-on. Este 422
// es «configura tus credenciales» ⇒ la UI lleva a los ajustes de `tenant-llm`.
// Fundirlos dejaría a un tenant que YA PAGÓ mirando una pantalla de venta.
type credentialsMissingResponse struct {
	Error string `json:"error"`
	Via   string `json:"via"`
}

// sourceUnavailableResponse es el cuerpo del 422 `source_unavailable`, con la razón
// que decide qué se le dice al dueño. Los dos textos los fija el §8.1 y se copian
// literales para que quien pinte la pantalla no los reinvente:
//
//	purged       → «el texto original de esta conversación ya venció por la política
//	               de retención; no se puede regenerar desde el origen»
//	never_stored → «esta conversación es anterior a tu plan con IA; no hay original
//	               guardado»
//
// Son dos mensajes distintos para dos hechos distintos —uno se tuvo y se destruyó, el
// otro nunca existió— y por eso la razón viaja en el cuerpo en vez de resolverse aquí:
// la prosa es de la UI, el hecho es del contrato.
type sourceUnavailableResponse struct {
	Error  string `json:"error"`
	Reason string `json:"reason"`
}

// reanalysisInProgressResponse es el cuerpo del 422 `reanalysis_in_progress`. Lleva
// el `job_id` para que quien llame pueda seguirlo en vez de reintentar a ciegas.
type reanalysisInProgressResponse struct {
	Error string `json:"error"`
	JobID string `json:"job_id"`
}

// noteTooLongResponse es el cuerpo del 400 del `text` que no cabe. Lleva las dos
// cifras por la misma razón que cart.NoteTooLongError las lleva: quien se pasa tiene
// que poder decirle al usuario cuánto sobra («312 de 280»), no un «es muy largo» que
// no ayuda a arreglarlo.
//
// ⚠️ NO ESTÁ EN LA TABLA DEL §8.1, y se añade a propósito. El contrato enumera los
// desenlaces del re-análisis, no los del saneo del texto libre — que es una puerta
// del Plan 041 (REQ-33e) que este endpoint REUSA. El alternativo era truncar en
// silencio, que es justo lo que REQ-33e prohíbe.
type noteTooLongResponse struct {
	Error string `json:"error"`
	Runes int    `json:"runes"`
	Max   int    `json:"max"`
}

// reanalyzeIntakeHandler sirve POST /api/v1/intakes/{id}/reanalyze: el dueño pide
// que la máquina vuelva a leer el pedido DESDE EL ORIGEN (Plan 044 · T4.6).
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
func reanalyzeIntakeHandler(svc ReanalysisService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		var req reanalyzeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReanalyzeBytes)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "cuerpo JSON inválido")
			return
		}

		out, err := svc.Reanalizar(r.Context(), reanalisis.Solicitud{
			TenantID: id.TenantID,
			IntakeID: r.PathValue("id"),
			Via:      req.Via,
			Text:     req.Text,
		})
		if err != nil {
			writeReanalyzeError(w, err)
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

// writeReanalyzeError traduce el fallo del dominio al código y al cuerpo del §8.1.
//
// ════════════════════════════════════════════════════════════════════════════
// EL ORDEN DE ESTE `switch` NO ES EL ORDEN DE LOS CHEQUEOS
// ════════════════════════════════════════════════════════════════════════════
//
// Quien decide en qué orden se comprueban las cosas es el dominio
// (`reanalisis.Servicio.Reanalizar`, donde está escrito y razonado): forma → gate
// base → gate de vía → credencial → solicitud → job vivo → fuente. Aquí solo llega
// UN error ya decidido, así que el orden de los `case` es indiferente y se escribe en
// el del contrato para que se lea al lado de la tabla del §8.1.
//
// 🔴 EL `default` ES 500 Y NO SE PUEDE ALCANZAR CON EL PROVEEDOR CAÍDO, que es el
// criterio INV-10 de T4.6. Este endpoint NO llama al modelo: abre un job y vuelve. Un
// proveedor muerto se descubre minutos después, en el worker, y allí lo que pasa es
// que el job se reintenta o muere con su causa escrita — la revisión anterior queda
// intacta, el intake no cambia de estado y el cliente no recibe nada. Por aquí solo
// se cae al 500 si se cae Postgres, que es un 500 honesto.
func writeReanalyzeError(w http.ResponseWriter, err error) {
	var (
		viaInvalida reanalisis.ViaInvalidaError
		featureNo   reanalisis.FeatureAusenteError
		credencial  reanalisis.CredencialAusenteError
		fuente      reanalisis.FuenteAusenteError
		enCurso     reanalisis.EnCursoError
		larga       cart.NoteTooLongError
	)
	switch {
	case errors.As(err, &viaInvalida):
		writeJSON(w, http.StatusBadRequest, invalidViaResponse{
			Error: "invalid_via", Via: viaInvalida.Via, ConfiguredVia: viaInvalida.Configurada,
		})
	case errors.As(err, &larga):
		writeJSON(w, http.StatusBadRequest, noteTooLongResponse{
			Error: "text_too_long", Runes: larga.Runes, Max: larga.Max,
		})
	case errors.As(err, &featureNo):
		writeJSON(w, http.StatusForbidden, featureDeniedResponse{
			Error: "feature_not_enabled", Feature: featureNo.Feature,
		})
	case errors.As(err, &credencial):
		writeJSON(w, http.StatusUnprocessableEntity, credentialsMissingResponse{
			Error: "llm_credentials_missing", Via: credencial.Via,
		})
	case errors.Is(err, intakes.ErrNotFound):
		// 404 y NUNCA 403: un 403 confirmaría que el id existe. «No existe» y «es de
		// otro tenant» tienen que ser la misma respuesta (INV-8).
		writeError(w, http.StatusNotFound, "solicitud no encontrada")
	case errors.As(err, &enCurso):
		writeJSON(w, http.StatusUnprocessableEntity, reanalysisInProgressResponse{
			Error: "reanalysis_in_progress", JobID: enCurso.JobID,
		})
	case errors.As(err, &fuente):
		writeJSON(w, http.StatusUnprocessableEntity, sourceUnavailableResponse{
			Error: "source_unavailable", Reason: fuente.Reason,
		})
	default:
		writeError(w, http.StatusInternalServerError, "no se pudo pedir el re-análisis de la solicitud")
	}
}
