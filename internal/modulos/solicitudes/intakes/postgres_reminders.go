// Porta internal/intakes/postgres.go @ 64c181a

// postgres_reminders.go es lo que los dos recordatorios necesitan de la persistencia
// (DepositStore, ExpiryStore) y la configuración de notificación del tenant
// (SettingsReader). Nada de aquí abre transacción: cada método es UNA sentencia
// suelta, y en los dos «Mark» esa única sentencia ES la garantía.

package intakes

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// MarkDepositReminded implementa DepositStore: intenta ganarse el derecho a recordar
// la seña de UNA solicitud.
//
// Es un COMPARE-AND-SWAP en una sola sentencia: pone deposit_reminded_at = `at` si y
// solo si la solicitud es del tenant, su estado almacenado es una variante de
// StatusDepositRequested, tiene deposit_due_at, esa fecha es <= `at` y NADIE la marcó
// antes. Ahí está toda la garantía de «un solo recordatorio»: leer y decidir en
// memoria dejaría sitio a otro toque entre la lectura y el envío.
//
//   - escribió ⇒ (la solicitud con la marca puesta, true, nil);
//   - no le tocaba (no casa ninguna fila) ⇒ (Intake{}, false, nil): es el caso normal,
//     no una avería, y NO distingue «no existe» de «no vencida» o «ya recordada»;
//   - intakeID que no es un UUID ⇒ (Intake{}, false, ErrNotFound) sin tocar la base;
//   - fallo de la base ⇒ (Intake{}, false, err) con "intakes: leer solicitud: ".
//
// El instante es el del llamante (`at`), no el reloj de la base: es el mismo con el
// que el llamante decidió que tocaba.
func (p *Postgres) MarkDepositReminded(ctx context.Context, tenantID, intakeID string, at time.Time) (Intake, bool, error) {
	panic(pendiente.Implementar("intakes.Postgres.MarkDepositReminded"))
}

// MarkExpiryReminded implementa ExpiryStore: intenta ganarse el derecho a avisar al
// dueño de que el plazo de UN presupuesto venció (T4.5).
//
// Mismo compare-and-swap que MarkDepositReminded, con otra condición: pone
// expiry_reminded_at = `at` si y solo si la solicitud es del tenant, su estado
// almacenado es una variante de StatusPendingApproval, su updated_at es <= el corte
// del plazo calculado desde `at` (el mismo corte que usa el vencimiento en memoria)
// y nadie la marcó antes. El plazo se mide desde updated_at porque es el último
// movimiento del dueño sobre el presupuesto.
//
//   - escribió ⇒ (la solicitud con la marca puesta, true, nil);
//   - no le tocaba ⇒ (Intake{}, false, nil);
//   - intakeID que no es un UUID ⇒ (Intake{}, false, ErrNotFound) sin tocar la base;
//   - fallo de la base ⇒ (Intake{}, false, err) con "intakes: leer solicitud: ".
func (p *Postgres) MarkExpiryReminded(ctx context.Context, tenantID, intakeID string, at time.Time) (Intake, bool, error) {
	panic(pendiente.Implementar("intakes.Postgres.MarkExpiryReminded"))
}

// PendingDepositReminders implementa DepositStore: las solicitudes de ESE contacto
// a las que tocaría recordarles la seña a fecha `at` —en una variante de
// StatusDepositRequested, con fecha límite <= `at` y sin recordar—, como mucho
// `limit`, de la que venció antes a la que venció después (ORDER BY deposit_due_at).
//
// SOLO LEE: no marca nada. Quien quiera recordar tiene que ganarse cada una con
// MarkDepositReminded.
//
// Con limit <= 0 devuelve ([]Intake{}, nil) sin tocar la base. Sin filas, slice vacío
// y no nil.
//
// Errores, envueltos con %w y devolviendo (nil, err):
//
//   - "intakes: listar señas vencidas del contacto: " — falla la consulta;
//   - "intakes: leer solicitud: " — una fila no se puede escanear;
//   - "intakes: recorrer señas vencidas: " — falla el recorrido;
//   - "intakes: cerrar filas de señas vencidas: " — falla el cierre cuando lo demás
//     fue bien.
func (p *Postgres) PendingDepositReminders(ctx context.Context, tenantID, contactID string, at time.Time, limit int) (out []Intake, err error) {
	panic(pendiente.Implementar("intakes.Postgres.PendingDepositReminders"))
}

// NotifySettings implementa SettingsReader: la plantilla del aviso de la seña y el
// plazo en días, de public.tenant_settings.
//
//   - tenant SIN fila de config ⇒ (NotifySettings{DepositDueDays: DefaultDepositDueDays}, nil):
//     plantilla vacía —el llamante usa la de plataforma— y el plazo por defecto. No
//     es un error;
//   - fila con deposit_due_days <= 0 ⇒ el plazo sale como DefaultDepositDueDays; la
//     plantilla sale tal cual, sin recortar;
//   - fallo de la base ⇒ (NotifySettings{}, err) con
//     "intakes: leer la config de notificación del tenant: ".
func (p *Postgres) NotifySettings(ctx context.Context, tenantID string) (NotifySettings, error) {
	panic(pendiente.Implementar("intakes.Postgres.NotifySettings"))
}
