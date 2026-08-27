// Package casebank es el BANCO DE CASOS del pipeline de captación: solicitudes
// reales de clientes, ya anonimizadas y con consentimiento, junto a la
// interpretación que el pipeline debería haber producido (Plan 044 · Ola 5 ·
// T5.3, design §6.4, tabla `intake_case_bank` de la migración 0082).
//
// # POR QUÉ ESTÁ AQUÍ Y NO BAJO `internal/intake/`
//
// Porque NINGÚN camino de producción lo lee. El banco no alimenta al pipeline:
// alimenta a quien lo EVALÚA. Colgarlo de `internal/intake/` diría lo contrario
// —que P2/P3/P4 tienen algo que buscar aquí— y esa lectura equivocada es
// exactamente la que convertiría un dataset en una fuente de contexto para el
// modelo. Vive al lado de `internal/degradation`, `internal/evidence` y
// `internal/inferstats`, que son las otras piezas transversales del plan.
//
// # LAS DOS PUERTAS Y POR QUÉ SON DOS
//
//   - `Servicio.Insertar` es la ÚNICA puerta legítima de escritura: valida el
//     consentimiento con un error tipado ANTES de tocar la base y anonimiza el
//     literal antes de que salga de este proceso;
//   - el CHECK `intake_case_bank_consented_check` de la 0082 es la red debajo de
//     la red, para el INSERT a mano y para el store que alguien escriba mañana.
//
// 🔴 Las dos son necesarias y NO se tapan la una a la otra: el test del guard es
// de unidad y no abre conexión (borra el guard y el doble del store recibe la
// llamada), el test del CHECK es de integración y hace el INSERT crudo saltándose
// el servicio (borra el `ADD CONSTRAINT` y la base acepta la fila). Cada uno
// tiene una mutación que solo lo mata a él.
package casebank

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Los tres motivos por los que `Insertar` se niega ANTES de ir a la base. Son
// errores tipados y no un `fmt.Errorf` suelto porque el llamador tiene que poder
// distinguirlos: el de consentimiento es un NO rotundo (nunca se reintenta, y es
// el criterio literal de T5.3), y los otros dos son datos que faltan.
var (
	// ErrSinConsentimiento es EL guard de T5.3: sin `consented=true` no se
	// inserta. 🔴 Se devuelve sin haber llamado al store — no es «la base lo
	// rechazó», es «esto no llega a la base».
	ErrSinConsentimiento = errors.New("casebank: el caso no lleva consentimiento (consented=true) y no se inserta")
	// ErrSinTenant: un caso sin dueño no se puede consentir ni reutilizar.
	ErrSinTenant = errors.New("casebank: el caso no lleva tenant_id")
	// ErrSinTexto: un caso sin literal no es material de evaluación.
	ErrSinTexto = errors.New("casebank: el caso no lleva source_text")
)

// Caso es una fila del banco, tal como el llamador la propone.
//
// 🔴 `SourceText` VIAJA EN CRUDO HASTA `Insertar` y sale ANONIMIZADO de ahí. Es
// deliberado que el tipo no distinga las dos formas con dos campos: un struct con
// `Crudo` y `Anonimo` invita a persistir el que no toca, y el compilador no
// ayudaría a distinguirlos porque los dos son `string`. La regla es que el crudo
// no sobrevive a la llamada.
type Caso struct {
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

// Store es el puerto de persistencia. Lo implementa `Postgres`.
type Store interface {
	// Insertar escribe la fila y devuelve su `id`. Recibe el caso YA
	// ANONIMIZADO: este puerto no sabe anonimizar y no debe.
	Insertar(ctx context.Context, c Caso) (int64, error)
	// Existe dice si ese tenant ya tiene un caso con ese literal EXACTO. Es el
	// guard de idempotencia de la siembra.
	Existe(ctx context.Context, tenantID, sourceText string) (bool, error)
}

// Servicio es la puerta de escritura del banco.
type Servicio struct {
	store Store
	anon  Anonimizador
}

// NewServicio construye el servicio. Falla si le falta el store: un servicio a
// medias que compile y luego panique en la primera llamada es peor que un error
// en el arranque (mismo criterio que `NewP2`/`NewP3` del pipeline).
func NewServicio(store Store, anon Anonimizador) (*Servicio, error) {
	if store == nil {
		return nil, errors.New("casebank: NewServicio sin store")
	}
	return &Servicio{store: store, anon: anon}, nil
}

// Insertar mete un caso en el banco. Devuelve el `id` de la fila.
//
// EL ORDEN ES EL CRITERIO, y por eso está escrito y no solo codificado:
//
//  1. valida — y si el consentimiento falta, DEVUELVE SIN LLAMAR AL STORE. Lo que
//     T5.3 pide («insert sin consentimiento ⇒ error») se cumple aquí y no en el
//     CHECK: dejar que lo rechazara la base convertiría un error del llamador en
//     un error de infraestructura, que es el mismo argumento con el que la 0071
//     puso el 400 del consentimiento en la API y no en el `NOT NULL`;
//  2. anonimiza — y solo entonces el literal puede salir de este proceso.
//
// 🔴 NO se vuelve a barrer con `Restos` después de anonimizar para «confirmar»
// que quedó limpio: sería una tautología (los dos usan los mismos detectores) y
// una red que se comprueba a sí misma tapa a los tests que sí miran. El barrido
// se aplica al texto que NO pasó por aquí — el fixture escrito a mano, ver
// `semilla.go`.
func (s *Servicio) Insertar(ctx context.Context, c Caso) (int64, error) {
	if err := validar(c); err != nil {
		return 0, err
	}
	c.SourceText = s.anon.Anonimizar(c.SourceText)
	id, err := s.store.Insertar(ctx, c)
	if err != nil {
		return 0, fmt.Errorf("casebank: insertar el caso del tenant %q: %w", c.TenantID, err)
	}
	return id, nil
}

// Sembrar inserta el caso si ese tenant no lo tiene ya, y dice si escribió.
// Devuelve (id, true) cuando sembró y (0, false) cuando ya estaba.
//
// ⚠️ LA COMPROBACIÓN ES CONTRA EL TEXTO ANONIMIZADO, que es el que está en la
// base: preguntar por el crudo daría siempre «no existe» y sembraría un duplicado
// en cada corrida. Es el defecto obvio de este método y por eso la anonimización
// ocurre ANTES de preguntar y se le pasa al store ya hecha.
//
// ⚠️ Y NO ES ATÓMICO: entre el `Existe` y el `Insertar` cabe otra corrida. Se
// acepta a sabiendas — esto lo ejecuta una persona desde `cmd/casebank`, no un
// worker, y un caso duplicado en un dataset es un incordio, no una corrupción.
// La alternativa (un índice único sobre un TEXT sin cota) haría FALLAR el insert
// de los casos largos, que son los que más falta hacen (ver la 0082).
func (s *Servicio) Sembrar(ctx context.Context, c Caso) (int64, bool, error) {
	if err := validar(c); err != nil {
		return 0, false, err
	}
	c.SourceText = s.anon.Anonimizar(c.SourceText)

	ya, err := s.store.Existe(ctx, c.TenantID, c.SourceText)
	if err != nil {
		return 0, false, fmt.Errorf("casebank: comprobar si el caso ya estaba: %w", err)
	}
	if ya {
		return 0, false, nil
	}
	id, err := s.store.Insertar(ctx, c)
	if err != nil {
		return 0, false, fmt.Errorf("casebank: sembrar el caso del tenant %q: %w", c.TenantID, err)
	}
	return id, true, nil
}

// validar es el guard. Está extraído para que `Insertar` y `Sembrar` no puedan
// divergir: dos copias de una regla de admisión se desincronizan, y la que se
// queda vieja es siempre la del camino menos transitado.
func validar(c Caso) error {
	if strings.TrimSpace(c.TenantID) == "" {
		return ErrSinTenant
	}
	if strings.TrimSpace(c.SourceText) == "" {
		return ErrSinTexto
	}
	if !c.Consented {
		return ErrSinConsentimiento
	}
	return nil
}
