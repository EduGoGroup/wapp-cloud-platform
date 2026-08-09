package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations/sigv1"
)

// Headers del callback (contrato wapp-crm-v1 · intake.status, D-042.5). SIN JWT: la
// autenticación entera es la firma HMAC sobre el cuerpo crudo.
const (
	headerCRMTenant    = "X-Wapp-Tenant"
	headerCRMTimestamp = "X-Wapp-Timestamp"
	headerCRMSignature = "X-Wapp-Signature"
)

// crmSignaturePrefix es el prefijo de versión del ESQUEMA de firma en el header.
// Va aquí y no en sigv1 porque allí ya vive su constructor (SignatureHeader) y lo
// que falta en el lado que verifica es el recorte.
const crmSignaturePrefix = "v1="

// crmCallbackWindow es la ventana anti-replay: ±300 s sobre X-Wapp-Timestamp
// (contrato §respuestas, 401). No es una defensa contra un atacante con el secreto
// —quien lo tiene puede firmar lo que quiera— sino contra la REPETICIÓN de un
// cuerpo legítimo capturado, y contra un puente con el reloj torcido, que es el
// caso que de verdad pasa.
const crmCallbackWindow = 300 * time.Second

// maxCRMCallbackBody acota el cuerpo del callback. Es un objeto de siete campos
// cortos; el techo existe para que un puente roto no se lleve la memoria por
// delante. Se lee ENTERO antes de verificar la firma porque la firma es sobre el
// cuerpo crudo: no hay forma de autenticar sin haberlo leído.
const maxCRMCallbackBody = 64 * 1024

// CRMSecretReader entrega el secreto HMAC del tenant para verificar el callback.
// Lo satisface *integrations.Postgres.
type CRMSecretReader interface {
	GetTenantSecret(ctx context.Context, tenantID string) (string, bool, error)
}

// CRMBridgeGate responde si el tenant puede usar el puente CRM: la feature comercial
// Y la integración configurada y encendida (D-042.8). Lo satisface
// *integrations.EntitlementsGate — el MISMO gate que decide si se encola la ida, de
// modo que un tenant no puede quedar en el absurdo de recibir la vuelta de algo que
// no se le mandó.
type CRMBridgeGate interface {
	Enabled(ctx context.Context, tenantID string) (bool, error)
}

// CRMReflector aplica el estado canónico del CRM sobre la solicitud del tenant.
// Lo satisface *intakes.Postgres.
type CRMReflector interface {
	ReflectCRMStatus(ctx context.Context, tenantID, intakeID, status, externalRef string,
		syncedAt time.Time) (intakes.CRMReflection, error)
}

// CRMStatusNotifier avisa al cliente final de que su pedido cambió de estado en el
// CRM. Es OPCIONAL (nil ⇒ no se avisa): el reflejo es la operación y el aviso es su
// consecuencia.
//
// NO devuelve error, y no es un olvido: es la regla 1 del notificador del Plan 041,
// que este puerto respeta en vez de inventarse otra. Cuando esto corre, el reflejo YA
// está escrito; un error hacia arriba haría que el puente reintentara y volviera a
// escribir lo mismo para nada, y el aviso tampoco se recuperaría por ese camino. Todo
// fallo se registra donde ocurre y muere ahí. Lo satisface *intakes.Notifier.
type CRMStatusNotifier interface {
	NotifyCRMStatus(ctx context.Context, tenantID string, in intakes.Intake, crmStatus string)
}

// crmCallbackRequest es el cuerpo del verbo intake.status.
//
// Se decodifica con DisallowUnknownFields, que es como se cumple en Go el
// `additionalProperties: false` del schema publicado: un campo de más —`tenant`, el
// primero que a un autor de puentes se le ocurre mandar— responde 422 en vez de
// ignorarse en silencio. Un test compara este validador contra el schema publicado
// caso por caso, que es lo que impide que las dos representaciones se separen.
type crmCallbackRequest struct {
	ContractVersion string `json:"contract_version"`
	Verb            string `json:"verb"`
	IntakeID        string `json:"intake_id"`
	Status          string `json:"status"`
	ExternalRef     string `json:"external_ref"`
	OccurredAt      string `json:"occurred_at"`
}

// validate aplica las reglas del schema que importan en la frontera. Devuelve el
// motivo para el 422; cadena vacía si el cuerpo es válido.
func (r crmCallbackRequest) validate() string {
	switch {
	case r.ContractVersion != "1":
		return "contract_version debe ser \"1\""
	case r.Verb != "intake.status":
		return "verb debe ser \"intake.status\""
	case strings.TrimSpace(r.IntakeID) == "":
		return "intake_id es obligatorio"
	case r.Status == "":
		return "status es obligatorio"
	case !intakes.IsCRMStatus(r.Status):
		// El mensaje NOMBRA los cuatro: quien mapea mal su CRM necesita saber contra
		// qué mapear, y el contrato dice que reintentar el mismo cuerpo dará siempre 422.
		return "status debe ser uno de: paid, preparing, delivered, rejected"
	case strings.TrimSpace(r.OccurredAt) == "":
		return "occurred_at es obligatorio"
	}
	if _, err := time.Parse(time.RFC3339, r.OccurredAt); err != nil {
		return "occurred_at debe ser una marca RFC3339"
	}
	return ""
}

// crmCallbackHandler atiende POST /api/v1/integrations/callback: la VUELTA del
// puente CRM (Plan 042 · T4.2).
//
// EL ORDEN DE LAS COMPROBACIONES ES LA MITAD DEL DISEÑO, y por eso va explicado:
//
//  1. Ventana y firma ANTES que nada. Mientras no se sepa que quien llama tiene el
//     secreto del tenant, no se responde nada que dependa del estado de ese tenant.
//  2. El gate (403) DESPUÉS de la firma. Al revés, un desconocido podría averiguar
//     qué tenants tienen contratado el puente por la diferencia entre 401 y 403.
//  3. Un tenant sin integración —y por tanto sin secreto— sale por 401, no por 403.
//     No es una imprecisión: sin secreto no hay forma de verificar la firma, así que
//     es literalmente indistinguible de una firma mala.
//  4. La solicitud ajena y la inexistente responden el MISMO 404, que es lo que
//     impide usar el callback como oráculo de identificadores (contrato §respuestas).
func crmCallbackHandler(secrets CRMSecretReader, gate CRMBridgeGate, reflector CRMReflector,
	notifier CRMStatusNotifier, log sharedlogger.Logger) http.Handler {
	// El logger llega nil en los tests del paquete (mismo trato que httpapi.authmw):
	// se envuelve una vez, aquí, en vez de repartir `if log != nil` por cada rama de
	// error del handler.
	warn := func(msg string, args ...any) {
		if log != nil {
			log.Warn(msg, args...)
		}
	}
	fail := func(msg string, args ...any) {
		if log != nil {
			log.Error(msg, args...)
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID, body, ok := authenticateCRMCallback(w, r, secrets, warn)
		if !ok {
			return // authenticateCRMCallback ya respondió
		}

		enabled, err := gate.Enabled(r.Context(), tenantID)
		if err != nil {
			// Fail-closed, mismo criterio que RequireFeature: el llamante no debe poder
			// distinguir "no lo tienes" de "no pude averiguarlo".
			warn("callback CRM: no se pudo resolver el puente del tenant", "tenant", tenantID, "error", err)
			enabled = false
		}
		if !enabled {
			writeError(w, http.StatusForbidden, "el puente CRM no está activo para este tenant")
			return
		}

		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.DisallowUnknownFields()
		var req crmCallbackRequest
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusUnprocessableEntity, crmDecodeReason(err))
			return
		}
		if reason := req.validate(); reason != "" {
			writeError(w, http.StatusUnprocessableEntity, reason)
			return
		}

		ref, err := reflector.ReflectCRMStatus(r.Context(), tenantID, req.IntakeID,
			req.Status, strings.TrimSpace(req.ExternalRef), time.Now().UTC())
		if err != nil {
			fail("callback CRM: no se pudo reflejar el estado", "tenant", tenantID, "error", err)
			writeError(w, http.StatusInternalServerError, "no se pudo aplicar el estado")
			return
		}
		if !ref.Found {
			writeError(w, http.StatusNotFound, "solicitud no encontrada")
			return
		}

		// El aviso al cliente va DESPUÉS de escribir y NO puede cambiar la respuesta:
		// el reflejo ya está aplicado y un puente que reintenta por un 5xx del aviso
		// volvería a escribir lo mismo para nada. Solo se avisa si algo CAMBIÓ — un
		// puente con reintentos manda el mismo estado muchas veces y el cliente no
		// puede recibir el mismo mensaje una vez por reintento.
		if ref.Changed && notifier != nil {
			notifyCRMSafely(r.Context(), notifier, tenantID, ref.Intake, req.Status, fail)
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"intake_id":  req.IntakeID,
			"crm_status": req.Status,
			"changed":    ref.Changed,
		})
	})
}

// notifyCRMSafely avisa al cliente conteniendo el pánico.
//
// El notificador del Plan 041 ya contiene los suyos (su regla 1), así que esto es
// redundante HOY y a propósito: cuando esta línea corre, el reflejo ya está escrito
// en la base, y un pánico de cualquier notificador —el de mañana, el de un módulo
// nuevo— convertiría una operación aplicada en un 500. El puente reintentaría y
// volvería a escribir lo mismo para nada, y el aviso no se recuperaría igualmente.
//
// No se traga el defecto: se registra entero en Error, que es donde hay que ir a
// buscarlo. Lo que se contiene es el ALCANCE del daño, no la noticia.
func notifyCRMSafely(ctx context.Context, notifier CRMStatusNotifier, tenantID string,
	in intakes.Intake, crmStatus string, fail func(string, ...any)) {
	defer func() {
		if rec := recover(); rec != nil {
			fail("callback CRM: pánico avisando al cliente; el reflejo YA está aplicado",
				"tenant", tenantID, "intake", in.ID, "panic", fmt.Sprint(rec))
		}
	}()
	notifier.NotifyCRMStatus(ctx, tenantID, in, crmStatus)
}

// authenticateCRMCallback resuelve la credencial ENTERA del puente: tenant del
// header, ventana anti-replay y firma HMAC sobre el cuerpo crudo. Devuelve el tenant
// autenticado y el cuerpo leído; con ok=false ya respondió y el llamante solo tiene
// que volver.
//
// Va en su propia función porque es una unidad con sentido —o el llamante es quien
// dice ser, o no hay conversación— y porque el handler que la usa ya lleva encima el
// gate, la validación del contrato y el reflejo.
//
// TODAS las formas de fallar la autenticación responden LO MISMO: header ausente,
// timestamp ilegible, fuera de ventana, tenant sin integración y firma mala son un
// único 401 sin detalle. Distinguirlos ayudaría más a quien sondea que a quien
// integra —el manual del puente sí los distingue, que es donde toca—.
func authenticateCRMCallback(w http.ResponseWriter, r *http.Request, secrets CRMSecretReader,
	warn func(string, ...any)) (tenantID string, body []byte, ok bool) {
	tenantID = strings.TrimSpace(r.Header.Get(headerCRMTenant))
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "no autenticado")
		return "", nil, false
	}
	ts, inWindow := crmCallbackTimestamp(r.Header.Get(headerCRMTimestamp))
	if !inWindow {
		writeError(w, http.StatusUnauthorized, "no autenticado")
		return "", nil, false
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCRMCallbackBody))
	if err != nil {
		writeTooLarge(w, "el cuerpo del callback", maxCRMCallbackBody)
		return "", nil, false
	}

	secret, found, err := secrets.GetTenantSecret(r.Context(), tenantID)
	if err != nil {
		// Un fallo de infraestructura NO se disfraza de 401: el puente reintentaría
		// creyendo que su secreto está mal y acabaría rotándolo para nada.
		warn("callback CRM: no se pudo leer el secreto del tenant", "tenant", tenantID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "no se pudo verificar la petición")
		return "", nil, false
	}
	signature := strings.TrimPrefix(r.Header.Get(headerCRMSignature), crmSignaturePrefix)
	// Sin integración no hay secreto que comparar; se responde lo mismo que a una
	// firma mala para no delatar qué tenants tienen puente.
	if !found || !sigv1.Verify(secret, ts, body, signature) {
		writeError(w, http.StatusUnauthorized, "no autenticado")
		return "", nil, false
	}
	return tenantID, body, true
}

// crmCallbackTimestamp lee y valida el header de tiempo contra la ventana
// anti-replay. Devuelve el instante Unix y si es aceptable.
func crmCallbackTimestamp(raw string) (int64, bool) {
	ts, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, false
	}
	delta := time.Since(time.Unix(ts, 0))
	if delta < 0 {
		delta = -delta // un puente adelantado es tan válido como uno atrasado
	}
	return ts, delta <= crmCallbackWindow
}

// crmDecodeReason traduce el fallo del decodificador a un motivo que le sirva al
// autor del puente. El campo de más se nombra tal cual viene, porque saber CUÁL
// sobra es la mitad de la corrección.
func crmDecodeReason(err error) string {
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &typeErr):
		return "el campo " + typeErr.Field + " tiene un tipo que el contrato no admite"
	case strings.Contains(err.Error(), "unknown field"):
		return "el cuerpo trae un campo que el contrato no admite: " +
			strings.TrimPrefix(err.Error(), "json: unknown field ")
	default:
		return "el cuerpo no es un intake.status válido"
	}
}
