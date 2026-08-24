// Package config define la configuración de arranque de la Plataforma Cloud y
// su carga desde archivo YAML con overlay de variables de entorno (prefijo
// WAPP_).
//
// Se apoya en github.com/EduGoGroup/wapp-shared/config para la lectura del YAML
// y el acceso tipado a variables de entorno. En el corte T0 solo cubre los
// parámetros del servidor HTTP de health y el logging; PostgreSQL y gRPC entran
// en T1/T2.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	sharedconfig "github.com/EduGoGroup/wapp-shared/config"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// EnvPrefix es el prefijo aplicado a las variables de entorno de la Plataforma
// Cloud. Por ejemplo, la clave HTTP_ADDR se lee de la variable WAPP_HTTP_ADDR.
const EnvPrefix = "WAPP_"

// fileEnvKey es la clave (sin prefijo) que indica la ruta del archivo YAML de
// configuración. Por ejemplo, WAPP_CONFIG_FILE=/etc/wapp/platform.yaml.
const fileEnvKey = "CONFIG_FILE"

// AppConfig agrupa los parámetros mínimos de arranque de la Plataforma Cloud.
type AppConfig struct {
	// Env es la señal de entorno de ejecución: "prod" o "dev" (default "dev",
	// seguro para desarrollo). En "prod" el KeyProvider EXIGE la indexKey
	// explícita (WAPP_KEK_INDEX_B64) y falla-rápido si falta (Plan 012 §10.C).
	// Se lee de WAPP_APP_ENV.
	Env string `yaml:"env"`
	// HTTPAddr es la dirección de escucha del servidor HTTP de health/admin
	// (interno). Se lee de WAPP_HTTP_ADDR (default :8100).
	HTTPAddr string `yaml:"http_addr"`
	// PublicHTTPAddr es la dirección del SEGUNDO servidor HTTP: la API pública
	// /api/v1 para terceros (Plan 018, Decisión D/INV-7). Mismo binario, un solo
	// proceso; separado del admin para no exponer la red de administración. Se lee
	// de WAPP_PUBLIC_HTTP_ADDR (default :8103, banda 81xx).
	PublicHTTPAddr string `yaml:"public_http_addr"`
	// GRPCEnrollAddr es la dirección del servidor gRPC de Enrolamiento (TLS de
	// servidor SOLAMENTE: el Edge enrola aquí SIN cert de cliente).
	GRPCEnrollAddr string `yaml:"grpc_enroll_addr"`
	// GRPCConnectAddr es la dirección del servidor gRPC CloudLink (mTLS estricto:
	// el Edge conecta aquí con el cert emitido en el enrolamiento).
	GRPCConnectAddr string `yaml:"grpc_connect_addr"`
	// GRPCPushTimeout acota cada Send hacia un Edge por el stream CloudLink (Plan
	// 027 · Ola 1 · T5, cierra H6): un Edge lento que no lee su stream no debe
	// retener al llamante ni atascar el kill-switch (RevokeLease). Default 10s. <=0
	// cae al default. Se lee como cadena time.Duration de WAPP_GRPC_PUSH_TIMEOUT.
	GRPCPushTimeout time.Duration `yaml:"grpc_push_timeout"`
	// GRPCAckTimeout acota la ESPERA DEL ACK del Edge tras empujar un comando
	// (SendText/SendMedia). Es el hermano de GRPCPushTimeout —que acota el empuje,
	// no la respuesta— y cierra el hueco que dejó abierto el Plan 027 · Ola 1 · T5
	// (H6: "Push/Send sin deadline bloquea al llamante"): sin este reloj, el select
	// del Ack espera contra un ctx SIN deadline y un Edge saturado cuelga al
	// llamante HTTP indefinidamente (incidente del 2026-08-06: 88s sin respuesta ni
	// log). Default 8s. <=0 cae al default. Env WAPP_GRPC_ACK_TIMEOUT.
	//
	// ⚠️ INVARIANTE: tiene que ser MENOR que el WriteTimeout del servidor HTTP
	// (10s, internal/bootstrap/http.go). En Go el WriteTimeout NO interrumpe al
	// handler ni cancela su contexto: solo hace fallar el Write posterior. Si este
	// timeout lo igualara o superara, el 504 se generaría con el deadline de
	// escritura ya vencido y el cliente seguiría viendo una conexión cerrada sin
	// respuesta — exactamente el síntoma que este reloj viene a eliminar.
	//
	// 🔴 CORREGIDO (Plan 050 · Ola 5 · T5.4). El párrafo de arriba decía además que
	// «8s deja ~2s de margen para serializar y escribir la respuesta». Eso solo era
	// cierto si este reloj fuera el ÚNICO del envío, y no lo es: por delante corren
	// la guarda de tenant (PublicAPIDBTimeout, 1,5s) y el empuje (GRPCPushTimeout,
	// 10s), los tres SECUENCIALES. El margen que este valor deja por sí solo no
	// existe. Ver la cuenta entera —y quién la hace cumplir ahora— en el comentario
	// de PublicAPIDBTimeout, más abajo en este mismo struct. No repetir aquí la
	// aritmética: tenerla escrita dos veces fue justo lo que la desincronizó.
	GRPCAckTimeout time.Duration `yaml:"grpc_ack_timeout"`
	// GatewayWorkQueue es el tope de trabajos encolados POR SESIÓN en el carril de
	// trabajo del Gateway CloudLink (Plan 050 · Ola 1, ADR-0040). Cuando la cola de
	// una sesión se llena, el bucle Recv del stream FRENA (contrapresión) en vez de
	// seguir aceptando trabajo sin límite. Default 64, igualado al techo de
	// entrantes concurrentes del runtime de flujos (Flow.MaxConcurrentIncoming) para
	// que ninguna de las dos colas sea el cuello por accidente. <=0 cae al default.
	// Env WAPP_GATEWAY_WORK_QUEUE.
	//
	// ⚠️ Subirlo cuesta MEMORIA POR STREAM: cada sesión viva puede retener hasta
	// este número de trabajos pendientes.
	GatewayWorkQueue int `yaml:"gateway_work_queue"`
	// GatewayWorkTimeout es el presupuesto de tiempo de PARED de cada trabajo del
	// carril (Plan 050 · Ola 1, ADR-0040): pasado ese plazo el trabajo se rinde y
	// libera el carril de su sesión. Default 5s, el mismo valor ya calibrado del
	// offlinePersistTimeout del gateway (internal/gateway/grpc/server.go). <=0 cae al
	// default. Env WAPP_GATEWAY_WORK_TIMEOUT.
	//
	// ⚠️ Subirlo cuesta TIEMPO COLGADO POR TRABAJO: el carril es serie por sesión,
	// así que todo lo que venga detrás de un trabajo lento espera ese plazo entero.
	GatewayWorkTimeout time.Duration `yaml:"gateway_work_timeout"`
	// PublicAPIDBTimeout acota cada CONSULTA A BD que hace un handler de la API
	// pública ANTES de tocar al Edge (Plan 050 · Ola 3): hoy la guarda de tenant del
	// envío interroga a Postgres contra un contexto sin plazo propio, y una base
	// lenta cuelga al llamante antes incluso de que arranque el reloj del Ack.
	// Default 1,5s. <=0 cae al default. Env WAPP_PUBLICAPI_DB_TIMEOUT.
	//
	// ⚠️ INVARIANTE: los relojes del envío son SECUENCIALES, no alternativos, así que
	// contra el WriteTimeout del servidor HTTP (10s, internal/bootstrap/http.go:22)
	// cuenta la SUMA.
	//
	// 🔴 CORREGIDO (Plan 050 · Ola 5 · T5.4). Este párrafo decía «los DOS relojes» y
	// hacía la cuenta 1,5+8 = 9,5s, concluyendo que quedaba margen. Eran TRES y la
	// cuenta era falsa: se saltaba GRPCPushTimeout (10s, declarado en este mismo
	// struct), que corre ENTRE los otros dos —internal/gateway/grpc/send.go:334 llama
	// a Push y :338 a awaitAck, uno después del otro—. El peor caso real es
	//
	//	1,5 (guarda) + 10 (push) + 8 (ack) = 19,5s   contra un WriteTimeout de 10s
	//
	// es decir margen NEGATIVO por 9,5s, no positivo por 0,5s. Con el Edge saturado
	// el POST se pasaba del deadline de escritura y el cliente veía la conexión
	// cerrada sin cuerpo (el incidente del 2026-08-06, curl 52), reproducido en
	// internal/publicapi/gateway_saturado_e2e_integration_test.go.
	//
	// 🔴 CÓMO SE SOSTIENE AHORA, y por qué esta cuenta ya no puede volver a mentir:
	// NO se corrigió moviendo ninguno de los cuatro valores (INV-050.6 lo prohíbe, y
	// los cuatro siguen exactamente donde estaban). Se AÑADIÓ un techo por encima de
	// todos ellos —el presupuesto de la petición de envío, publicapi.SendBudgetFrom—
	// que se DERIVA del WriteTimeout en vez de ser un quinto número suelto que
	// alguien tenga que mantener a mano. Mover el WriteTimeout arrastra el
	// presupuesto; la suma de abajo puede crecer sin que el cliente se quede sin
	// respuesta, porque quien manda es el techo, no la suma.
	PublicAPIDBTimeout time.Duration `yaml:"publicapi_db_timeout"`
	// LogLevel es el nivel mínimo de logging: debug, info, warn o error.
	LogLevel string `yaml:"log_level"`
	// LogJSON selecciona el formato JSON del logger cuando es true.
	LogJSON bool `yaml:"log_json"`
	// PlatformTenantID es el tenant OPERADOR de wApp: el único cuyos tokens
	// pueden cortar (o reactivar) a un tenant ajeno vía /admin/tenants/revoke y
	// /admin/tenants/restore (ADR-0039, Plan 055 · REQ-055.7). Los handlers lo
	// comparan contra la Identity del llamante; cualquier otro tenant recibe 403
	// aunque su token traiga el permiso.
	//
	// El default coincide con el id fijo que siembra la migración 0059
	// (0059_platform_admin.sql): plataforma y esquema tienen que hablar del
	// MISMO tenant, y un id generado obligaría a reconfigurar tras cada
	// bootstrap. Se sobreescribe solo en despliegues que sembraran otro.
	// Vacío ⇒ NADIE puede revocar a un tercero (fail-closed deliberado: una
	// config a medias no debe abrir el plano de plataforma). Se lee de
	// WAPP_PLATFORM_TENANT_ID.
	PlatformTenantID string `yaml:"platform_tenant_id"`
	// DB es la configuración de conexión a PostgreSQL.
	DB DatabaseConfig `yaml:"db"`
	// PKI son las rutas a la CA y el cert de servidor (los genera
	// scripts/gen-dev-certs.sh).
	PKI PKIConfig `yaml:"pki"`
	// Lease es la configuración del kill-switch (clave de firma y TTL). La
	// consume el Gateway al construir el lease.Manager (cableado en T5).
	Lease LeaseConfig `yaml:"lease"`
	// Crypto es la configuración del fundamento criptográfico de PII (Plan 011,
	// ADR-0017): la KEK maestra y la indexKey del índice ciego. Se expone aquí;
	// el cableado a los repos/main entra en tramos posteriores (T1/T3).
	Crypto CryptoConfig `yaml:"crypto"`
	// Storage es la configuración del almacén de objetos Cloudflare R2 (Plan 017):
	// credenciales, endpoint y vigencia de las URLs prefirmadas. La consume el
	// main al construir el objectstore.PresignClient (cableado en tramos
	// posteriores). Se lee con prefijo WAPP_STORAGE_S3_.
	Storage StorageConfig `yaml:"storage"`
	// JWT es la configuración de firma/validación del Context Token de wApp
	// (ADR-0019): clave EC P-256, su `kid` y el issuer esperado. La clave NUNCA
	// se hardcodea ni se loguea (zero-knowledge); en dev, si falta, se genera un
	// par efímero con warning (como la clave del lease). Se lee con prefijo WAPP_
	// (WAPP_JWT_EC_PRIVATE_KEY_FILE, WAPP_JWT_KID, WAPP_JWT_ISSUER).
	JWT JWTConfig `yaml:"jwt"`
	// RateLimit es la configuración del rate-limit de la API pública (Plan 018 ·
	// T10, R11): límite por credencial. Se lee con prefijo WAPP_RATELIMIT_.
	RateLimit RateLimitConfig `yaml:"rate_limit"`
	// Flow es la configuración del runtime del Motor de Flujos (Plan 020 · T0): el
	// tope de auto-respuestas por conversación (red anti-loop). Se lee con prefijo
	// WAPP_FLOW_.
	Flow FlowConfig `yaml:"flow"`
	// LLM son los DOS interruptores de la orquestación de inferencia del Plan 044 ·
	// Ola 1.7. Se lee con prefijo WAPP_LLM_. Existen para poder hacer el control A/B
	// de campo EN LA MISMA TANDA —encendido y apagado sin recompilar ni desplegar dos
	// binarios—, que es la única forma de probar que lo que cambia es el mecanismo y
	// no el humor de la máquina. Ver LLMConfig.
	LLM LLMConfig `yaml:"llm"`
	// Health gobierna la derivación de estados de salud de flota (Plan 031 · T4,
	// ADR-0023): los umbrales N (degradado sostenido) y M (sin salud ⇒ stale) que
	// GET /api/v1/sessions aplica al servir. Se lee con prefijo WAPP_HEALTH_.
	Health HealthConfig `yaml:"health"`
	// Diagnostics gobierna el diagnóstico remoto (Plan 031 · T5, ADR-0023): la
	// retención del bundle. Se lee con prefijo WAPP_DIAGNOSTICS_.
	Diagnostics DiagnosticsConfig `yaml:"diagnostics"`
	// TenantContent acota el peso de un blob de tenant_content, para TODOS los que
	// escriben en esa tabla (Plan 041 · Ola 3). Se lee con prefijo
	// WAPP_TENANT_CONTENT_.
	TenantContent TenantContentConfig `yaml:"tenant_content"`
	// Import son los topes propios del import de catálogo (Plan 041 · Ola 3,
	// D-041.5). Se lee con prefijo WAPP_IMPORT_.
	Import ImportConfig `yaml:"import"`
	// Identity es la puerta al SSO del grupo (identity-core, Plan 003 · Ola 1):
	// de dónde se toman las claves públicas con las que verificar sus Identity
	// Tokens. Vacía = wApp arranca sin identity, como hasta ahora. Se lee con
	// prefijo WAPP_IDENTITY_.
	Identity IdentityConfig `yaml:"identity"`
	// Webhook gobierna el worker del puente CRM (Plan 042 · Ola 3, D-042.4):
	// cadencia del poll, tope de reintentos y timeout de cada entrega. Se lee
	// con prefijo WAPP_WEBHOOK_.
	Webhook WebhookConfig `yaml:"webhook"`
	// ⚰️ Aquí vivió, del 2026-08-10 al 2026-08-22, el bloque del HILO CONVERSACIONAL:
	// un solo booleano —con su variable de entorno— que apagaba el productor
	// `message` del hilo del evento POR ENCIMA del gate de la feature `llm_intake`.
	// Lo puso el Plan 043 · Ola 4.5 porque el productor escribía sin que existiera
	// todavía nadie que lo leyera, y su propio comentario le puso fecha de caducidad:
	// «hasta que el Plan 044 (su LECTOR) exista; se quita entonces». El Plan 044 ·
	// T1.6 es esa fecha, y retiró el tipo, el campo, el default y la lectura. El gate
	// que queda es UNO y es por tenant: la feature `llm_intake`
	// (internal/flujos/runtime/thread.go). Los nombres exactos, en git.
	//
	// ⚠️ ESTA LÁPIDA CIERRA EL STRUCT: no hay declaración detrás, así que hoy no puede
	// fusionarse con el doc-comment de nadie. QUIEN AÑADA UN CAMPO AQUÍ DEBAJO tiene que
	// dejar una línea EN BLANCO de verdad entre las dos (no una línea `//` vacía, que
	// para gofmt y go/doc es el mismo comentario) — es el defecto que en
	// runtime_engine.go rompía el `revive`/`exported` de WithClock.
}

// WebhookConfig son los parámetros del worker de entregas del puente CRM
// (Plan 042, D-042.4). Los tres caen a su default con <= 0 (nunca un poll a 0
// ni un tope de intentos nulo por accidente, mismo criterio que FlowConfig).
type WebhookConfig struct {
	// PollInterval es la cadencia del loop de reclamo (FOR UPDATE SKIP LOCKED).
	// Default 5s. Se lee de WAPP_WEBHOOK_POLL_INTERVAL.
	PollInterval time.Duration `yaml:"poll_interval"`
	// MaxAttempts es el tope de intentos antes de pasar a dead (~8-10h con el
	// backoff de D-042.4). Default 10. Se lee de WAPP_WEBHOOK_MAX_ATTEMPTS.
	MaxAttempts int `yaml:"max_attempts"`
	// Timeout es el timeout HTTP de CADA entrega individual. Default 10s. Se
	// lee como cadena time.Duration de WAPP_WEBHOOK_TIMEOUT.
	Timeout time.Duration `yaml:"timeout"`
}

// IdentityConfig apunta al microecosistema identity, emisor de los Identity
// Tokens del grupo (identity ADR-0001/ADR-0002). Es distinto del bloque JWT, que
// gobierna los tokens que wApp emite por su cuenta: aquí wApp solo VERIFICA lo
// que firma otro.
type IdentityConfig struct {
	// JWKSURL es el endpoint JWKS de identity-api del que salen las claves
	// públicas ES256 del emisor. SIN default y con semántica de INTERRUPTOR: vacía
	// ⇒ el verificador de Identity Tokens no se construye y el arranque no depende
	// de identity. Con valor, el arranque FALLA si el JWKS no responde
	// (fail-closed: el verificador nunca nace con cero claves). En dev
	// http://localhost:8200/.well-known/jwks.json; fuera de loopback se exige
	// https. Es lo que habilita POST /api/v1/auth/exchange.
	JWKSURL string `yaml:"jwks_url"`
	// URL es la base de identity-api con la que el gateway CloudLink DELEGA el
	// login/refresh/logout del operador (identity Plan 003 · Ola 3, T3.3). SIN
	// default y con semántica de INTERRUPTOR, igual que JWKSURL: vacía ⇒ el relé
	// sigue resolviendo las credenciales con el IAM local, como hasta ahora. Es
	// el segundo eje de la transición: con las dos variables puestas, wApp
	// delega de verdad; con ninguna, el flujo legacy queda intacto. En dev
	// http://localhost:8200. Se lee de WAPP_IDENTITY_URL.
	URL string `yaml:"url"`
	// Timeout acota cada llamada a identity-api. Default 10s; <=0 cae al default.
	// Se lee como cadena time.Duration de WAPP_IDENTITY_TIMEOUT.
	Timeout time.Duration `yaml:"timeout"`
	// APIKey es la credencial M2M de wApp en identity (fila de `iam.api_keys`
	// con `ecosystem_key = 'wapp'`), la que el cliente M2M CANJEA por un Service
	// Token contra POST /api/v1/auth/token — nunca se presenta como portador
	// (identity ADR-0025). Es lo que habilita el alta de usuario: `users/ensure`
	// y `PUT /users/{id}/systems` (Plan 056 · T2.4).
	//
	// SIN default y con semántica de INTERRUPTOR, igual que URL y JWKSURL: vacía
	// ⇒ el cliente M2M no se construye y el alta por consola no existe; con
	// valor, el constructor exige además URL (la MISMA base de identity, no hay
	// una segunda) y falla al arrancar si falta. Se lee de WAPP_IDENTITY_API_KEY.
	//
	// 🔴 Es un SECRETO: vive en el `.env` del VPS, no se versiona y no se
	// registra en ningún log. INV-056.4: aquí NO entra ninguna conexión directa
	// a la base de identity — wApp habla con identity por HTTP y solo por HTTP.
	//
	// El tag es `-` A PROPÓSITO: este campo no se serializa nunca. Un bundle de
	// diagnóstico, un volcado de configuración o cualquier otro `yaml.Marshal` de
	// la config publicaría la credencial de máquina del ecosistema entera.
	APIKey string `yaml:"-"`
}

// DiagnosticsConfig gobierna el diagnóstico remoto bajo demanda (Plan 031 · T5,
// ADR-0023 capa 3). Default sano; <=0 cae al default (nunca desactiva la retención
// por accidente).
type DiagnosticsConfig struct {
	// BundleTTL es la retención del bundle: expires_at = requested_at + BundleTTL. Cubre
	// tanto la espera del bundle (el Edge puede tardar/estar offline) como su vida útil
	// tras recibirlo. Default 30m. Se lee como cadena time.Duration de
	// WAPP_DIAGNOSTICS_BUNDLE_TTL.
	BundleTTL time.Duration `yaml:"bundle_ttl"`
}

// TenantContentConfig acota lo que puede pesar un blob de public.tenant_content
// (Plan 041 · Ola 3; patrón EduGo D-038: límites por env con default).
//
// El techo cuelga del OBJETO y no del camino a propósito: escriben en esa tabla
// tanto el import de catálogo como el PUT /api/v1/tenant-content genérico, así que
// un techo por camino permitiría subir el del import y que el PUT rechazara
// después el mismo blob que el import acaba de aceptar — dos verdades sobre el
// mismo dato. Por eso no vive en ImportConfig.
//
// El default es el mismo que declara internal/catalogimport, que es donde se
// aplica; un test amarra los dos para que no puedan divergir.
type TenantContentConfig struct {
	// MaxBytes es el tamaño máximo del blob crudo, en bytes. Default 1 MiB (el que
	// PUT /api/v1/tenant-content ya tenía fijo en el código). Se aplica LEYENDO,
	// antes de deserializar, que es lo que impide que un documento absurdo se
	// materialice en memoria. Se lee de WAPP_TENANT_CONTENT_MAX_BYTES; <=0 cae al
	// default (nunca desactiva el tope por accidente).
	MaxBytes int64 `yaml:"max_bytes"`
}

// ImportConfig son los topes propios del import de catálogo (Plan 041 · Ola 3,
// D-041.5). El techo de bytes NO está aquí: gobierna la tabla, no el import (ver
// TenantContentConfig).
type ImportConfig struct {
	// MaxItems es el número máximo de artículos por importación, sumando todas las
	// categorías. Default 500. <=0 cae al default. Se lee de WAPP_IMPORT_MAX_ITEMS.
	MaxItems int `yaml:"max_items"`
}

// HealthConfig gobierna la derivación de estados de salud de flota que expone
// GET /api/v1/sessions (Plan 031 · T4, ADR-0023). Global por ahora; una capa
// por-tenant es la extensión natural (los valores concretos se afinan con el e2e
// real, ADR-0023 §Puntos abiertos). Defaults sanos; <=0 cae al default (nunca
// desactiva la derivación por accidente).
type HealthConfig struct {
	// DegradedAfter es el umbral N: una sesión con degraded_since sostenido por más
	// de este tiempo se sirve como derived "degraded". Default 5m. Se lee como cadena
	// time.Duration de WAPP_HEALTH_DEGRADED_AFTER.
	DegradedAfter time.Duration `yaml:"degraded_after"`
	// StaleAfter es el umbral M: si el último snapshot de salud (last_health_at)
	// envejeció más de este tiempo, el dato deja de ser confiable y la sesión se
	// sirve como derived "stale". Default 2m. Se lee como cadena time.Duration de
	// WAPP_HEALTH_STALE_AFTER.
	StaleAfter time.Duration `yaml:"stale_after"`
}

// LLMConfig son los interruptores de lo que el Cloud AÑADE a una inferencia. Los dos
// nacen ENCENDIDOS: apagarlos devuelve la conducta anterior a la Ola 1.7, que es peor
// pero conocida, y esa asimetría es deliberada — un interruptor que hay que acordarse
// de encender acaba apagado en producción.
//
// 🔴 SON DE CONFIGURACIÓN Y NO CONSTANTES POR UN MOTIVO DE MÉTODO, no de gusto. Los
// criterios de campo de T1.7-3 y T1.7-4 piden un control A/B EN LA MISMA TANDA: la
// primera inferencia con calentamiento y sin él, la misma P3 con techo de salida y sin
// él. Con constantes harían falta dos binarios y el A/B pierde el control de
// condiciones — deja de probar el mecanismo y pasa a comparar dos despliegues.
//
// ⚠️ Y por eso el arranque LOS IMPRIME (ver la línea «orquestación LLM» de bootstrap):
// el valor que gobierna es el EFECTIVO, no el que está en el fuente. Es el gotcha caro
// de esta casa —un default recalibrado en el binario que el `.env` del VPS pisaba, sin
// que ningún test pudiera verlo—: si el cambio no se puede confirmar leyendo el log del
// arranque, no está terminado.
type LLMConfig struct {
	// WarmupEnabled gobierna el PRECALENTADO de la caché de prefijo del Edge (T1.7-4):
	// el `inference_request` con `warmup=true` que el Cloud emite al conectar un Edge y
	// al publicar catálogo de intents. Default true. Env WAPP_LLM_WARMUP_ENABLED.
	//
	// Apagado, no se emite ninguno y la primera inferencia real de cada prefijo nuevo
	// vuelve a pagar el prefill FRÍO (~50 s medidos en UAT). Es exactamente el lado B
	// del criterio (a) de T1.7-4.
	WarmupEnabled bool `yaml:"warmup_enabled"`
	// MaxOutputTokensEnabled gobierna si el Cloud FIJA el presupuesto de salida por
	// tarea en el frame (campo 7, T1.7-3). Default true.
	// Env WAPP_LLM_MAX_OUTPUT_TOKENS_ENABLED.
	//
	// Apagado, el campo viaja AUSENTE y el Edge aplica su default (hoy 256), con lo que
	// una P2/P3 de 265–293 tokens vuelve a truncarse. Es el lado B del criterio (c) de
	// T1.7-3, y la razón de que el interruptor sea sobre el ENVÍO y no sobre el número:
	// lo que hay que reproducir es la conducta ANTERIOR, y esa era «el Cloud no dice
	// nada», no «el Cloud pide 256».
	MaxOutputTokensEnabled bool `yaml:"max_output_tokens_enabled"`
}

// FlowConfig gobierna el token-bucket EN MEMORIA de auto-respuestas por
// conversación del Motor de Flujos (Plan 020 · T0, red anti-loop). Defaults
// holgados: matan un bucle (~1 msg/2.6s del e2e 019) sin frenar un flujo legítimo.
type FlowConfig struct {
	// ReplyRate es el ritmo sostenido de auto-respuestas por conversación
	// (respuestas/seg). Default 0.5 (1 cada 2s). <=0 cae al default (nunca desactiva
	// el tope por accidente). Se lee de WAPP_FLOW_REPLY_RATE.
	ReplyRate float64 `yaml:"reply_rate"`
	// ReplyBurst es la ráfaga admitida por conversación. Default 3. <=0 cae al
	// default. Se lee de WAPP_FLOW_REPLY_BURST.
	ReplyBurst int `yaml:"reply_burst"`
	// MaxConcurrentIncoming acota cuántos entrantes reactivos procesa el runtime a la
	// vez (Plan 027 · Ola 1 · T5, cierra H5): un semáforo evita que una inundación de
	// historial arranque cientos de HandleIncoming en paralelo. Default 64. 0 cae al
	// default; <0 desactiva el techo. Se lee de WAPP_FLOW_MAX_CONCURRENT_INCOMING.
	MaxConcurrentIncoming int `yaml:"max_concurrent_incoming"`
	// IncomingTimeout acota el procesamiento de CADA entrante reactivo (Plan 027 ·
	// Ola 0 · T1, cierra H1): sin deadline, la goroutine de OnIncoming se fuga
	// esperando un Ack que nunca llega y retiene el keyedMutex de la conversación.
	// Default 30s. <=0 cae al default. Se lee como cadena time.Duration de
	// WAPP_FLOW_INCOMING_TIMEOUT.
	IncomingTimeout time.Duration `yaml:"incoming_timeout"`
}

// RateLimitConfig gobierna el token-bucket EN MEMORIA de la API pública (Plan
// 018 · T10). PublicRPS/PublicBurst acotan cada credencial en las rutas de
// operación. Defaults sanos; cero o negativo cae al default (nunca desactiva el
// límite por accidente).
//
// El freno por IP del login (LoginPerMin/LoginBurst) se retiró en la Ola 5 del
// Plan 003 de identity, junto con el propio login: la fuerza bruta de
// contraseñas se frena donde se validan, y eso es identity-core.
type RateLimitConfig struct {
	// PublicRPS es el ritmo sostenido (peticiones/seg) por credencial en la API
	// pública. Default 20. Se lee de WAPP_RATELIMIT_PUBLIC_RPS.
	PublicRPS int `yaml:"public_rps"`
	// PublicBurst es la ráfaga admitida por credencial. Default 40. Se lee de
	// WAPP_RATELIMIT_PUBLIC_BURST.
	PublicBurst int `yaml:"public_burst"`
	// TrustProxy habilita leer X-Forwarded-For/X-Real-IP para el rate-limit por
	// IP de rutas públicas SIN credencial (hoy solo POST /api/v1/signup,
	// A-06a). Default false: sin un proxy de confianza delante, esas cabeceras
	// las pone el cliente y serían un bypass trivial del freno (cada petición
	// estrena cubo con una IP falsa). Se lee de WAPP_RATELIMIT_TRUST_PROXY.
	TrustProxy bool `yaml:"trust_proxy"`
}

// JWTConfig agrupa el material de firma del Context Token de wApp (ADR-0019).
// Issuer es el emisor esperado en la validación; la clave EC P-256 y su `kid`
// son lo que firma y lo que ata cada token a su entrada de verificación.
//
// El secreto HS256 (Secret) y la audiencia del service token (ServiceAudience)
// desaparecieron con el plano M2M en la Ola 5: wApp ya no firma nada simétrico.
type JWTConfig struct {
	// Issuer es el emisor (`iss`) que se firma y se valida. Default "wapp-cloud".
	Issuer string `yaml:"issuer"`
	// ECPrivateKeyFile es la ruta al PEM de la clave privada EC P-256 que firma
	// los tokens de usuario en ES256 (ADR-0019, Plan 028). Acepta PKCS#8 y SEC1.
	// SIN default: en prod es obligatorio (fail-fast si falta, si los permisos son
	// más laxos que 0600 o si la clave es inválida); en dev, vacío ⇒ par efímero
	// generado en memoria con warning. Genera uno con:
	//   openssl ecparam -name prime256v1 -genkey -noout | openssl pkcs8 -topk8 -nocrypt -out jwt-es256.pem && chmod 600 jwt-es256.pem
	ECPrivateKeyFile string `yaml:"ec_private_key_file"`
	// Kid es el key id (`kid`) que el emisor ES256 estampa en cada token y que el
	// MultiVerifier usa para seleccionar la pública en la coexistencia de
	// algoritmos. Convención es256-YYYYMMDD. Vacío en dev ⇒ default "es256-dev".
	Kid string `yaml:"kid"`
}

// StorageConfig agrupa los parámetros del almacén de objetos Cloudflare R2
// (S3-compatible; NO AWS) del Plan 017. R2 se apunta por Endpoint (BaseEndpoint
// del SDK) y comparte cuenta/bucket con EduGo en alpha (bucket edugo-materials,
// prefijo wapp/ en las keys). Las credenciales (AccessKeyID/SecretAccessKey) y
// el Endpoint NO tienen default: van en el .env NO versionado. Se leen con
// prefijo WAPP_STORAGE_S3_ (p. ej. WAPP_STORAGE_S3_BUCKET).
type StorageConfig struct {
	// Region es la región del SDK. R2 la ignora, pero aws-sdk-go-v2 la exige.
	Region string `yaml:"region"`
	// Bucket es el bucket de R2 (alpha: edugo-materials, compartido con EduGo).
	Bucket string `yaml:"bucket"`
	// AccessKeyID es la Access Key ID del token R2 (sin default; va en .env).
	AccessKeyID string `yaml:"access_key_id"`
	// SecretAccessKey es la Secret Access Key del token R2 (sin default; .env).
	SecretAccessKey string `yaml:"secret_access_key"`
	// Endpoint es el endpoint S3 de R2 (https://<accountid>.r2.cloudflarestorage.com;
	// sin default, va en .env).
	Endpoint string `yaml:"endpoint"`
	// PresignExpiry es la vigencia de las URLs prefirmadas (default 15m). Se lee
	// como cadena time.Duration de WAPP_STORAGE_S3_PRESIGN_EXPIRY.
	PresignExpiry time.Duration `yaml:"presign_expiry"`
}

// CryptoConfig agrupa el material de clave del cifrado de PII en reposo (Plan
// 011). La KEK maestra (KEKMasterB64) envuelve las DEKs por-valor; la indexKey
// (KEKIndexB64) alimenta el índice ciego HMAC. Ambas van en base64 estándar y
// SEPARADAS del dato de negocio (no viven en la BD; §10.A). Se leen con prefijo
// WAPP_ (p. ej. WAPP_KEK_MASTER_B64), en paralelo a la clave del lease.
type CryptoConfig struct {
	// KEKProvider elige DE DÓNDE sale la KEK (Plan 042 · T9.1, ADR-0036):
	// "env" (default) lee las KEK en claro de las variables de abajo y es el
	// camino de DEV LOCAL; "kms" las toma cifradas por el KMS de GCP y las
	// desenvuelve una sola vez al arrancar. Un valor desconocido NO cae al
	// default: falla el arranque (crypto.ErrUnknownKeyProvider). Se lee de
	// WAPP_KEK_PROVIDER.
	//
	// 💰 El default sigue siendo "env" a propósito: el ADR-0036 §3 construye el
	// KMS pero NO lo activa en alfa (cero gasto). Activarlo es el gate T9.2.
	KEKProvider string `yaml:"kek_provider"`
	// KEKKMSKey es el nombre de recurso de la CryptoKey del KMS que desenvuelve el
	// keyring: projects/<p>/locations/<l>/keyRings/<r>/cryptoKeys/<k>. Solo se usa
	// con KEKProvider="kms" (y entonces es obligatorio). Se lee de WAPP_KEK_KMS_KEY.
	KEKKMSKey string `yaml:"kek_kms_key"`
	// KEKKMSKeyring es el keyring versionado CIFRADO por el KMS: entradas
	// "id:base64(ciphertext)" separadas por coma. Los id son los MISMOS key_id ya
	// persistidos en las columnas *_kek_id, así que migrar al KMS no toca la base.
	// Solo se usa con KEKProvider="kms". Se lee de WAPP_KEK_KMS_KEYRING.
	KEKKMSKeyring string `yaml:"kek_kms_keyring"`
	// KEKKMSIndexB64 es la indexKey del índice ciego CIFRADA por la misma llave del
	// KMS (base64 del ciphertext). OPCIONAL: si viene, gana sobre KEKIndexB64 y
	// evita dejar ese secreto en claro en el entorno productivo. Se lee de
	// WAPP_KEK_KMS_INDEX_B64.
	KEKKMSIndexB64 string `yaml:"kek_kms_index_b64"`
	// KEKKeyring es el keyring versionado del Plan 012: entradas "id:base64"
	// (cada KEK 32B AES-256) separadas por coma. Con él, WrapDEK usa la KEK
	// KEKCurrent y UnwrapDEK selecciona por key_id, habilitando la rotación sin
	// re-cifrar. Vacío = camino compat con KEKMasterB64 (key_id "1"). Se lee de
	// WAPP_KEK_KEYRING.
	KEKKeyring string `yaml:"kek_keyring"`
	// KEKCurrent es el key_id de la KEK current dentro de KEKKeyring (la que
	// envuelve las DEK nuevas). Obligatorio cuando KEKKeyring viene y debe existir
	// en él. Se lee de WAPP_KEK_CURRENT.
	KEKCurrent string `yaml:"kek_current"`
	// KEKMasterB64 es la KEK maestra única del Plan 011 (32B, AES-256) en base64.
	// Camino de compatibilidad: si no hay keyring, se carga como el key_id inicial
	// "1" y es la current. Su ausencia (junto con KEKKeyring) hace fallar-rápido al
	// construir el KeyProvider. Se lee de WAPP_KEK_MASTER_B64.
	KEKMasterB64 string `yaml:"kek_master_b64"`
	// KEKIndexB64 es la indexKey del índice ciego (32B) en base64, INDEPENDIENTE de
	// la KEK (Plan 012 §10.C). OBLIGATORIA en prod (fail-fast si falta) y estable de
	// por vida (cambiarla = reindexar value_bidx, que es PK). En dev, si queda vacía
	// se deriva de la KEK por HKDF-SHA256 con warning. Se lee de WAPP_KEK_INDEX_B64.
	KEKIndexB64 string `yaml:"kek_index_b64"`
	// CloudEncPrivKeyB64 es la clave privada X25519 (32B) del par de cifrado de
	// tránsito de la nube (Plan 011 §10.F), en base64 estándar. Con ella la nube
	// abre (OpenWith) el enc_payload sellado por el Edge; su pública se publica al
	// Edge en el enrolamiento. Es DISTINTA de la Ed25519 del lease y de la DEK.
	// Vacía = se genera un par efímero de dev (no apta para producción), como la
	// clave del lease. Se lee de WAPP_CLOUD_ENC_PRIVKEY_B64.
	CloudEncPrivKeyB64 string `yaml:"cloud_enc_privkey_b64"`
}

// PKIConfig agrupa las rutas de la PKI del Gateway. El cert de servidor es
// compartido por ambos listeners (enroll y connect); la CA firma los certs de
// Edge en el enrolamiento y valida a los Edges en el mTLS de CloudLink. La clave
// de la CA (CAKeyFile) la necesita el servicio de enrolamiento para firmar CSRs.
// Los defaults coinciden con scripts/gen-dev-certs.sh (directorio certs/). Se
// leen con prefijo WAPP_PKI_ (p. ej. WAPP_PKI_CA_KEY_FILE).
type PKIConfig struct {
	// ServerCertFile es el PEM del cert de servidor (SAN localhost en dev).
	ServerCertFile string `yaml:"server_cert_file"`
	// ServerKeyFile es el PEM de la clave privada del cert de servidor.
	ServerKeyFile string `yaml:"server_key_file"`
	// CACertFile es el PEM del cert de la CA (firma certs de Edge y los valida).
	CACertFile string `yaml:"ca_cert_file"`
	// CAKeyFile es el PEM de la clave privada de la CA (firma CSRs en el enroll).
	CAKeyFile string `yaml:"ca_key_file"`
}

// LeaseConfig agrupa la configuración del lease del Gateway (ADR-0007). La clave
// privada Ed25519 firma los leases; precedencia: archivo PEM > base64 >
// generación efímera de dev (si ambos quedan vacíos). Se lee con prefijo
// WAPP_LEASE_ (p. ej. WAPP_LEASE_PRIVATE_KEY_B64).
type LeaseConfig struct {
	// PrivateKeyFile es la ruta a un PEM PKCS#8 con la clave Ed25519. Tiene
	// prioridad sobre PrivateKeyB64. Vacío = no usar archivo.
	PrivateKeyFile string `yaml:"private_key_file"`
	// PrivateKeyB64 es la clave Ed25519 (semilla de 32B o clave de 64B) en base64.
	// Vacío = no usar base64. Si también PrivateKeyFile está vacío, se genera una
	// clave de dev efímera (NO apta para producción).
	PrivateKeyB64 string `yaml:"private_key_b64"`
	// TTLMinutes es la vigencia del lease en minutos. <=0 usa el default del
	// gestor (15 min). Se renueva en cada Heartbeat del Edge.
	TTLMinutes int `yaml:"ttl_minutes"`
}

// DatabaseConfig agrupa los parámetros de conexión a PostgreSQL. Se lee de las
// variables de entorno con prefijo WAPP_DB_ (p. ej. WAPP_DB_HOST) y/o del YAML.
// Los defaults coinciden con deploy/docker-compose.yml para arranque local.
type DatabaseConfig struct {
	// Host es el hostname del servidor PostgreSQL.
	Host string `yaml:"host"`
	// Port es el puerto TCP del servidor PostgreSQL.
	Port int `yaml:"port"`
	// User es el usuario de conexión.
	User string `yaml:"user"`
	// Password es la contraseña de conexión.
	Password string `yaml:"password"`
	// Name es el nombre de la base de datos.
	Name string `yaml:"name"`
	// SSLMode es el modo SSL de libpq (disable, require, verify-full, …).
	SSLMode string `yaml:"sslmode"`
	// MaxOpenConns es el máximo de conexiones abiertas simultáneas contra
	// PostgreSQL. Se lee de WAPP_DB_MAX_OPEN_CONNS.
	MaxOpenConns int `yaml:"max_open_conns"`
	// MaxIdleConns es el máximo de conexiones ociosas que el pool retiene.
	// Se lee de WAPP_DB_MAX_IDLE_CONNS.
	MaxIdleConns int `yaml:"max_idle_conns"`
	// ConnMaxLifetime es la vida máxima de una conexión antes de reciclarse.
	// Se lee de WAPP_DB_CONN_MAX_LIFETIME.
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	// ConnMaxIdleTime es el tiempo máximo que una conexión puede estar ociosa
	// antes de cerrarse. Se lee de WAPP_DB_CONN_MAX_IDLE_TIME.
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
}

// DSN construye la cadena de conexión en formato keyword/value de libpq, apta
// para pgx (sql.Open("pgx", dsn)).
func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}

// defaults devuelve la configuración con valores por defecto sensatos.
func defaults() AppConfig {
	return AppConfig{
		Env:             "dev",
		HTTPAddr:        ":8100",
		PublicHTTPAddr:  ":8103",
		GRPCEnrollAddr:  ":8102",
		GRPCConnectAddr: ":8101",
		GRPCPushTimeout: 10 * time.Second,
		GRPCAckTimeout:  8 * time.Second,
		// Carril de trabajo del Gateway (Plan 050 · ADR-0040): la cola es POR
		// SESIÓN y se iguala a propósito al techo de entrantes concurrentes
		// (Flow.MaxConcurrentIncoming, más abajo) para que ninguna de las dos sea
		// el cuello por accidente; el presupuesto reusa el valor ya calibrado del
		// offlinePersistTimeout del gateway.
		GatewayWorkQueue:   64,
		GatewayWorkTimeout: 5 * time.Second,
		// El plazo de las consultas a BD de la API pública (Plan 050 · Ola 3) se
		// calibra por la SUMA, no por sí mismo: 1,5s + los 8s del Ack son 9,5s y
		// caben en el WriteTimeout de 10s dejando margen para escribir la respuesta.
		PublicAPIDBTimeout: 1500 * time.Millisecond,
		LogLevel:           "info",
		LogJSON:            false,
		// Mismo id que siembra 0059_platform_admin.sql (ver el comentario del
		// campo): sin este default, un arranque sin configurar dejaría el plano
		// de plataforma cerrado a cal y canto.
		PlatformTenantID: "55550000-0000-0000-0000-000000000055",
		JWT: JWTConfig{
			Issuer: "wapp-cloud",
		},
		Crypto: CryptoConfig{
			// Default explícito: la KEK sigue en env (ADR-0036 §3, el KMS se
			// construye pero NO se activa en alfa — cero gasto). Lo cambia el
			// gate T9.2, no este archivo.
			KEKProvider: "env",
		},
		RateLimit: RateLimitConfig{
			PublicRPS:   20,
			PublicBurst: 40,
			// Explícito a propósito (igual que LogJSON): sin proxy de
			// confianza delante por defecto.
			TrustProxy: false,
		},
		Flow: FlowConfig{
			ReplyRate:             0.5,
			ReplyBurst:            3,
			IncomingTimeout:       30 * time.Second,
			MaxConcurrentIncoming: 64,
		},
		// Los dos interruptores de la Ola 1.7 nacen ENCENDIDOS: el estado nuevo es el
		// bueno y el apagado existe para el control A/B de campo, no al revés.
		LLM: LLMConfig{
			WarmupEnabled:          true,
			MaxOutputTokensEnabled: true,
		},
		Health: HealthConfig{
			DegradedAfter: 5 * time.Minute,
			StaleAfter:    2 * time.Minute,
		},
		Diagnostics: DiagnosticsConfig{
			BundleTTL: 30 * time.Minute,
		},
		TenantContent: TenantContentConfig{
			MaxBytes: 1 << 20, // 1 MiB
		},
		Import: ImportConfig{
			MaxItems: 500,
		},
		Webhook: WebhookConfig{
			PollInterval: 5 * time.Second,
			MaxAttempts:  10,
			Timeout:      10 * time.Second,
		},
		DB: DatabaseConfig{
			Host:     "localhost",
			Port:     5432,
			User:     "wapp",
			Password: "wapp",
			Name:     "wapp_cloud",
			SSLMode:  "disable",
			// Los defaults del pool se REFERENCIAN del paquete postgres, no se
			// copian (Plan 050 · Ola 4 · T4.2). Copiar los números crearía dos
			// fuentes de verdad para el mismo valor: en cuanto divergieran
			// ganaría esta —es la que se le pasa a postgres.Open— mientras las
			// constantes de connect.go seguirían pareciendo el default real.
			MaxOpenConns:    postgres.DefaultMaxOpenConns,
			MaxIdleConns:    postgres.DefaultMaxIdleConns,
			ConnMaxLifetime: postgres.DefaultConnMaxLifetime,
			ConnMaxIdleTime: postgres.DefaultConnMaxIdleTime,
		},
		PKI: PKIConfig{
			ServerCertFile: "certs/server.crt",
			ServerKeyFile:  "certs/server.key",
			CACertFile:     "certs/ca.crt",
			CAKeyFile:      "certs/ca.key",
		},
		Storage: StorageConfig{
			Region:        "us-east-1",
			Bucket:        "edugo-materials",
			PresignExpiry: 15 * time.Minute,
		},
	}
}

// Load construye la configuración de la Plataforma Cloud.
//
// Orden de precedencia (de menor a mayor): valores por defecto, archivo YAML
// indicado por WAPP_CONFIG_FILE (opcional; si no existe se ignora) y variables
// de entorno con prefijo WAPP_. Devuelve error solo si el YAML existe pero no
// puede leerse o parsearse.
func Load() (AppConfig, error) {
	cfg := defaults()

	loader := sharedconfig.New(
		sharedconfig.WithEnvPrefix(EnvPrefix),
		sharedconfig.WithFile(os.Getenv(EnvPrefix+fileEnvKey)),
	)

	if err := loader.Unmarshal(&cfg); err != nil {
		return AppConfig{}, err
	}

	// Overlay de entorno: usa el valor actual (default o YAML) como fallback.
	cfg.Env = loader.GetString("APP_ENV", cfg.Env)
	cfg.HTTPAddr = loader.GetString("HTTP_ADDR", cfg.HTTPAddr)
	cfg.PublicHTTPAddr = loader.GetString("PUBLIC_HTTP_ADDR", cfg.PublicHTTPAddr)
	cfg.GRPCEnrollAddr = loader.GetString("GRPC_ENROLL_ADDR", cfg.GRPCEnrollAddr)
	cfg.GRPCConnectAddr = loader.GetString("GRPC_CONNECT_ADDR", cfg.GRPCConnectAddr)
	cfg.GRPCPushTimeout = loader.GetDuration("GRPC_PUSH_TIMEOUT", cfg.GRPCPushTimeout)
	cfg.GRPCAckTimeout = loader.GetDuration("GRPC_ACK_TIMEOUT", cfg.GRPCAckTimeout)
	cfg.GatewayWorkQueue = loader.GetInt("GATEWAY_WORK_QUEUE", cfg.GatewayWorkQueue)
	cfg.GatewayWorkTimeout = loader.GetDuration("GATEWAY_WORK_TIMEOUT", cfg.GatewayWorkTimeout)
	cfg.PublicAPIDBTimeout = loader.GetDuration("PUBLICAPI_DB_TIMEOUT", cfg.PublicAPIDBTimeout)
	cfg.LogLevel = loader.GetString("LOG_LEVEL", cfg.LogLevel)
	cfg.LogJSON = loader.GetBool("LOG_JSON", cfg.LogJSON)
	cfg.PlatformTenantID = loader.GetString("PLATFORM_TENANT_ID", cfg.PlatformTenantID)

	cfg.DB.Host = loader.GetString("DB_HOST", cfg.DB.Host)
	cfg.DB.Port = loader.GetInt("DB_PORT", cfg.DB.Port)
	cfg.DB.User = loader.GetString("DB_USER", cfg.DB.User)
	cfg.DB.Password = loader.GetString("DB_PASSWORD", cfg.DB.Password)
	cfg.DB.Name = loader.GetString("DB_NAME", cfg.DB.Name)
	cfg.DB.SSLMode = loader.GetString("DB_SSLMODE", cfg.DB.SSLMode)

	// Pool de conexiones: solo se acepta un valor POSITIVO. Un negativo no es
	// "sin tope", es un error de tecleo, y en database/sql un maxOpen negativo
	// significa pool ILIMITADO — contra Neon eso se paga en conexiones abiertas,
	// no en una excepción. Esta es la red de ARRIBA; la de ABAJO está en
	// postgres.applyPool, que vuelve a descartar el negativo. Cada una tiene su
	// propio test, y a propósito: si solo hubiera uno que las cruzara, quitar
	// cualquiera de las dos guardas seguiría dando verde.
	if n := loader.GetInt("DB_MAX_OPEN_CONNS", cfg.DB.MaxOpenConns); n > 0 {
		cfg.DB.MaxOpenConns = n
	}
	if n := loader.GetInt("DB_MAX_IDLE_CONNS", cfg.DB.MaxIdleConns); n > 0 {
		cfg.DB.MaxIdleConns = n
	}
	if d := loader.GetDuration("DB_CONN_MAX_LIFETIME", cfg.DB.ConnMaxLifetime); d > 0 {
		cfg.DB.ConnMaxLifetime = d
	}
	if d := loader.GetDuration("DB_CONN_MAX_IDLE_TIME", cfg.DB.ConnMaxIdleTime); d > 0 {
		cfg.DB.ConnMaxIdleTime = d
	}

	cfg.PKI.ServerCertFile = loader.GetString("PKI_SERVER_CERT_FILE", cfg.PKI.ServerCertFile)
	cfg.PKI.ServerKeyFile = loader.GetString("PKI_SERVER_KEY_FILE", cfg.PKI.ServerKeyFile)
	cfg.PKI.CACertFile = loader.GetString("PKI_CA_CERT_FILE", cfg.PKI.CACertFile)
	cfg.PKI.CAKeyFile = loader.GetString("PKI_CA_KEY_FILE", cfg.PKI.CAKeyFile)

	cfg.Lease.PrivateKeyFile = loader.GetString("LEASE_PRIVATE_KEY_FILE", cfg.Lease.PrivateKeyFile)
	cfg.Lease.PrivateKeyB64 = loader.GetString("LEASE_PRIVATE_KEY_B64", cfg.Lease.PrivateKeyB64)
	cfg.Lease.TTLMinutes = loader.GetInt("LEASE_TTL_MINUTES", cfg.Lease.TTLMinutes)

	cfg.Crypto.KEKProvider = loader.GetString("KEK_PROVIDER", cfg.Crypto.KEKProvider)
	cfg.Crypto.KEKKMSKey = loader.GetString("KEK_KMS_KEY", cfg.Crypto.KEKKMSKey)
	cfg.Crypto.KEKKMSKeyring = loader.GetString("KEK_KMS_KEYRING", cfg.Crypto.KEKKMSKeyring)
	cfg.Crypto.KEKKMSIndexB64 = loader.GetString("KEK_KMS_INDEX_B64", cfg.Crypto.KEKKMSIndexB64)
	cfg.Crypto.KEKKeyring = loader.GetString("KEK_KEYRING", cfg.Crypto.KEKKeyring)
	cfg.Crypto.KEKCurrent = loader.GetString("KEK_CURRENT", cfg.Crypto.KEKCurrent)
	cfg.Crypto.KEKMasterB64 = loader.GetString("KEK_MASTER_B64", cfg.Crypto.KEKMasterB64)
	cfg.Crypto.KEKIndexB64 = loader.GetString("KEK_INDEX_B64", cfg.Crypto.KEKIndexB64)
	cfg.Crypto.CloudEncPrivKeyB64 = loader.GetString("CLOUD_ENC_PRIVKEY_B64", cfg.Crypto.CloudEncPrivKeyB64)

	cfg.Storage.Region = loader.GetString("STORAGE_S3_REGION", cfg.Storage.Region)
	cfg.Storage.Bucket = loader.GetString("STORAGE_S3_BUCKET", cfg.Storage.Bucket)
	cfg.Storage.AccessKeyID = loader.GetString("STORAGE_S3_ACCESS_KEY_ID", cfg.Storage.AccessKeyID)
	cfg.Storage.SecretAccessKey = loader.GetString("STORAGE_S3_SECRET_ACCESS_KEY", cfg.Storage.SecretAccessKey)
	cfg.Storage.Endpoint = loader.GetString("STORAGE_S3_ENDPOINT", cfg.Storage.Endpoint)
	cfg.Storage.PresignExpiry = loader.GetDuration("STORAGE_S3_PRESIGN_EXPIRY", cfg.Storage.PresignExpiry)

	cfg.JWT.Issuer = loader.GetString("JWT_ISSUER", cfg.JWT.Issuer)
	cfg.JWT.ECPrivateKeyFile = loader.GetString("JWT_EC_PRIVATE_KEY_FILE", cfg.JWT.ECPrivateKeyFile)
	cfg.JWT.Kid = loader.GetString("JWT_KID", cfg.JWT.Kid)

	cfg.RateLimit.PublicRPS = loader.GetInt("RATELIMIT_PUBLIC_RPS", cfg.RateLimit.PublicRPS)
	cfg.RateLimit.PublicBurst = loader.GetInt("RATELIMIT_PUBLIC_BURST", cfg.RateLimit.PublicBurst)
	cfg.RateLimit.TrustProxy = loader.GetBool("RATELIMIT_TRUST_PROXY", cfg.RateLimit.TrustProxy)

	cfg.Flow.ReplyRate = getFloat(loader, "FLOW_REPLY_RATE", cfg.Flow.ReplyRate)
	if b := loader.GetInt("FLOW_REPLY_BURST", cfg.Flow.ReplyBurst); b > 0 {
		cfg.Flow.ReplyBurst = b
	}
	cfg.Flow.IncomingTimeout = loader.GetDuration("FLOW_INCOMING_TIMEOUT", cfg.Flow.IncomingTimeout)
	cfg.Flow.MaxConcurrentIncoming = loader.GetInt("FLOW_MAX_CONCURRENT_INCOMING", cfg.Flow.MaxConcurrentIncoming)
	cfg.LLM.WarmupEnabled = loader.GetBool("LLM_WARMUP_ENABLED", cfg.LLM.WarmupEnabled)
	cfg.LLM.MaxOutputTokensEnabled = loader.GetBool("LLM_MAX_OUTPUT_TOKENS_ENABLED", cfg.LLM.MaxOutputTokensEnabled)

	cfg.Health.DegradedAfter = loader.GetDuration("HEALTH_DEGRADED_AFTER", cfg.Health.DegradedAfter)
	cfg.Health.StaleAfter = loader.GetDuration("HEALTH_STALE_AFTER", cfg.Health.StaleAfter)

	cfg.Diagnostics.BundleTTL = loader.GetDuration("DIAGNOSTICS_BUNDLE_TTL", cfg.Diagnostics.BundleTTL)

	if n := loader.GetInt("TENANT_CONTENT_MAX_BYTES", int(cfg.TenantContent.MaxBytes)); n > 0 {
		cfg.TenantContent.MaxBytes = int64(n)
	}
	if n := loader.GetInt("IMPORT_MAX_ITEMS", cfg.Import.MaxItems); n > 0 {
		cfg.Import.MaxItems = n
	}

	cfg.Identity.JWKSURL = loader.GetString("IDENTITY_JWKS_URL", cfg.Identity.JWKSURL)
	cfg.Identity.URL = loader.GetString("IDENTITY_URL", cfg.Identity.URL)
	cfg.Identity.Timeout = loader.GetDuration("IDENTITY_TIMEOUT", cfg.Identity.Timeout)
	cfg.Identity.APIKey = loader.GetString("IDENTITY_API_KEY", cfg.Identity.APIKey)

	cfg.Webhook.PollInterval = loader.GetDuration("WEBHOOK_POLL_INTERVAL", cfg.Webhook.PollInterval)
	if n := loader.GetInt("WEBHOOK_MAX_ATTEMPTS", cfg.Webhook.MaxAttempts); n > 0 {
		cfg.Webhook.MaxAttempts = n
	}
	cfg.Webhook.Timeout = loader.GetDuration("WEBHOOK_TIMEOUT", cfg.Webhook.Timeout)

	return cfg, nil
}

// getFloat lee una clave como float64 (p. ej. "0.5"). El loader de wapp-shared no
// expone un GetFloat, así que se parsea aquí: vacío, inválido o <=0 cae al default
// (nunca desactiva el tope por accidente).
func getFloat(loader *sharedconfig.Loader, key string, def float64) float64 {
	raw := loader.GetString(key, "")
	if raw == "" {
		return def
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || f <= 0 {
		return def
	}
	return f
}
