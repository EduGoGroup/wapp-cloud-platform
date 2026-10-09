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
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
	panic(pendiente.Implementar("apipublica.MountIntents"))
}
