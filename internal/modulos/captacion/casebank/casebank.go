// Porta internal/casebank/casebank.go @ 8d875ab

// Package casebank es el BANCO DE CASOS del pipeline de captación: solicitudes
// reales de clientes, ya anonimizadas y con consentimiento, junto a la
// interpretación que el pipeline debería haber producido (Plan 044 · Ola 5 ·
// T5.3, design §6.4, tabla `intake_case_bank` de la migración 0082).
//
// # POR QUÉ ES UNA HOJA Y NO PARTE DEL PIPELINE
//
// Porque NINGÚN camino de producción lo lee. El banco no alimenta al pipeline:
// alimenta a quien lo EVALÚA. Colgarlo del pipeline diría lo contrario —que
// P2/P3/P4 tienen algo que buscar aquí— y esa lectura equivocada es exactamente
// la que convertiría un dataset en una fuente de contexto para el modelo. Hoy
// solo lo usa el CLI `cmd/casebank`, que sigue sobre el paquete viejo hasta F10
// (D-F7-2).
//
// # LAS DOS PUERTAS Y POR QUÉ SON DOS
//
//   - `Service.Insert` es la ÚNICA puerta legítima de escritura: valida el
//     consentimiento con un error tipado ANTES de tocar la base y anonimiza el
//     literal antes de que salga de este proceso;
//   - el CHECK `intake_case_bank_consented_check` de la 0082 es la red debajo de
//     la red, para el INSERT a mano y para el store que alguien escriba mañana.
//
// 🔴 Las dos son necesarias y NO se tapan la una a la otra: el test del guard es
// de unidad y no abre conexión (borra el guard y el doble del store recibe la
// llamada), y el del CHECK es un caso de la suite del puerto
// (casebankhelpertest.Contrato), que llama al store saltándose el servicio. Cada
// uno tiene una mutación que solo lo mata a él.
package casebank

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Los tres motivos por los que `Insert` y `Seed` se niegan ANTES de ir a la base.
// Son errores tipados y no un `fmt.Errorf` suelto porque el llamador tiene que
// poder distinguirlos: el de consentimiento es un NO rotundo (nunca se reintenta,
// y es el criterio literal de T5.3), y los otros dos son datos que faltan. Sus
// textos son los del paquete viejo, literales.
var (
	// ErrNoConsent (antes `ErrSinConsentimiento`) es EL guard de T5.3: sin
	// `consented=true` no se inserta. 🔴 Se devuelve sin haber llamado al store —
	// no es «la base lo rechazó», es «esto no llega a la base».
	ErrNoConsent = errors.New("casebank: el caso no lleva consentimiento (consented=true) y no se inserta")
	// ErrNoTenant (antes `ErrSinTenant`): un caso sin dueño no se puede consentir
	// ni reutilizar.
	ErrNoTenant = errors.New("casebank: el caso no lleva tenant_id")
	// ErrNoText (antes `ErrSinTexto`): un caso sin literal no es material de
	// evaluación.
	ErrNoText = errors.New("casebank: el caso no lleva source_text")
)

// Case (antes `Caso`) es una fila del banco, tal como el llamador la propone.
//
// 🔴 `SourceText` VIAJA EN CRUDO HASTA `Service.Insert` y sale ANONIMIZADO de
// ahí. Es deliberado que el tipo no distinga las dos formas con dos campos: un
// struct con `Crudo` y `Anonimo` invita a persistir el que no toca, y el
// compilador no ayudaría a distinguirlos porque los dos son `string`. La regla es
// que el crudo no sobrevive a la llamada.
type Case struct {
	// TenantID es el dueño del caso.
	TenantID string
	// Consented es el consentimiento explícito del tenant. Sin `true` no hay fila.
	Consented bool
	// SourceText es el literal del cliente. Entra crudo y se persiste anonimizado.
	SourceText string
	// Expected es la interpretación correcta, curada a mano. Puede ir vacía: un
	// caso sin etiquetar ya es material útil.
	Expected json.RawMessage
}

// Store es el puerto de persistencia. Lo implementan `Postgres` y el doble
// `casebankhelpertest.Memory`; sus promesas las fija la suite
// `casebankhelpertest.Contrato`, que corren los dos.
type Store interface {
	// Insert (antes `Insertar`) escribe la fila y devuelve su `id`, distinto de
	// cero y distinto del de cualquier otra fila. Recibe el caso YA ANONIMIZADO:
	// este puerto no sabe anonimizar y no debe, así que guarda `SourceText` byte a
	// byte. Un `Expected` vacío (nil o de longitud cero) se guarda como «sin
	// curar» (NULL), no como JSON.
	//
	// No valida `TenantID` ni `SourceText` y NO deduplica: el mismo caso insertado
	// dos veces son dos filas. Lo único que rechaza por su cuenta es lo que la
	// tabla rechaza: un caso con `Consented=false` (CHECK
	// `intake_case_bank_consented_check`, cuyo nombre viaja en el error) y un
	// `Expected` que no es JSON. En los dos casos devuelve error y NO escribe
	// nada. Ese error NO es `ErrNoConsent`: el centinela es del servicio.
	Insert(ctx context.Context, c Case) (int64, error)
	// Exists (antes `Existe`) dice si ese tenant ya tiene un caso con ese literal
	// EXACTO (comparación byte a byte: ni mayúsculas, ni espacios, ni prefijos
	// cuentan como iguales). Los casos de otro tenant no cuentan. Es el guard de
	// idempotencia de la siembra. No escribe.
	Exists(ctx context.Context, tenantID, sourceText string) (bool, error)
}

// Service (antes `Servicio`) es la puerta de escritura del banco.
type Service struct {
	store Store
	anon  Anonymizer
}

// NewService (antes `NewServicio`) construye el servicio sobre el store y el
// anonimizador. Falla con el error "casebank: NewServicio sin store" (texto del
// paquete viejo, literal) si el store es nil: un servicio a medias que compile y
// luego panique en la primera llamada es peor que un error en el arranque (mismo
// criterio que `NewP2`/`NewP3` del pipeline). El anonimizador vacío es legítimo.
func NewService(store Store, anon Anonymizer) (*Service, error) {
	if store == nil {
		return nil, errors.New("casebank: NewServicio sin store")
	}
	return &Service{store: store, anon: anon}, nil
}

// Insert (antes `Servicio.Insertar`) mete un caso en el banco. Devuelve el `id`
// de la fila.
//
// EL ORDEN ES EL CRITERIO, y por eso está escrito y no solo codificado:
//
//  1. valida — y si algo falta, DEVUELVE SIN LLAMAR AL STORE: `ErrNoTenant` si
//     `TenantID` es vacío o solo espacios; si no, `ErrNoText` si `SourceText` es
//     vacío o solo espacios; si no, `ErrNoConsent` si `Consented` es false. Lo
//     que T5.3 pide («insert sin consentimiento ⇒ error») se cumple aquí y no en
//     el CHECK: dejar que lo rechazara la base convertiría un error del llamador
//     en un error de infraestructura, que es el mismo argumento con el que la
//     0071 puso el 400 del consentimiento en la API y no en el `NOT NULL`;
//  2. anonimiza — y solo entonces el literal puede salir de este proceso: el
//     store recibe `SourceText` ya pasado por `Anonymizer.Anonymize`, y el resto
//     del caso tal cual.
//
// Si el store falla, devuelve 0 y el error envuelto (%w) con el prefijo
// `casebank: insertar el caso del tenant "<tenant>": `.
//
// 🔴 NO se vuelve a barrer con `Remains` después de anonimizar para «confirmar»
// que quedó limpio: sería casi una tautología (los dos usan los mismos detectores) y
// una red que se comprueba a sí misma tapa a los tests que sí miran. El barrido
// se aplica al texto que NO pasó por aquí — el fixture escrito a mano, ver
// `seed.go`.
func (s *Service) Insert(ctx context.Context, c Case) (int64, error) {
	if err := validate(c); err != nil {
		return 0, err
	}
	c.SourceText = s.anon.Anonymize(c.SourceText)
	id, err := s.store.Insert(ctx, c)
	if err != nil {
		return 0, fmt.Errorf("casebank: insertar el caso del tenant %q: %w", c.TenantID, err)
	}
	return id, nil
}

// Seed (antes `Sembrar`) inserta el caso si ese tenant no lo tiene ya, y dice si
// escribió. Devuelve (id, true) cuando sembró y (0, false) cuando ya estaba.
// Valida igual que `Insert` y con los mismos centinelas, sin consultar ni
// escribir cuando la validación falla.
//
// ⚠️ LA COMPROBACIÓN ES CONTRA EL TEXTO ANONIMIZADO, que es el que está en la
// base: preguntar por el crudo daría siempre «no existe» y sembraría un duplicado
// en cada corrida. Es el defecto obvio de este método y por eso la anonimización
// ocurre ANTES de preguntar y se le pasa al store ya hecha.
//
// Errores del store, envueltos con %w y con (0, false):
//
//   - "casebank: comprobar si el caso ya estaba: " — falla `Exists`; no se escribe;
//   - `casebank: sembrar el caso del tenant "<tenant>": ` — falla `Insert`.
//
// ⚠️ Y NO ES ATÓMICO: entre el `Exists` y el `Insert` cabe otra corrida. Se
// acepta a sabiendas — esto lo ejecuta una persona desde `cmd/casebank`, no un
// worker, y un caso duplicado en un dataset es un incordio, no una corrupción.
// La alternativa (un índice único sobre un TEXT sin cota) haría FALLAR el insert
// de los casos largos, que son los que más falta hacen (ver la 0082).
func (s *Service) Seed(ctx context.Context, c Case) (int64, bool, error) {
	if err := validate(c); err != nil {
		return 0, false, err
	}
	c.SourceText = s.anon.Anonymize(c.SourceText)

	found, err := s.store.Exists(ctx, c.TenantID, c.SourceText)
	if err != nil {
		return 0, false, fmt.Errorf("casebank: comprobar si el caso ya estaba: %w", err)
	}
	if found {
		return 0, false, nil
	}
	id, err := s.store.Insert(ctx, c)
	if err != nil {
		return 0, false, fmt.Errorf("casebank: sembrar el caso del tenant %q: %w", c.TenantID, err)
	}
	return id, true, nil
}

// validate (antes `validar`) es el guard. Está extraído para que `Insert` y
// `Seed` no puedan divergir: dos copias de una regla de admisión se
// desincronizan, y la que se queda vieja es siempre la del camino menos
// transitado.
func validate(c Case) error {
	if strings.TrimSpace(c.TenantID) == "" {
		return ErrNoTenant
	}
	if strings.TrimSpace(c.SourceText) == "" {
		return ErrNoText
	}
	if !c.Consented {
		return ErrNoConsent
	}
	return nil
}
