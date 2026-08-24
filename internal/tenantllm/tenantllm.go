// Package tenantllm guarda la configuración LLM del tenant: LA VÍA que usa
// (`local` | `api`, REQ-33 · D-044.28 · migración 0073) y, cuando esa vía es
// `api`, el proveedor y su credencial cifrada (Plan 044 · Ola 0 · T0.3, tabla
// public.tenant_llm de la migración 0071).
//
// 🔧 Lo que la Ola 1.5 cambió de sitio: hasta T1.5-2 este paquete solo sabía de
// la vía API y la fila SIGNIFICABA «este tenant tiene vía API». Desde la 0073 la
// fila describe la configuración ENTERA, y una fila sin credencial —vía local—
// es una configuración completa, no una a medias.
//
// Qué es esto y qué NO es: aquí vive la CREDENCIAL con la que el Cloud llama al
// proveedor externo en nombre del tenant (ADR-0030, D-044.1: la vía API la
// consume el Cloud, no el Edge). No vive el cliente HTTP del proveedor —eso es
// wapp-shared/llm (T0.1/T0.2)— ni ninguna decisión de a qué vía va cada tarea,
// que D-044.21 dejó escrito que NO SE CONSTRUYE.
//
// La API key entra CIFRADA y sale SOLO hacia quien va a llamar al proveedor. La
// capa HTTP (internal/publicapi/tenantllm.go) nunca la tiene: su puerto no
// expone ningún método que la devuelva, igual que integrations.SecretFingerprint
// mantiene el secreto HMAC fuera del paquete publicapi (crud.go:38-42).
package tenantllm

import (
	"context"
	"errors"
	"time"
)

// Vocabulario CERRADO de proveedores, el mismo que acota el CHECK
// `tenant_llm_provider_check` de la migración 0071.
//
// Se declara aquí, en el dominio, y no en la capa HTTP: quien valida es la API
// (para rechazar ANTES de la BD y no convertir un error del cliente en un 500),
// pero el vocabulario es del dominio y lo comparten el store y sus tests.
const (
	// ProviderAnthropic es la ÚNICA implementación cableada del Plan 044 (T0.2,
	// D-044.21).
	ProviderAnthropic = "anthropic"
	// ProviderGemini existe como stub que compila y falla nombrado (T0.2). El
	// CHECK lo admite para que el stub sea alcanzable: un proveedor declarado
	// que la API rechazase sería código muerto.
	ProviderGemini = "gemini"
)

// ProviderLocal NO es un proveedor de esta tabla y no está en el CHECK: `local`
// es una VÍA (ADR-0030), su implementación es futura (D-044.4) y la API la
// rechaza con 422 `llm_provider_unavailable`. Se nombra aquí para que ese
// rechazo compare contra una constante y no contra un literal suelto.
const ProviderLocal = "local"

// Vocabulario CERRADO de VÍAS, el que acota el CHECK `tenant_llm_via_check` de
// la migración 0073 (REQ-33, D-044.28, ADR-0044).
//
// 🔴 ES OTRO EJE, y por eso son otras constantes. La VÍA dice QUIÉN EJECUTA (el
// fierro del propio cliente o un proveedor externo); el PROVEEDOR dice A QUÉ
// TERCERO se llama, y solo tiene sentido DENTRO de la vía `api` (D-044.22,
// ratificada). Leer los dos vocabularios como uno es lo que producía preguntas
// sin respuesta del tipo «¿y si pido provider:"gemini" en la vía local?».
//
// ⚠️ `ViaLocal` y `ProviderLocal` comparten el literal "local" y NO se unifican:
// dicen cosas distintas en ejes distintos, y el día que el eje proveedor crezca
// o el eje vía cambie, una constante compartida obligaría a desenredarlos con
// prisa. Cada uno nombra su eje; que hoy valgan lo mismo es una coincidencia del
// vocabulario, no una relación.
const (
	// ViaLocal es el DEFAULT del producto (REQ-33) y el valor seguro: la
	// inferencia la ejecuta el Edge del propio tenant (ADR-0045) y NO sale texto
	// hacia ningún tercero, así que no exige credencial ni consentimiento.
	//
	// ⚠️ En la Ola 1.5 la API todavía RECHAZA elegirla con 422
	// `llm_provider_unavailable`: la columna admite el valor y el store sabe
	// escribirlo, pero el pipeline local no existe hasta T1.6-3. La puerta la
	// abre esa tarea, no ésta.
	ViaLocal = "local"
	// ViaAPI es la vía del proveedor externo: exige credencial cifrada +
	// `consented_at` (REQ-05, ADR-0030), y es la única cableada en el Plan 044.
	ViaAPI = "api"
)

// ValidVia dice si v pertenece al vocabulario cerrado de vías. La usan la capa
// HTTP (para rechazar ANTES de la BD, y no convertir un error del cliente en un
// 500) y el store (como guarda de programación).
func ValidVia(v string) bool { return v == ViaLocal || v == ViaAPI }

// ErrNotConfigured lo devuelve APIKey cuando el tenant no tiene fila. Es un
// sentinel y no un `found bool` porque el ÚNICO llamante que existirá —el
// pipeline, al construir el provider— tiene que distinguirlo del fallo de
// infraestructura para responder 422 `llm_credentials_missing` (design §8.1) en
// vez de 500.
var ErrNotConfigured = errors.New("tenantllm: el tenant no tiene configurada la vía LLM API")

// Config es la configuración LLM de un tenant SIN la credencial.
//
// La ausencia de la API key en este struct es DELIBERADA y es el mecanismo, no
// una omisión: es el tipo que cruza hacia la capa HTTP, y lo que no está en el
// struct no puede acabar en un log, en un %v ni en un JSON de respuesta.
// HasAPIKey dice que hay clave; para tenerla hay que pedirla aparte, por un
// método distinto, a propósito.
//
// 🔴 LOS CAMPOS DEL EJE `api` PUEDEN VENIR VACÍOS desde la 0073 (T1.5-2), y eso
// NO es una fila corrupta: es la forma de un tenant en la vía local. Provider,
// Model y ConsentedAt son `NULL` en la base para `Via == ViaLocal`, y aquí
// llegan como su valor cero. Para `Via == ViaAPI` están SIEMPRE los cuatro
// (credencial incluida) y lo garantiza Postgres, no el código:
// `tenant_llm_via_api_completa_check`.
type Config struct {
	TenantID string
	// Via es el eje QUIÉN EJECUTA (ViaLocal | ViaAPI) — REQ-33. Es el único
	// campo que TODA fila tiene relleno, porque es el que decide qué significa
	// el resto.
	Via         string
	Provider    string
	Model       string
	HasAPIKey   bool
	ConsentedAt time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Store es el puerto de persistencia de la configuración LLM por tenant. Lo
// satisface *Postgres.
//
// TODAS las operaciones van acotadas al tenant (INV-7/INV-8) y el aislamiento lo
// garantiza la firma: el tenant es un ARGUMENTO que sale del token, y la tabla
// tiene tenant_id como PRIMARY KEY. No hay forma de pedirle la fila de otro.
type Store interface {
	// Get devuelve la configuración del tenant sin la credencial. found false ⇒
	// el tenant no tiene fila, que es un estado legítimo: NO ha configurado
	// nada y por tanto está en la vía por defecto (ViaLocal, REQ-33). Quien
	// responda a eso tiene que decir `local`, no «desconocido» — la ausencia de
	// fila ES una respuesta y significa una vía concreta.
	Get(ctx context.Context, tenantID string) (Config, bool, error)

	// Upsert crea o reemplaza la configuración del tenant. Es un REEMPLAZO
	// COMPLETO: lo que no venga en cfg deja de estar en la fila.
	//
	// 🔴 QUÉ EXIGE CADA VÍA (REQ-33), que es lo que cambió en T1.5-2:
	//
	//   - cfg.Via == ViaAPI   ⇒ apiKey OBLIGATORIA y no vacía, y consentedAt no
	//     nulo. Sin las dos cosas la fila no puede existir
	//     (tenant_llm_via_api_completa_check), y el store devuelve error en vez
	//     de dejar que reviente el CHECK: un error del cliente no debe llegar a
	//     la base convertido en 500.
	//   - cfg.Via == ViaLocal ⇒ apiKey se IGNORA y la fila se escribe SIN sobre,
	//     sin proveedor, sin modelo y sin consentimiento. No hay a qué consentir:
	//     la vía local no manda texto a ningún tercero.
	//
	// ⚠️ Consecuencia deliberada de «reemplazo completo»: pasar de ViaAPI a
	// ViaLocal RETIRA la credencial guardada. No se conserva «por si vuelve»,
	// porque una credencial dormida en una fila que declara no usarla es
	// exactamente lo que REQ-33 prohíbe («una sola vía activa»), y porque
	// mantenerla obligaría a un camino write-only que este contrato no tiene
	// (el contraste con integrations.UpsertTenantIntegration —donde secret==""
	// conserva el secreto existente— es una decisión, no un descuido).
	//
	// consentedAt se ESCRIBE en cada upsert de ViaAPI: el cuerpo del PUT
	// re-afirma el consentimiento cada vez.
	Upsert(ctx context.Context, cfg Config, apiKey string, consentedAt time.Time) error

	// Delete borra la fila del tenant: revoca credencial y consentimiento de una
	// vez (design §8). Idempotente — borrar lo que no hay no es un error.
	Delete(ctx context.Context, tenantID string) error

	// APIKey descifra y devuelve la credencial del tenant. ErrNotConfigured si no
	// hay fila O si la fila no tiene sobre (tenant en la vía local desde la
	// 0073): las dos son «este tenant no tiene vía API», y el llamante —el
	// pipeline, que responde 422 llm_credentials_missing— hace lo mismo con las
	// dos. Distinguirlas le daría una rama que no sabría qué hacer.
	//
	// 🔴 NO ESTÁ EN EL PUERTO QUE CONSUME LA CAPA HTTP, y esa separación es el
	// punto: quien serializa respuestas y escribe logs no debe tener un método
	// que devuelva la clave. Lo llamará el pipeline (O2), que sí necesita el
	// valor para llamar al proveedor.
	APIKey(ctx context.Context, tenantID string) (string, error)
}
