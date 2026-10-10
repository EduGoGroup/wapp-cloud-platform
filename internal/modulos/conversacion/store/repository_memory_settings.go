// Porta internal/flujos/store/repository_memory.go @ c0c0c03
//
// Trozo de repository_memory.go (05 E-13): la configuración por tenant
// (tenant_settings) y la bienvenida única (conversation_welcomes) en el gemelo en
// memoria. Las reglas comunes del gemelo están en la cabecera de repository_memory.go.

package store

import (
	"context"
	"time"
)

// GetTenantSettings implementa Repository: devuelve la config sembrada para
// tenantID o DefaultTenantSettings si no hay fila, SIN error (design.md §9.E/§9.G).
//
// Los DOS caminos son los mismos que en Postgres, incluida la parte que muerde: una
// config SEMBRADA se devuelve TAL CUAL, así que un EventInactivityTTL de 0 sembrado
// sale 0 —el override «sin vencimiento» de D-043.7— y no se convierte en las 2 h del
// default. Los defaults salen de la MISMA función que usa el repo Postgres, de modo
// que los dos no pueden divergir.
func (r *MemoryRepository) GetTenantSettings(ctx context.Context, tenantID string) (TenantSettings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.settings[tenantID]; ok {
		return s, nil
	}
	return DefaultTenantSettings(tenantID), nil
}

// SetTenantSettings siembra la config del carrito para un tenant. Es un helper de
// test (imita el alta en tenant_settings).
//
// SIEMBRA LA FILA LITERALMENTE, como un INSERT que nombra TODAS las columnas: los
// campos que no rellenes quedan en el cero de Go, NO en el DEFAULT de su columna. La
// diferencia importa desde el Plan 043: un TenantSettings a medio construir deja
// EventInactivityTTL en 0, que aquí significa «sin vencimiento» mientras que la misma
// fila creada en Postgres sin nombrar la columna traería 2 h. Para sembrar un tenant
// realista parte de DefaultTenantSettings(tenantID) y cambia lo que el test necesite;
// el 0 déjalo solo cuando el 0 sea lo que se está probando.
//
// 🔴 Desde el Plan 044 · T1.2 la trampa tiene un SEGUNDO campo, y es peor que el
// primero: AggregationWindow a 0 significa FLUSH INMEDIATO (un pipeline por mensaje,
// agregación apagada), no «45 s por defecto». Un test que siembre a mano y no la
// nombre estará probando el agregador con la ventana desactivada y verá N jobs donde
// la producción vería UNO. Este repo NO parchea ceros a propósito (misma regla que el
// Postgres): parte de DefaultTenantSettings.
func (r *MemoryRepository) SetTenantSettings(s TenantSettings) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settings[s.TenantID] = s
}

// TouchContact implementa WelcomeStore en memoria con la MISMA semántica que el
// repo Postgres, incluida la que muerde: devuelve el estado ANTERIOR al toque.
//
// El gemelo Postgres necesita un CTE para conseguirlo (un `RETURNING` le daría la
// fila ya actualizada); aquí basta con copiar antes de escribir. Que se consiga de
// dos maneras distintas es irrelevante: lo que tiene que coincidir es lo que ve el
// llamante, y lo vigila reads_conformance_test.go.
func (r *MemoryRepository) TouchContact(ctx context.Context, key Key, now time.Time) (WelcomeMark, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := stateKey(key)
	previous := r.welcomes[k] // cero si no existía: «nunca habló, nunca se le saludó».
	current := previous
	current.LastIncomingAt = now
	r.welcomes[k] = current
	return previous, nil
}

// MarkWelcomed implementa WelcomeStore en memoria con el MISMO compare-and-set que
// el SQL (`welcomed_at IS NOT DISTINCT FROM $testigo`): si la marca cambió desde que
// el llamante la leyó, no se escribe y se devuelve false SIN error.
//
// ⚠️ Si NO hay fila devuelve false, igual que el UPDATE de Postgres (que afectaría 0
// filas). No la crea: MarkWelcomed solo puede llegar después de un TouchContact, que
// es quien la crea, y fabricarla aquí taparía un orden de llamadas equivocado.
func (r *MemoryRepository) MarkWelcomed(ctx context.Context, key Key, witness WelcomeMark, now time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := stateKey(key)
	current, ok := r.welcomes[k]
	if !ok || !current.WelcomedAt.Equal(witness.WelcomedAt) {
		return false, nil
	}
	current.WelcomedAt = now
	r.welcomes[k] = current
	return true, nil
}

// Welcome devuelve el estado de la bienvenida de una conversación. Helper de test:
// es el equivalente a mirar la fila de conversation_welcomes con SQL directo.
func (r *MemoryRepository) Welcome(key Key) WelcomeMark {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.welcomes[stateKey(key)]
}
