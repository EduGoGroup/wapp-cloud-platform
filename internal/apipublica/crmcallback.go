// Porta internal/publicapi/crmcallback.go @ ed60c24 (310 líneas) y su registro
// (internal/publicapi/publicapi.go @ ed60c24, registerCRMCallback, líneas 1035-1057).
//
// crmcallback.go — LA VUELTA DEL PUENTE CRM (Plan 042 · T4.2, D-042.5; mapa §2.7, G17):
// POST /api/v1/integrations/callback, el verbo `intake.status` del contrato wapp-crm-v1. El
// dominio vive en internal/modulos/solicitudes (integrations, integrations/sigv1 e intakes); aquí
// solo se abre la puerta HTTP.
//
// 🔴 NO PASA POR protect NI POR protectRead, y esa es la diferencia con todo lo demás de la cara
// (FX T-7): el callback no lleva JWT. Quien llama es el puente del cliente, un proceso que no
// tiene usuario ni sesión, y su credencial es la firma HMAC sobre el cuerpo crudo con el secreto
// de firma del puente (D-042.5). Meterlo bajo el middleware de sesión lo dejaría inalcanzable;
// darle un token lo convertiría en un usuario más, que es justo lo que el contrato evita.
//
// El secreto que aquí se lee es el de la firma del puente, del envelope de NEGOCIO de esta
// pieza: vive solo dentro de la verificación y no sale por ninguna puerta ni por ningún log.
//
// En el rojo solo existían los cuatro puertos, CRMCallbackDeps y MountCRMCallback; el handler y
// sus auxiliares nacieron con el verde (05 E-4, P6).

package apipublica

import (
	"bytes"
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

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations/sigv1"
)

// Headers del callback (contrato wapp-crm-v1 · intake.status, D-042.5). SIN JWT: la
// autenticación entera es la firma HMAC sobre el cuerpo crudo.
const (
	crmHeaderTenant    = "X-Wapp-Tenant"
	crmHeaderTimestamp = "X-Wapp-Timestamp"
	crmHeaderSignature = "X-Wapp-Signature"
)

// crmSignaturePrefix es el prefijo de versión del ESQUEMA de firma en el header. Va aquí y no
// en sigv1 porque allí ya vive su constructor (SignatureHeader) y lo que falta en el lado que
// verifica es el recorte.
const crmSignaturePrefix = "v1="

// crmCallbackWindow es la ventana anti-replay: ±300 s sobre X-Wapp-Timestamp (contrato
// §respuestas, 401). No es una defensa contra un atacante con el secreto —quien lo tiene puede
// firmar lo que quiera— sino contra la REPETICIÓN de un cuerpo legítimo capturado, y contra un
// puente con el reloj torcido, que es el caso que de verdad pasa.
const crmCallbackWindow = 300 * time.Second

// crmCallbackMaxBody acota el cuerpo del callback. Es un objeto de siete campos cortos; el
// techo existe para que un puente roto no se lleve la memoria por delante. Se lee ENTERO antes
// de verificar la firma porque la firma es sobre el cuerpo crudo: no hay forma de autenticar
// sin haberlo leído.
const crmCallbackMaxBody = 64 * 1024

// CRMSecretReader entrega el secreto de firma del puente del tenant, para verificar el
// callback. Lo satisface *integrations.Postgres del módulo solicitudes NUEVO.
type CRMSecretReader interface {
	// GetTenantSecret devuelve el secreto de tenantID. found false ⇒ el tenant no tiene
	// integración, o la tiene sin secreto.
	GetTenantSecret(ctx context.Context, tenantID string) (secret string, found bool, err error)
}

// CRMBridgeGate responde si el tenant puede usar el puente CRM: la feature comercial Y la
// integración configurada, encendida y con destino (D-042.8, D-F6-11). Lo satisface
// *integrations.EntitlementsGate — el MISMO gate que decide si se encola la ida, de modo que un
// tenant no puede quedar en el absurdo de recibir la vuelta de algo que no se le mandó.
type CRMBridgeGate interface {
	// Enabled dice si el puente de tenantID está abierto. Un gate cerrado no es un error.
	Enabled(ctx context.Context, tenantID string) (bool, error)
}

// CRMReflector aplica el estado canónico del CRM sobre la solicitud del tenant. Lo satisface
// *intakes.Postgres del módulo solicitudes NUEVO.
type CRMReflector interface {
	// ReflectCRMStatus refleja status en la solicitud intakeID DE tenantID. Found false ⇒ no
	// existe o es de otro tenant, sin distinguir cuál.
	ReflectCRMStatus(ctx context.Context, tenantID, intakeID, status, externalRef string,
		syncedAt time.Time) (intakes.CRMReflection, error)
}

// CRMStatusNotifier avisa al cliente final de que su pedido cambió de estado en el CRM. Es
// OPCIONAL (nil ⇒ no se avisa): el reflejo es la operación y el aviso es su consecuencia.
//
// NO devuelve error, y no es un olvido: es la regla 1 del notificador del Plan 041, que este
// puerto respeta en vez de inventarse otra. Cuando esto corre, el reflejo YA está escrito; un
// error hacia arriba haría que el puente reintentara y volviera a escribir lo mismo para nada,
// y el aviso tampoco se recuperaría por ese camino. Todo fallo se registra donde ocurre y muere
// ahí. Lo satisface *intakes.Notifier.
type CRMStatusNotifier interface {
	// NotifyCRMStatus avisa al cliente de in de que su pedido pasó a crmStatus.
	NotifyCRMStatus(ctx context.Context, tenantID string, in intakes.Intake, crmStatus string)
}

// CRMCallbackDeps es lo que G17 necesita. En la cara vieja eran los campos CRMSecrets, CRMGate,
// CRMReflect y CRMNotify de publicapi.Deps; Now es nuevo (el viejo leía el reloj del proceso).
type CRMCallbackDeps struct {
	// CRMSecrets entrega el secreto con el que se verifica la firma. nil ⇒ G17 no se monta.
	CRMSecrets CRMSecretReader
	// CRMGate combina la feature comercial y la integración encendida. nil ⇒ G17 no se monta.
	CRMGate CRMBridgeGate
	// CRMReflect aplica el estado sobre la solicitud. nil ⇒ G17 no se monta.
	CRMReflect CRMReflector
	// CRMNotify avisa al cliente final del cambio reflejado. OPCIONAL: nil ⇒ G17 se monta y
	// refleja igual, sin avisar. ⚠️ Tiene que ser un nil de interfaz: un puntero nil metido en
	// la interfaz NO es nil, y el aviso se intentaría (el pánico se contiene, ver abajo).
	CRMNotify CRMStatusNotifier
	// Now es el reloj contra el que se mide la ventana anti-replay y con el que se sella el
	// reflejo. nil ⇒ time.Now. Lo inyectan los tests.
	Now func() time.Time
}

// MountCRMCallback registra en c "POST /api/v1/integrations/callback" (G17), solo si
// d.CRMSecrets, d.CRMGate y d.CRMReflect son los tres distintos de nil. Faltando cualquiera la
// ruta NO existe (404 de ruta inexistente): sin secreto no se puede autenticar, sin gate no se
// puede decidir y sin reflector no hay dónde escribir, y un 404 es mejor que un 500 a mitad de
// una puerta de autenticación. d.CRMNotify es opcional y no condiciona el montaje.
//
// 🔴 SIN Authenticate NI RequirePermission (FX T-7): la cadena es SOLO el access-log de k.Log
// (ver Common: una línea por petición, con "method", "path" y "status", y sin "tenant_id",
// porque aquí no hay identidad de token). Por eso este Mount NO exige k.MW: con MW nil monta y
// sirve igual, y un Authorization que venga —válido, inválido o ausente— ni se mira. NO deja
// registro de auditoría en ningún desenlace; k.Auditor no se usa. Con k.Log nil no hay
// access-log ni líneas de diagnóstico, y las respuestas son las mismas.
//
// La credencial ENTERA son tres headers y el cuerpo:
//
//   - "X-Wapp-Tenant": el tenant que dice ser quien llama (se recortan los espacios de los
//     bordes). Es el ÚNICO sitio del que sale el tenant: el cuerpo no lo lleva;
//   - "X-Wapp-Timestamp": segundos Unix en decimal (dígitos ASCII, con signo opcional; se
//     recortan los espacios de los bordes), que deben caer en la ventana de ±300 s —los dos
//     bordes incluidos— alrededor de d.Now();
//   - "X-Wapp-Signature": "v1=" + la firma sigv1 en hex del timestamp y del cuerpo CRUDO, byte
//     a byte, con el secreto del tenant. Se verifica en tiempo constante (sigv1.Verify). El
//     prefijo "v1=" se quita UNA vez si está; el hex a secas también verifica, y en mayúsculas
//     también.
//
// EL ORDEN DE LAS COMPROBACIONES ES LA MITAD DEL DISEÑO. La primera que falla responde:
//
//  1. sin "X-Wapp-Tenant" (o en blanco) ⇒ 401 {"error":"no autenticado"};
//  2. "X-Wapp-Timestamp" ausente, ilegible (vacío, con decimales, con espacios por dentro, con
//     dígitos que no son ASCII, fuera del rango de int64) o fuera de la ventana ⇒ el MISMO 401.
//     Hasta aquí no se ha leído el cuerpo ni consultado nada;
//  3. cuerpo de más de 64 KiB (65536 bytes justos sí entran) ⇒ 413 {"error":"el cuerpo del
//     callback excede el tamaño máximo de 65536 bytes","max_bytes":65536}, sin consultar el
//     secreto. Un fallo al leer el cuerpo responde ese mismo 413;
//  4. GetTenantSecret falla ⇒ 503 {"error":"no se pudo verificar la petición"} y un Warn
//     "callback CRM: no se pudo leer el secreto del tenant" con "tenant" y "error": un fallo de
//     infraestructura NO se disfraza de 401, o el puente acabaría rotando su secreto para nada;
//  5. tenant sin secreto, o firma que no verifica (de otro secreto, de otro timestamp, de otro
//     cuerpo, malformada, vacía, ausente) ⇒ el MISMO 401 {"error":"no autenticado"}. Un tenant
//     sin integración sale por 401 y no por 403: sin secreto es indistinguible de una firma
//     mala, y así no se delata qué tenants tienen puente. TODAS las formas de fallar la
//     autenticación responden lo mismo, sin detalle;
//  6. SOLO con la firma buena se consulta el gate: cerrado ⇒ 403 {"error":"el puente CRM no
//     está activo para este tenant"}. Si Enabled falla, es fail-closed: el mismo 403 y un Warn
//     "callback CRM: no se pudo resolver el puente del tenant". Va DESPUÉS de la firma para que
//     un desconocido no pueda averiguar quién tiene contratado el puente por la diferencia
//     entre 401 y 403;
//  7. el cuerpo no es un `intake.status` válido ⇒ 422 con el motivo (ver abajo); el reflector
//     no se llama;
//  8. ReflectCRMStatus falla ⇒ 500 {"error":"no se pudo aplicar el estado"} y un Error
//     "callback CRM: no se pudo reflejar el estado"; el error del puerto no viaja al cliente;
//  9. la solicitud no existe O es de otro tenant (Found false) ⇒ el MISMO 404
//     {"error":"solicitud no encontrada"}: el callback no sirve de oráculo de identificadores;
//  10. 200 {"changed":<bool>,"crm_status":"<status>","intake_id":"<intake_id>"}, con las claves
//     en ese orden (el alfabético, el del viejo) y los valores tal cual llegaron en el cuerpo.
//
// El 422 (el `additionalProperties:false` y los `required` del schema publicado,
// docs/contracts/wapp-crm-v1/intake.status.schema.json; reintentar el mismo cuerpo dará siempre
// 422). Los motivos, en el orden en que se comprueban:
//
//   - un campo que el contrato no tiene ⇒ `el cuerpo trae un campo que el contrato no admite:
//     "<campo>"` (se nombra el que sobra; "tenant" es el primero que se le ocurre mandar a un
//     autor de puentes);
//   - un campo con otro tipo ⇒ "el campo <campo> tiene un tipo que el contrato no admite"; un
//     cuerpo que es JSON pero no un objeto (una lista, una cadena) da ese mismo motivo con el
//     nombre VACÍO («el campo  tiene…», con sus dos espacios: es el texto del viejo);
//   - no es JSON, está vacío o está truncado ⇒ "el cuerpo no es un intake.status válido";
//   - un `null` o un `{}` no fallan al decodificar: caen en la primera regla de abajo;
//   - `contract_version debe ser "1"` · `verb debe ser "intake.status"` · "intake_id es
//     obligatorio" (vacío o solo espacios) · "status es obligatorio" · "status debe ser uno de:
//     paid, preparing, delivered, rejected" (el mensaje NOMBRA los cuatro: quien mapea mal su
//     CRM necesita saber contra qué mapear) · "occurred_at es obligatorio" · "occurred_at debe
//     ser una marca RFC3339".
//
// El reflejo: ReflectCRMStatus recibe el tenant AUTENTICADO del header, el intake_id y el status
// del cuerpo, external_ref recortado de espacios, y como syncedAt el instante de d.Now() en UTC
// (NO el occurred_at del cuerpo, que solo se valida).
//
// El aviso al cliente va DESPUÉS de escribir y NO puede cambiar la respuesta:
//
//   - se llama a d.CRMNotify.NotifyCRMStatus(tenant, la solicitud reflejada, status) SOLO si el
//     reflejo CAMBIÓ algo (Changed): un puente con reintentos manda el mismo estado muchas
//     veces y el cliente no puede recibir el mismo mensaje una vez por reintento;
//   - con d.CRMNotify nil no se avisa y la respuesta es la misma;
//   - un PÁNICO del notificador se contiene: la respuesta sigue siendo el 200 y queda un Error
//     "callback CRM: pánico avisando al cliente; el reflejo YA está aplicado" con "tenant",
//     "intake" y "panic".
//
// ⚠️ DIVERGENCIAS con la cara vieja, las dos aceptadas:
//
//   - D-F6-11: con el gate nuevo (integrations.EntitlementsGate exige endpoint y secreto), un
//     callback BIEN FIRMADO de un tenant con el puente encendido pero SIN endpoint_url pasa de
//     200 a 403. (Sin secreto ya era 401 en las dos: no hay con qué verificar la firma.) La
//     diferencia es del gate, no de esta puerta, que le pregunta lo mismo que antes;
//   - la ventana: el viejo aceptaba un timestamp a más de ~292 años en el FUTURO —el caso real
//     es un puente que manda MILISEGUNDOS en vez de segundos—, porque la resta de instantes
//     satura en el mínimo de time.Duration y su negación desborda y sigue siendo negativa
//     (internal/publicapi/crmcallback.go:289-293). Un mensaje así no caducaba nunca. Aquí es
//     401, que es lo que el contrato dice de todo lo que cae fuera de ±300 s.
//
// Rarezas del viejo que se conservan: solo se decodifica el PRIMER valor JSON del cuerpo (lo que
// venga detrás del objeto no se mira, aunque sí está cubierto por la firma); una clave repetida
// toma su última aparición; y, en dos cosas, la frontera es MÁS LAXA que el schema publicado:
// un campo a null cuenta como ausente (`"external_ref":null` pasa) y las claves casan sin
// distinguir mayúsculas (`"STATUS"` vale por `"status"`), que es lo que hace encoding/json.
func MountCRMCallback(c *Cara, k Common, d CRMCallbackDeps) {
	// Las tres dependencias son obligatorias; el notificador, no.
	if d.CRMSecrets == nil || d.CRMGate == nil || d.CRMReflect == nil {
		return
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	// accessLog explícito: es la ÚNICA ruta pública que no pasa por protect/protectRead (se
	// autentica por firma HMAC del CRM, no por Context Token), así que sin esto sería el agujero
	// que deja el «toda petición deja rastro» a medias.
	c.Handle("POST /api/v1/integrations/callback",
		accessLog(k.Log, crmCallbackHandler(d.CRMSecrets, d.CRMGate, d.CRMReflect, d.CRMNotify, now, k.Log)))
}

// crmCallbackRequest es el cuerpo del verbo intake.status.
//
// Se decodifica con DisallowUnknownFields, que es como se cumple en Go el
// `additionalProperties: false` del schema publicado: un campo de más —`tenant`, el primero que
// a un autor de puentes se le ocurre mandar— responde 422 en vez de ignorarse en silencio. Un
// test compara este validador contra el schema publicado caso por caso, que es lo que impide
// que las dos representaciones se separen.
type crmCallbackRequest struct {
	ContractVersion string `json:"contract_version"`
	Verb            string `json:"verb"`
	IntakeID        string `json:"intake_id"`
	Status          string `json:"status"`
	ExternalRef     string `json:"external_ref"`
	OccurredAt      string `json:"occurred_at"`
}

// validate aplica las reglas del schema que importan en la frontera. Devuelve el motivo para el
// 422; cadena vacía si el cuerpo es válido.
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
		// El mensaje NOMBRA los cuatro: quien mapea mal su CRM necesita saber contra qué
		// mapear, y el contrato dice que reintentar el mismo cuerpo dará siempre 422.
		return "status debe ser uno de: paid, preparing, delivered, rejected"
	case strings.TrimSpace(r.OccurredAt) == "":
		return "occurred_at es obligatorio"
	}
	if _, err := time.Parse(time.RFC3339, r.OccurredAt); err != nil {
		return "occurred_at debe ser una marca RFC3339"
	}
	return ""
}

// crmCallbackResponse es el 200 del callback. Los campos van en orden ALFABÉTICO de su clave
// porque el viejo respondía con un mapa, que encoding/json serializa ordenado: así el cuerpo es
// el mismo byte a byte.
type crmCallbackResponse struct {
	Changed   bool   `json:"changed"`
	CRMStatus string `json:"crm_status"`
	IntakeID  string `json:"intake_id"`
}

// crmCallbackHandler atiende POST /api/v1/integrations/callback: la VUELTA del puente CRM.
//
// EL ORDEN DE LAS COMPROBACIONES ES LA MITAD DEL DISEÑO, y por eso va explicado:
//
//  1. Ventana y firma ANTES que nada. Mientras no se sepa que quien llama tiene el secreto del
//     tenant, no se responde nada que dependa del estado de ese tenant.
//  2. El gate (403) DESPUÉS de la firma. Al revés, un desconocido podría averiguar qué tenants
//     tienen contratado el puente por la diferencia entre 401 y 403.
//  3. Un tenant sin integración —y por tanto sin secreto— sale por 401, no por 403. No es una
//     imprecisión: sin secreto no hay forma de verificar la firma, así que es literalmente
//     indistinguible de una firma mala.
//  4. La solicitud ajena y la inexistente responden el MISMO 404, que es lo que impide usar el
//     callback como oráculo de identificadores (contrato §respuestas).
func crmCallbackHandler(secrets CRMSecretReader, gate CRMBridgeGate, reflector CRMReflector,
	notifier CRMStatusNotifier, now func() time.Time, log sharedlogger.Logger) http.Handler {
	// El logger puede llegar nil (Common.Log es opcional): se envuelve una vez, aquí, en vez de
	// repartir `if log != nil` por cada rama de error del handler.
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
		tenantID, body, ok := crmAuthenticate(w, r, secrets, now, warn)
		if !ok {
			return // crmAuthenticate ya respondió
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

		dec := json.NewDecoder(bytes.NewReader(body))
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
			req.Status, strings.TrimSpace(req.ExternalRef), now().UTC())
		if err != nil {
			fail("callback CRM: no se pudo reflejar el estado", "tenant", tenantID, "error", err)
			writeError(w, http.StatusInternalServerError, "no se pudo aplicar el estado")
			return
		}
		if !ref.Found {
			writeError(w, http.StatusNotFound, "solicitud no encontrada")
			return
		}

		// El aviso al cliente va DESPUÉS de escribir y NO puede cambiar la respuesta: el reflejo
		// ya está aplicado y un puente que reintenta por un 5xx del aviso volvería a escribir lo
		// mismo para nada. Solo se avisa si algo CAMBIÓ — un puente con reintentos manda el
		// mismo estado muchas veces y el cliente no puede recibir el mismo mensaje una vez por
		// reintento.
		if ref.Changed && notifier != nil {
			crmNotifySafely(r.Context(), notifier, tenantID, ref.Intake, req.Status, fail)
		}

		writeJSON(w, http.StatusOK, crmCallbackResponse{
			Changed:   ref.Changed,
			CRMStatus: req.Status,
			IntakeID:  req.IntakeID,
		})
	})
}

// crmNotifySafely avisa al cliente conteniendo el pánico.
//
// El notificador del Plan 041 ya contiene los suyos (su regla 1), así que esto es redundante
// HOY y a propósito: cuando esta línea corre, el reflejo ya está escrito en la base, y un
// pánico de cualquier notificador —el de mañana, el de un módulo nuevo— convertiría una
// operación aplicada en un 500. El puente reintentaría y volvería a escribir lo mismo para
// nada, y el aviso no se recuperaría igualmente.
//
// No se traga el defecto: se registra entero en Error, que es donde hay que ir a buscarlo. Lo
// que se contiene es el ALCANCE del daño, no la noticia.
func crmNotifySafely(ctx context.Context, notifier CRMStatusNotifier, tenantID string,
	in intakes.Intake, crmStatus string, fail func(string, ...any)) {
	defer func() {
		if rec := recover(); rec != nil {
			fail("callback CRM: pánico avisando al cliente; el reflejo YA está aplicado",
				"tenant", tenantID, "intake", in.ID, "panic", fmt.Sprint(rec))
		}
	}()
	notifier.NotifyCRMStatus(ctx, tenantID, in, crmStatus)
}

// crmAuthenticate resuelve la credencial ENTERA del puente: tenant del header, ventana
// anti-replay y firma HMAC sobre el cuerpo crudo. Devuelve el tenant autenticado y el cuerpo
// leído; con ok=false ya respondió y el llamante solo tiene que volver.
//
// Va en su propia función porque es una unidad con sentido —o el llamante es quien dice ser, o
// no hay conversación— y porque el handler que la usa ya lleva encima el gate, la validación
// del contrato y el reflejo.
//
// TODAS las formas de fallar la autenticación responden LO MISMO: header ausente, timestamp
// ilegible, fuera de ventana, tenant sin integración y firma mala son un único 401 sin detalle.
// Distinguirlos ayudaría más a quien sondea que a quien integra —el manual del puente sí los
// distingue, que es donde toca—.
func crmAuthenticate(w http.ResponseWriter, r *http.Request, secrets CRMSecretReader,
	now func() time.Time, warn func(string, ...any)) (tenantID string, body []byte, ok bool) {
	tenantID = strings.TrimSpace(r.Header.Get(crmHeaderTenant))
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "no autenticado")
		return "", nil, false
	}
	ts, inWindow := crmCallbackTimestamp(r.Header.Get(crmHeaderTimestamp), now())
	if !inWindow {
		writeError(w, http.StatusUnauthorized, "no autenticado")
		return "", nil, false
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, crmCallbackMaxBody))
	if err != nil {
		writeTooLarge(w, "el cuerpo del callback", crmCallbackMaxBody)
		return "", nil, false
	}

	secret, found, err := secrets.GetTenantSecret(r.Context(), tenantID)
	if err != nil {
		// Un fallo de infraestructura NO se disfraza de 401: el puente reintentaría creyendo
		// que su secreto está mal y acabaría rotándolo para nada.
		warn("callback CRM: no se pudo leer el secreto del tenant", "tenant", tenantID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "no se pudo verificar la petición")
		return "", nil, false
	}
	signature := strings.TrimPrefix(r.Header.Get(crmHeaderSignature), crmSignaturePrefix)
	// Sin integración no hay secreto que comparar; se responde lo mismo que a una firma mala
	// para no delatar qué tenants tienen puente. sigv1.Verify compara en tiempo constante.
	if !found || !sigv1.Verify(secret, ts, body, signature) {
		writeError(w, http.StatusUnauthorized, "no autenticado")
		return "", nil, false
	}
	return tenantID, body, true
}

// crmCallbackTimestamp lee y valida el header de tiempo contra la ventana anti-replay, medida
// desde now. Devuelve el instante Unix y si es aceptable.
//
// strconv.ParseInt solo admite dígitos ASCII (con signo): un «١٢٣» o un espacio por dentro son
// un timestamp ilegible, no uno distinto.
func crmCallbackTimestamp(raw string, now time.Time) (int64, bool) {
	ts, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, false
	}
	delta := now.Sub(time.Unix(ts, 0))
	if delta < 0 {
		delta = -delta // un puente adelantado es tan válido como uno atrasado
	}
	// `delta >= 0` NO es redundante, y es la única línea que el viejo no tenía: para un
	// timestamp a más de ~292 años en el futuro, Sub satura en el MÍNIMO de time.Duration, cuya
	// negación desborda y vuelve a ser ese mismo mínimo. Sin la guarda, ese negativo «cabría»
	// en la ventana, y un puente que mandara milisegundos en vez de segundos (año ~58000)
	// pasaría por actual para siempre: justo la repetición que la ventana existe para cortar.
	return ts, delta >= 0 && delta <= crmCallbackWindow
}

// crmDecodeReason traduce el fallo del decodificador a un motivo que le sirva al autor del
// puente. El campo de más se nombra tal cual viene, porque saber CUÁL sobra es la mitad de la
// corrección.
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
