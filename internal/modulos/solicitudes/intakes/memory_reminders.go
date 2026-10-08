// Porta internal/intakes/memory.go @ 64c181a (los dos recordatorios).
//
// Los tres métodos reciben el instante `at` del llamante y NO miran el reloj del
// store. Ninguno toca UpdatedAt: en la seña es higiene y en el plazo del presupuesto
// es la regla entera, porque UpdatedAt es la BASE de ese plazo y moverlo apagaría la
// marca justo al encender el recordatorio.

package intakes

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// MarkDepositReminded implementa DepositStore con el MISMO compare-and-swap que el
// Postgres: escribe DepositRemindedAt = `at` si y solo si la solicitud es del tenant,
// está en `deposit_requested`, tiene DepositDueAt, esa fecha ya llegó (DepositDueAt ≤
// `at`: el instante exacto cuenta) y NADIE la marcó antes. Devuelve la cabecera
// normalizada con la marca puesta y true.
//
// Si no le toca —ya recordada, aún no vence, sin fecha, en otro estado (el cliente ya
// pagó, se canceló), de otro tenant o inexistente— devuelve un Intake a cero, false y
// NINGÚN error: es el caso normal, no una avería. Y no escribe nada.
//
// Las condiciones se evalúan y la marca se escribe sin soltar el candado: de N toques
// simultáneos gana exactamente uno («un solo recordatorio», D-041.12).
func (m *MemoryStore) MarkDepositReminded(_ context.Context, tenantID, intakeID string, at time.Time) (Intake, bool, error) {
	panic(pendiente.Implementar("intakes.MemoryStore.MarkDepositReminded"))
}

// MarkExpiryReminded implementa ExpiryStore con el MISMO compare-and-swap que el
// Postgres (Plan 044 · T4.5): escribe ExpiryRemindedAt = `at` si y solo si la
// solicitud es del tenant, está vencida a fecha `at` según Overdue —la MISMA función
// que pinta la marca en la bandeja: `pending_approval` y UpdatedAt + QuoteDeadline ≤
// `at`— y NADIE la marcó antes. Devuelve la cabecera normalizada con la marca puesta y
// true.
//
// Si no le toca —ya avisada, aún en plazo, en otro estado (el dueño ya aprobó, rechazó
// o pidió información), de otro tenant o inexistente— devuelve un Intake a cero, false
// y ningún error, y no escribe nada. De N toques simultáneos gana exactamente uno.
//
// 🔴 La marca NO mata nada: la solicitud sigue en `pending_approval`.
func (m *MemoryStore) MarkExpiryReminded(_ context.Context, tenantID, intakeID string, at time.Time) (Intake, bool, error) {
	panic(pendiente.Implementar("intakes.MemoryStore.MarkExpiryReminded"))
}

// PendingDepositReminders implementa DepositStore: las señas del CONTACTO `contactID`
// en ese tenant que MarkDepositReminded marcaría a fecha `at` (misma condición: en
// `deposit_requested`, con fecha, vencida y sin recordar), lo más vencido primero
// (DepositDueAt ascendente) y acotadas a `limit`. Es de solo lectura: no marca nada.
//
// Sin candidatas devuelve un slice vacío no nil; con `limit` ≤ 0 también, sin mirar
// nada. Nunca devuelve error. Las cabeceras salen con el Status normalizado.
func (m *MemoryStore) PendingDepositReminders(_ context.Context, tenantID, contactID string, at time.Time, limit int) ([]Intake, error) {
	panic(pendiente.Implementar("intakes.MemoryStore.PendingDepositReminders"))
}
