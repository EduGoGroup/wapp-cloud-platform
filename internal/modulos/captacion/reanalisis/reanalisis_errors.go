// Porta internal/reanalisis/reanalisis.go @ 56097aa (E-13: los desenlaces con nombre,
// `reanalisis.go:139-233` del viejo).

package reanalisis

import (
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// reanalisis_errors.go — LOS DESENLACES CON NOMBRE (design §8.1).
//
// Cada uno es un TIPO y no un centinela porque los cinco llevan un dato dentro —la
// vía que se pidió, la feature que falta, la razón, el job que estorba— y ese dato
// va en el CUERPO de la respuesta. Un centinela obligaría al transporte a
// reconstruirlo, y reconstruirlo mal es cómo un 403 acaba nombrando la feature
// equivocada. Se leen con errors.As (son tipos VALOR, no punteros). Los textos de
// `Error()` son observables y se copian literales del viejo.

// Razones del `422 source_unavailable` (design §8.1). Son un vocabulario del
// CONTRATO, no de la base: viajan al cliente dentro del cuerpo del error.
const (
	// ReasonPurged (antes `RazonPurgada`), `purged` — el literal EXISTIÓ y ya no
	// está: quedan filas `message` del evento pero ninguna conserva cuerpo.
	//
	// ⚠️ HOY NADIE PUEDE PRODUCIR ESTE ESTADO: el Plan 046 NO construyó ninguna poda
	// de `conversation_event_messages` (ADR-0043), así que la retención de esa tabla
	// es INDEFINIDA. La rama existe porque vaciar el cuerpo dejando la fila es la
	// forma que esa poda tendrá el día que se construya, y porque el contrato la
	// exige. Se prueba con dobles.
	ReasonPurged = "purged"
	// ReasonNeverStored (antes `RazonNuncaGuardada`), `never_stored` — no hay NI UNA
	// fila `message` para ese evento: el tenant no tenía `llm_intake` cuando ocurrió
	// (nivel 3 del ADR-0034), o la solicitud es legada y no cuelga de ningún evento.
	ReasonNeverStored = "never_stored"
)

// InvalidViaError (antes `ViaInvalidaError`) es el `400 invalid_via`.
type InvalidViaError struct {
	// Via es el valor que mandó el cliente, TAL CUAL.
	Via string
	// Configured (antes `Configurada`) es la vía EFECTIVA del tenant, cuando el
	// rechazo es por contradecirla. Vacía cuando el rechazo es de vocabulario.
	Configured string
}

// Error devuelve, con `Configured` vacío, `reanalisis: "<via>" no es una vía
// (local|api)`; y con él, `reanalisis: la vía "<via>" no es la configurada por el
// tenant ("<configurada>")`. Las vías van con `%q`.
func (e InvalidViaError) Error() string {
	panic(pendiente.Implementar("reanalisis.InvalidViaError.Error"))
}

// FeatureMissingError (antes `FeatureAusenteError`) es el `403 feature_not_enabled`.
// Lleva la clave porque son DOS casos con dos claves distintas —`llm_intake` (el
// nivel) y `api_llm` (la vía)— y la UI ofrece un upgrade distinto con cada una.
type FeatureMissingError struct{ Feature string }

// Error devuelve `reanalisis: el tenant no tiene la capacidad "<feature>"`.
func (e FeatureMissingError) Error() string {
	panic(pendiente.Implementar("reanalisis.FeatureMissingError.Error"))
}

// CredentialsMissingError (antes `CredencialAusenteError`) es el
// `422 llm_credentials_missing`: la feature SÍ está, pero no hay credencial ni
// consentimiento.
//
// 🔴 NO SE MEZCLA CON FeatureMissingError. «Tu plan no lo incluye» manda a la UI al
// paywall del add-on; «configura tus credenciales» la manda a los ajustes de
// `tenant-llm`. Fundirlos dejaría a un tenant que YA PAGÓ mirando una pantalla de venta.
type CredentialsMissingError struct{ Via string }

// Error devuelve `reanalisis: la vía "<via>" exige credencial y consentimiento en
// tenant_llm`.
func (e CredentialsMissingError) Error() string {
	panic(pendiente.Implementar("reanalisis.CredentialsMissingError.Error"))
}

// SourceUnavailableError (antes `FuenteAusenteError`) es el `422 source_unavailable`,
// con su razón (ReasonPurged o ReasonNeverStored).
type SourceUnavailableError struct{ Reason string }

// Error devuelve `reanalisis: no hay literal original que re-analizar (<razón>)`.
func (e SourceUnavailableError) Error() string {
	panic(pendiente.Implementar("reanalisis.SourceUnavailableError.Error"))
}

// InProgressError (antes `EnCursoError`) es el `422 reanalysis_in_progress`: ya hay un
// job NO TERMINAL para ese evento. Lleva el id del job para que quien llame pueda
// seguirlo en vez de reintentar a ciegas.
type InProgressError struct{ JobID string }

// Error devuelve `reanalisis: el evento ya tiene un job vivo (<job>)`.
func (e InProgressError) Error() string {
	panic(pendiente.Implementar("reanalisis.InProgressError.Error"))
}

// ErrNotWired (antes `ErrSinCablear`) es el servicio al que le falta una pieza. Su
// texto es literal del viejo: `reanalisis: el servicio necesita log, solicitudes,
// hilo, jobs, compositor, features y config LLM`. Lo devuelven NewService (con una
// pieza a nil, o envuelto con un límite del hilo no positivo) y Reanalyze sobre un
// servicio nil.
var ErrNotWired = errors.New("reanalisis: el servicio necesita log, solicitudes, hilo, jobs, compositor, features y config LLM")
