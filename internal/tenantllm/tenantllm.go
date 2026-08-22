// Package tenantllm guarda la configuración de la vía LLM API por tenant
// (Plan 044 · Ola 0 · T0.3, tabla public.tenant_llm de la migración 0071).
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
type Config struct {
	TenantID    string
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
	// el tenant no tiene fila, que es un estado legítimo (no hay vía API).
	Get(ctx context.Context, tenantID string) (Config, bool, error)

	// Upsert crea o reemplaza la configuración del tenant cifrando apiKey.
	//
	// apiKey es OBLIGATORIA en cada llamada y no puede ser vacía: las tres
	// columnas del sobre son NOT NULL en la 0071 porque una fila sin clave no
	// significa nada. Ver el comentario de la migración; el contraste con
	// integrations.UpsertTenantIntegration —donde secret=="" conserva el secreto
	// existente— es una decisión, no un descuido.
	//
	// consentedAt se ESCRIBE en cada upsert: el cuerpo del PUT re-afirma el
	// consentimiento cada vez.
	Upsert(ctx context.Context, cfg Config, apiKey string, consentedAt time.Time) error

	// Delete borra la fila del tenant: revoca credencial y consentimiento de una
	// vez (design §8). Idempotente — borrar lo que no hay no es un error.
	Delete(ctx context.Context, tenantID string) error

	// APIKey descifra y devuelve la credencial del tenant. ErrNotConfigured si no
	// hay fila.
	//
	// 🔴 NO ESTÁ EN EL PUERTO QUE CONSUME LA CAPA HTTP, y esa separación es el
	// punto: quien serializa respuestas y escribe logs no debe tener un método
	// que devuelva la clave. Lo llamará el pipeline (O2), que sí necesita el
	// valor para llamar al proveedor.
	APIKey(ctx context.Context, tenantID string) (string, error)
}
