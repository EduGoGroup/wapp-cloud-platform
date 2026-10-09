// Porta internal/publicapi/intents.go @ 7ccb98e (192 líneas) y su registro
// (internal/publicapi/publicapi.go @ 8fa81a3, líneas 563-575).
//
// intents.go — LA CONFIG DEL CLASIFICADOR DE INTENCIONES DEL TENANT (Plan 029 · T5, ADR-0020/
// 0021/0022; mapa §2.5, E1 y E2): GET y PUT de /api/v1/intents. Es P1, el catálogo de
// intenciones, que se edita por API y no por fichero como los prompts P2–P5. La persistencia
// vive en internal/modulos/captacion/intentcfg y el contrato del blob en wapp-shared/intents;
// aquí solo se abre la puerta HTTP.
//
// En el rojo solo existían los dos puertos, IntentsDeps y MountIntents; los handlers y sus
// auxiliares nacieron con el verde (05 E-4, P6).

package apipublica

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	sharedintents "github.com/EduGoGroup/wapp-shared/intents"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// IntentConfigStore es el puerto de persistencia del blob de intents por tenant que la cara
// consume. Lo satisfacen *intentcfg.PostgresStore y *intentcfg.MemoryStore del módulo captación
// NUEVO (es la misma forma que intentcfg.Store). Toda operación va acotada al tenant, que sale
// del token (INV-8).
type IntentConfigStore interface {
	// Get devuelve la config del tenant. Sin config, un error que cumple
	// errors.Is(err, intentcfg.ErrNotFound) (el Postgres lo devuelve ENVUELTO).
	Get(ctx context.Context, tenantID string) (intentcfg.Config, error)
	// Upsert crea o SUSTITUYE entera la config del tenant con esa version de entidad.
	Upsert(ctx context.Context, tenantID, version string, blob []byte) error
}

// ConfigPusher empuja un ConfigUpdate (ADR-0021) a las sesiones vivas del tenant tras un PUT.
// Lo satisface el servidor gRPC del gateway (PushConfig). Es best-effort: un fallo de push NO
// invalida el PUT (la config ya quedó persistida y el push al conectar reconcilia).
type ConfigPusher interface {
	PushConfig(ctx context.Context, tenantID, kind, version string, payload []byte) error
}

// IntentsDeps es lo que E1 y E2 necesitan. En la cara vieja eran los campos Intents,
// Entitlements, ConfigPush y DBTimeout de publicapi.Deps.
type IntentsDeps struct {
	// Intents guarda y lee el blob. nil ⇒ E1 y E2 no se montan.
	Intents IntentConfigStore
	// Entitlements es el resolver de derechos del módulo acceso NUEVO: el gate de VERDAD de E2
	// (`llm_intent`). nil ⇒ E1 y E2 no se montan (tampoco E1, que no lo consulta: la familia
	// va junta).
	Entitlements entitlements.Resolver
	// ConfigPush empuja la config recién guardada a las sesiones vivas. Opcional: nil ⇒ E2
	// persiste y no empuja. NO condiciona el montaje.
	ConfigPush ConfigPusher
	// DBTimeout es el plazo de la LECTURA de E1, cableado desde config.PublicAPIDBTimeout.
	// <= 0 ⇒ 1,5 s. E2 no lo usa.
	DBTimeout time.Duration
}

// MountIntents registra en c las DOS rutas de /api/v1/intents, juntas o ninguna (una familia
// partida daría un 405 falso), solo si d.Intents y d.Entitlements son los dos distintos de nil.
// Si falta cualquiera, ninguna existe (404 de ruta inexistente, mejor que un 500 a medio
// camino). d.ConfigPush no cuenta para el montaje.
//
//   - E1 "GET /api/v1/intents": cadena R con permiso "intents.read";
//   - E2 "PUT /api/v1/intents": cadena W con permiso "intents.write" y recurso de auditoría
//     "intents".
//
// (Ver Common: 401 sin token; 403 {"error":"permiso denegado"} sin el permiso o con un token sin
// empresa; E1 no deja registro de auditoría y E2 deja exactamente uno por petición que pasa el
// permiso, "success" o "failure" según el código.)
//
// El tenant es SIEMPRE el del token (INV-8): no hay parámetro ni campo del cuerpo que lo cambie.
//
// E1 — leer. No lleva gate de feature: un tenant sin `llm_intent` lee (lo que tenga).
//
//   - d.Intents.Get(tenant) acotado por d.DBTimeout (<= 0 ⇒ 1,5 s);
//   - 200 {"version":…,"config":…}: la version de ENTIDAD guardada y el blob como JSON CRUDO,
//     tal cual lo devuelve el puerto (ni se tipa ni se reinterpreta; el codificador solo lo
//     compacta);
//   - plazo vencido ⇒ 504 {"error":"la lectura de la config de intents no respondió a tiempo,
//     reintenta"} y un Warn "lectura a BD vencida: se responde 504" en k.Log con
//     "op"="intents.get" y "tenant_id". El plazo se mira ANTES que los demás errores;
//   - errors.Is(err, intentcfg.ErrNotFound) —también envuelto— ⇒ 404 {"error":"el tenant no
//     tiene config de intents"};
//   - cualquier otro fallo de Get ⇒ 500 {"error":"no se pudo leer la config de intents"}, que
//     NO repite el error del puerto.
//
// E2 — guardar. Los pasos van en ESTE orden y cada uno corta:
//
//  1. GATE DE VERDAD (ADR-0022), DENTRO del handler y no en la cadena:
//     d.Entitlements.Has(tenant, entitlements.FeatureLLMIntent) con el contexto de la petición.
//     Sin la feature ⇒ 403 {"error":"el plan del tenant no incluye la clasificación de
//     intenciones"} (el {"error"} de siempre, NO el cuerpo feature_not_enabled del middleware).
//     Si el resolver FALLA ⇒ 500 {"error":"no se pudo verificar el entitlement"}: conducta
//     heredada del viejo (NO es fail-closed con 403, aunque su comentario lo dijera); en ningún
//     caso se abre la capacidad. El gate va ANTES de leer el cuerpo: un tenant sin la feature
//     recibe el 403 también con un blob inválido o demasiado grande. Como va por dentro de la
//     auditoría, el 403 del gate SÍ deja su registro ("failure", Meta {"status":403});
//  2. el cuerpo se lee acotado a sharedintents.MaxConfigBytes+1 (256 KiB + 1). Fallo de
//     lectura ⇒ 400 {"error":"no se pudo leer el cuerpo"};
//  3. más de MaxConfigBytes ⇒ 413 {"error":"la config excede el tamaño máximo de 262144
//     bytes","max_bytes":262144} (262144 bytes justos NO son un 413);
//  4. sharedintents.ParseAndValidate lo rechaza ⇒ 400 {"error":"config de intents inválida:
//     <el error del validador>"};
//  5. la version de ENTIDAD es el sha256 (sus 12 primeros hex) del JSON NORMALIZADO
//     (re-serializado desde su forma decodificada: claves ordenadas, sin espacio
//     insignificante), así que dos cuerpos con el mismo contenido lógico dan la misma version
//     (idempotencia del push, ADR-0021). Si no se pudiera calcular ⇒ 400 {"error":"el cuerpo
//     debe ser JSON válido"}: rama DEFENSIVA heredada, inalcanzable tras el paso 4;
//  6. d.Intents.Upsert(tenant, version, cuerpo) con el cuerpo BYTE A BYTE como llegó (no el
//     normalizado) y el contexto de la petición, SIN plazo propio (d.DBTimeout está calibrado
//     para lecturas). Falla ⇒ 500 {"error":"no se pudo persistir la config de intents"};
//  7. si d.ConfigPush no es nil: PushConfig(tenant, intentcfg.Kind, version, cuerpo), UNA vez y
//     best-effort. Su error NO cambia la respuesta: solo deja en k.Log (si no es nil) el Warn
//     "intents: push de config best-effort falló (persistida; reconcilia al conectar)" con
//     "tenant_id", "version" y "error". Conducta heredada: el push usa el contexto de la
//     PETICIÓN (no uno desligado con context.WithoutCancel), así que un cliente que cuelga
//     puede cancelarlo; el push al conectar reconcilia;
//  8. 200 {"version":<la version>}.
//
// Un PUT cortado en los pasos 1–5 no llama a Upsert ni a PushConfig; uno cortado en el 6 no
// llama a PushConfig.
//
// `event_kind` (Plan 043 · T5.3, D-043.9): un blob que lo trae por intent valida igual que uno
// que no (el validador tolera claves desconocidas) y se guarda TAL CUAL, pero es INERTE en el
// Cloud: no arma ni acota ninguna regla (el scoping real sale de flow_triggers, kind='llm').
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una
// identidad sin empresa que llegara a un handler recibiría 401 {"error":"autenticación
// requerida"}.
//
// Fallo de cableado: k.MW nil con las dos dependencias presentes hace panic AL MONTAR (ver
// Common).
func MountIntents(c *Cara, k Common, d IntentsDeps) {
	// Solo se montan si el store y el resolver están cableados: un 404 de ruta inexistente es
	// mejor que un 500 a medio camino o que un PUT que no puede comprobar el plan.
	if d.Intents == nil || d.Entitlements == nil {
		return
	}
	mustHaveMW(k, "MountIntents")

	// GET lee el blob vigente (intents.read); PUT valida el contrato (wapp-shared/intents),
	// exige la feature llm_intent (gate de verdad ⇒ 403 sin ella), persiste y empuja el
	// ConfigUpdate a las sesiones vivas del tenant. Escritura auditada; lectura sin auditoría.
	c.Handle("GET /api/v1/intents", protectRead(k, "intents.read",
		intentsGetHandler(d.Intents, d.DBTimeout, k.Log)))
	c.Handle("PUT /api/v1/intents", protect(k, "intents.write", "intents",
		intentsPutHandler(d.Intents, d.Entitlements, d.ConfigPush, k.Log)))
}

// intentsConfigResponse (intentConfigResponse en la cara vieja) es la respuesta de E1: la
// version de entidad + el blob de config crudo (verbatim, ya validado al persistir).
type intentsConfigResponse struct {
	Version string          `json:"version"`
	Config  json.RawMessage `json:"config"`
}

// intentsGetHandler (getIntentsHandler en la cara vieja) sirve E1: el blob de intents del
// tenant del token (INV-8) con su version de entidad.
//
// dbTimeout acota la lectura (Plan 050 · Ola 3 · T3.3, ver dbCtx). El PUT hermano queda FUERA a
// propósito: escribe, y el presupuesto está calibrado para lecturas.
//
// La rama vieja «store nil ⇒ 500 store de intents no configurado» no se porta: la ruta solo se
// monta con un store no nil (MountIntents), así que era inalcanzable.
func intentsGetHandler(store IntentConfigStore, dbTimeout time.Duration, log sharedlogger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		ctx, cancel := dbCtx(r.Context(), dbTimeout)
		defer cancel()
		cfg, err := store.Get(ctx, id.TenantID)
		if err != nil {
			if dbTimedOut504(w, log, err, "la lectura de la config de intents no respondió a tiempo, reintenta",
				"op", "intents.get", "tenant_id", id.TenantID) {
				return
			}
			if errors.Is(err, intentcfg.ErrNotFound) {
				writeError(w, http.StatusNotFound, "el tenant no tiene config de intents")
				return
			}
			writeError(w, http.StatusInternalServerError, "no se pudo leer la config de intents")
			return
		}
		writeJSON(w, http.StatusOK, intentsConfigResponse{Version: cfg.Version, Config: cfg.Blob})
	})
}

// intentsPutHandler (putIntentsHandler en la cara vieja) sirve E2: exige la feature llm_intent
// (gate de verdad, ADR-0022), valida el blob con wapp-shared/intents, fija la version de
// entidad (hash del blob normalizado), persiste y empuja el ConfigUpdate a las sesiones vivas
// del tenant (ADR-0021). El tenant SIEMPRE sale del token (INV-8).
//
// event_kind del blob (Plan 043 · T5.3, D-043.9): el Cloud NO lee `event_kind` del blob: el
// scoping por evento activo sale de flow_triggers (regla kind='llm'). El campo es informativo
// para un Edge futuro. Si un tenant lo incluye por intent, el body sigue validando
// (ParseAndValidate no usa DisallowUnknownFields) y se persiste tal cual, pero es INERTE en el
// Cloud: no arma ni acota ninguna regla.
//
// La rama vieja «store o checker nil ⇒ 500 API de intents no configurada» no se porta: la ruta
// solo se monta con los dos (MountIntents), así que era inalcanzable.
func intentsPutHandler(store IntentConfigStore, ents entitlements.Resolver, pusher ConfigPusher, log sharedlogger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		// Gate de VERDAD (ADR-0022): sin la feature, la superficie de administración se
		// rechaza (403). Un fallo del resolver NO abre la capacidad, pero tampoco se disfraza
		// de «sin la feature»: responde 500, como la cara vieja (su comentario decía que se
		// trataba como sin la feature; el código nunca lo hizo, y manda el código).
		has, err := ents.Has(r.Context(), id.TenantID, entitlements.FeatureLLMIntent)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo verificar el entitlement")
			return
		}
		if !has {
			writeError(w, http.StatusForbidden, "el plan del tenant no incluye la clasificación de intenciones")
			return
		}

		// Cortafuegos de tamaño ANTES de leer todo: el contrato acota el blob a
		// MaxConfigBytes (wapp-shared/intents). +1 detecta el exceso.
		body, err := io.ReadAll(io.LimitReader(r.Body, sharedintents.MaxConfigBytes+1))
		if err != nil {
			writeError(w, http.StatusBadRequest, "no se pudo leer el cuerpo")
			return
		}
		if len(body) > sharedintents.MaxConfigBytes {
			writeTooLarge(w, "la config", sharedintents.MaxConfigBytes)
			return
		}

		// Validación del contrato (ParseAndValidate): nombres únicos/kebab, >=1 ejemplo por
		// intent, umbral en rango, etc. Config inválida ⇒ 400.
		if _, verr := sharedintents.ParseAndValidate(body); verr != nil {
			writeError(w, http.StatusBadRequest, "config de intents inválida: "+verr.Error())
			return
		}

		// Defensa heredada: tras ParseAndValidate el cuerpo ya es JSON, así que esta rama no
		// se alcanza. Se conserva por si el validador dejara un día de decodificar.
		version, err := intentsEntityVersion(body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "el cuerpo debe ser JSON válido")
			return
		}

		if err := store.Upsert(r.Context(), id.TenantID, version, body); err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo persistir la config de intents")
			return
		}

		// Push best-effort a las sesiones vivas del tenant (ADR-0021). Un fallo NO invalida el
		// PUT: la config ya está persistida y el push al conectar reconcilia (hace de
		// reintento; no hay reintentos aquí). El error se registra pero no se propaga al
		// cliente. Va con el contexto de la PETICIÓN, como en la cara vieja.
		if pusher != nil {
			if perr := pusher.PushConfig(r.Context(), id.TenantID, intentcfg.Kind, version, body); perr != nil && log != nil {
				log.Warn("intents: push de config best-effort falló (persistida; reconcilia al conectar)",
					"tenant_id", id.TenantID, "version", version, "error", perr)
			}
		}

		writeJSON(w, http.StatusOK, map[string]string{"version": version})
	})
}

// intentsEntityVersion (entityVersion en la cara vieja) calcula la version de ENTIDAD del blob:
// sha256 (12 hex) del JSON NORMALIZADO (re-serializado desde su forma decodificada, lo que
// ordena las claves de objeto y descarta el espacio en blanco insignificante). Así dos cuerpos
// con el mismo contenido lógico producen la misma version (idempotencia del push, ADR-0021).
func intentsEntityVersion(body []byte) (string, error) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return "", err
	}
	norm, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(norm)
	return hex.EncodeToString(sum[:])[:12], nil
}
