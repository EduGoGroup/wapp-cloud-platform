package contacthelpertest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// claveSesion identifica el estado conversacional por (tenant, sesión), como public.flow_state.
type claveSesion struct {
	tenantID, sessionID string
}

// EstadoMemoria es el doble en memoria de public.flow_state: el estado conversacional por
// (tenant, sesión, contact_id) que la fusión de contactos tiene que migrar. Implementa a la vez
// contact.StateMigrator (es el migrador que se le pasa a contact.NewMemoryResolver) y Estado
// (con lo que la suite lo siembra y lo observa), y es seguro para uso concurrente.
//
// No es un mock: tiene la lógica de verdad del estado. Como en la tabla, una sesión puede tener
// a la vez estado de DOS contactos (la clave incluye el contact_id), que es el conflicto que la
// fusión resuelve; y un tenant no ve nunca el estado de otro.
//
// Cada fila lleva la marca opaca con que se sembró (ver Estado): es lo único del contenido que el
// doble guarda, y basta para que la suite vea qué fila sobrevive a una fusión.
//
// La política de conflicto que aplica MigrateContactID es la de R-17: se conserva el estado del
// canónico, con su marca, y se descarta el del huérfano. Por eso, con memoria, los casos de fusión de estado de
// Contrato prueban que MemoryResolver llama al migrador (con qué huérfano, qué canónico y qué
// tenant) y no la política: esa es de este doble. Con Postgres la aplica el SQL de la fusión.
type EstadoMemoria struct {
	mu sync.Mutex
	// duenos guarda, por sesión, los contact_id con estado en ella y, para cada uno, la marca de
	// su fila.
	duenos map[claveSesion]map[string]string
}

var (
	_ Estado                = (*EstadoMemoria)(nil)
	_ contact.StateMigrator = (*EstadoMemoria)(nil)
)

// NuevoEstado devuelve un EstadoMemoria vacío: sin estado en ningún tenant.
func NuevoEstado() *EstadoMemoria {
	return &EstadoMemoria{duenos: make(map[claveSesion]map[string]string)}
}

// Sembrar implementa Estado: da estado a la sesión sessionID del tenant tenantID con contactID
// como dueño y mark como marca de la fila. Sembrar a un segundo contacto en la misma sesión lo
// suma como segundo dueño, con su marca (el conflicto de la fusión). Repetir un (tenant, sesión,
// contacto) ya sembrado no cambia nada, ni siquiera con otra marca: se queda la primera, como el
// INSERT … ON CONFLICT DO NOTHING sobre la clave primaria de flow_state. Falla el test t si alguno
// de los cuatro argumentos viene vacío: no es un estado, y una marca vacía no distinguiría filas.
func (e *EstadoMemoria) Sembrar(t *testing.T, tenantID, sessionID, contactID, mark string) {
	t.Helper()
	if tenantID == "" || sessionID == "" || contactID == "" || mark == "" {
		t.Fatalf("EstadoMemoria.Sembrar(tenant %q, sesión %q, contacto %q, marca %q): los cuatro son obligatorios", tenantID, sessionID, contactID, mark)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	clave := claveSesion{tenantID: tenantID, sessionID: sessionID}
	if e.duenos[clave] == nil {
		e.duenos[clave] = make(map[string]string)
	}
	if _, ya := e.duenos[clave][contactID]; ya {
		return // ya sembrado: gana la primera marca
	}
	e.duenos[clave][contactID] = mark
}

// Dueno implementa Estado: el contact_id dueño del estado de la sesión sessionID del tenant
// tenantID y la marca de su fila, o ok=false (y los dos vacíos) si no tiene. Si la sesión tuviera
// más de un dueño —una fusión que dejó el estado del huérfano junto al del canónico— falla el
// test t nombrándolos, en vez de elegir uno. El estado de otro tenant no se ve.
func (e *EstadoMemoria) Dueno(t *testing.T, tenantID, sessionID string) (contactID, mark string, ok bool) {
	t.Helper()
	filas := e.rowsOf(tenantID, sessionID)
	duenos := slices.Sorted(maps.Keys(filas))
	switch len(duenos) {
	case 0:
		return "", "", false
	case 1:
		return duenos[0], filas[duenos[0]], true
	default:
		t.Fatalf("estado inconsistente: la sesión %q del tenant %q tiene %d dueños %q y debía tener uno", sessionID, tenantID, len(duenos), duenos)
		return "", "", false
	}
}

// rowsOf devuelve una copia de las filas de estado de la sesión del tenant: contact_id → marca.
// Vacía (nil) si no hay estado. Es copia para que nadie lea el mapa del doble fuera del cerrojo.
func (e *EstadoMemoria) rowsOf(tenantID, sessionID string) map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return maps.Clone(e.duenos[claveSesion{tenantID: tenantID, sessionID: sessionID}])
}

// duenosDe devuelve, ordenados, los contact_id con estado en la sesión del tenant: ninguno si no
// hay estado.
func (e *EstadoMemoria) duenosDe(tenantID, sessionID string) []string {
	return slices.Sorted(maps.Keys(e.rowsOf(tenantID, sessionID)))
}

// MigrateContactID implementa contact.StateMigrator: re-clava en toContactID el estado de
// fromContactID en todas las sesiones del tenant tenantID, y solo de ese tenant.
//
// Política de conflicto (R-17): si el canónico (toContactID) ya tiene estado en una sesión, se
// conserva el suyo, con su marca, y se descarta el del huérfano; las demás sesiones del huérfano
// migran con la marca que tenían (la fila se re-clava, no se rehace). Es
// idempotente: repetirla no cambia nada, y migrar un huérfano sin estado, o un contacto sobre sí
// mismo, es un no-op sin error. Con un contexto ya cancelado devuelve su error (envuelto con %w)
// sin tocar nada.
func (e *EstadoMemoria) MigrateContactID(ctx context.Context, tenantID, fromContactID, toContactID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("contacthelpertest: migrar estado de %q a %q: %w", fromContactID, toContactID, err)
	}
	if fromContactID == toContactID {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for clave, duenos := range e.duenos {
		if clave.tenantID != tenantID {
			continue
		}
		mark, tiene := duenos[fromContactID]
		if !tiene {
			continue
		}
		delete(duenos, fromContactID)
		if _, conflicto := duenos[toContactID]; conflicto {
			continue // el canónico ya tenía estado en la sesión: gana el suyo, con su marca
		}
		duenos[toContactID] = mark
	}
	return nil
}
