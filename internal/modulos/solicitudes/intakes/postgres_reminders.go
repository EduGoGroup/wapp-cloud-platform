// Porta internal/intakes/postgres.go @ 64c181a

// postgres_reminders.go es lo que los dos recordatorios necesitan de la persistencia
// (DepositStore, ExpiryStore) y la configuración de notificación del tenant
// (SettingsReader). Nada de aquí abre transacción: cada método es UNA sentencia
// suelta, y en los dos «Mark» esa única sentencia ES la garantía.

package intakes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// markDepositRemindedQuery es el COMPARE-AND-SWAP que reparte el derecho a recordar
// (D-041.12, T4.4). Las cuatro condiciones son la regla entera y por eso viven en el
// WHERE y no en un `if` de Go: entre un `if` y el UPDATE cabe otro toque.
//
//   - status = ANY(...): si el dueño ya marcó la seña recibida (deposit_paid) o
//     canceló, la fila deja de casar. Nadie recuerda algo que ya se hizo.
//   - deposit_due_at IS NOT NULL AND <= $3: solo lo VENCIDO, y contra el reloj que
//     manda el llamante (el mismo con el que decidió que era candidata).
//   - deposit_reminded_at IS NULL: el que llega segundo no escribe. Ahí está el «un
//     solo recordatorio», sostenido por la BD y no por una comprobación en memoria.
//
// NO toca updated_at, y es deliberado: updated_at de esta tabla significa "la
// solicitud cambió" —su estado, sus líneas, su total— y un recordatorio no cambia
// nada de eso. Marcarlo pondría toda la bandeja del dueño como recién tocada por
// mensajes que él no mandó. El momento del recordatorio ya tiene su propia columna.
const markDepositRemindedQuery = `
	UPDATE public.intakes
	SET deposit_reminded_at = $3
	WHERE tenant_id = $1 AND id = $2
	  AND status = ANY($4)
	  AND deposit_due_at IS NOT NULL
	  AND deposit_due_at <= $3
	  AND deposit_reminded_at IS NULL
	RETURNING ` + intakeCols

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
	if _, err := uuid.Parse(intakeID); err != nil {
		return Intake{}, false, ErrNotFound
	}
	in, err := scanIntake(p.db.QueryRowContext(ctx, markDepositRemindedQuery,
		tenantID, intakeID, at, StoredVariants(StatusDepositRequested)))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Intake{}, false, nil
	case err != nil:
		return Intake{}, false, err
	}
	return in, true, nil
}

// markExpiryRemindedQuery es el COMPARE-AND-SWAP que reparte el derecho a
// recordarle al DUEÑO que un presupuesto lleva más del plazo esperando su decisión
// (Plan 044 · T4.5, D-044.50). Molde exacto del de la seña, con tres diferencias que
// no son de estilo:
//
//   - status = ANY(...) sobre `pending_approval`: en cuanto el dueño aprueba,
//     rechaza o pide información, la fila deja de casar. Nadie recuerda una decisión
//     que ya se tomó.
//   - EL PLAZO NO ES UNA COLUMNA. Aquí no hay `expiry_due_at` que comparar porque el
//     plazo es una CONSTANTE DE PLATAFORMA (QuoteDeadline, D-044.50 §1): quien
//     llama resta el plazo de su propio reloj y manda el CORTE en $5. Así la
//     constante vive en UN solo sitio —Go— y el pre-filtro y esta consulta no pueden
//     discrepar sobre cuánto dura un plazo.
//   - expiry_reminded_at IS NULL: el que llega segundo no escribe. Ahí está el «un
//     solo recordatorio», sostenido por la BD.
//
// 🔴 NO TOCA updated_at, y aquí no es solo higiene como en su gemelo: updated_at es
// la BASE del plazo (ver quoteDeadlineOf). Tocarlo aquí reiniciaría el plazo que
// este mismo UPDATE acaba de constatar como vencido, y la marca de la bandeja se
// apagaría en el mismo instante en que sale el aviso.
//
// 🔴 Y NO TOCA status. Es la mitad de la tarea: el presupuesto vencido sigue en
// `pending_approval`. Nada muere por tiempo (D-041.16); esto solo AVISA.
const markExpiryRemindedQuery = `
	UPDATE public.intakes
	SET expiry_reminded_at = $3
	WHERE tenant_id = $1 AND id = $2
	  AND status = ANY($4)
	  AND updated_at <= $5
	  AND expiry_reminded_at IS NULL
	RETURNING ` + intakeCols

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
	if _, err := uuid.Parse(intakeID); err != nil {
		return Intake{}, false, ErrNotFound
	}
	in, err := scanIntake(p.db.QueryRowContext(ctx, markExpiryRemindedQuery,
		tenantID, intakeID, at, StoredVariants(StatusPendingApproval), deadlineCutoff(at)))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Intake{}, false, nil
	case err != nil:
		return Intake{}, false, err
	}
	return in, true, nil
}

// pendingDepositRemindersQuery lista las señas vencidas y sin recordar de UN
// contacto. El predicado es el MISMO del compare-and-swap (menos el id): si
// divergieran, este camino traería candidatas que el CAS rechaza siempre y el toque
// del cliente no recordaría nunca nada.
//
// Ordena por la fecha límite ascendente —lo más vencido primero—: con la cota de
// maxRemindersPerTouch, quien lleva más tiempo esperando es a quien primero se le
// avisa.
const pendingDepositRemindersQuery = `
	SELECT ` + intakeCols + `
	FROM public.intakes
	WHERE tenant_id = $1 AND contact_id = $2
	  AND status = ANY($5)
	  AND deposit_due_at IS NOT NULL
	  AND deposit_due_at <= $3
	  AND deposit_reminded_at IS NULL
	ORDER BY deposit_due_at
	LIMIT $4`

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
	if limit <= 0 {
		return []Intake{}, nil
	}
	rows, err := p.db.QueryContext(ctx, pendingDepositRemindersQuery,
		tenantID, contactID, at, limit, StoredVariants(StatusDepositRequested))
	if err != nil {
		return nil, fmt.Errorf("intakes: listar señas vencidas del contacto: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("intakes: cerrar filas de señas vencidas: %w", cerr)
		}
	}()

	out = []Intake{}
	for rows.Next() {
		in, serr := scanIntake(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, in)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("intakes: recorrer señas vencidas: %w", rerr)
	}
	return out, nil
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
	var (
		template string
		dueDays  int
	)
	err := p.db.QueryRowContext(ctx,
		`SELECT deposit_template, deposit_due_days FROM public.tenant_settings WHERE tenant_id = $1`,
		tenantID).Scan(&template, &dueDays)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return NotifySettings{DepositDueDays: DefaultDepositDueDays}, nil
	case err != nil:
		return NotifySettings{}, fmt.Errorf("intakes: leer la config de notificación del tenant: %w", err)
	}
	if dueDays <= 0 {
		dueDays = DefaultDepositDueDays
	}
	return NotifySettings{DepositTemplate: template, DepositDueDays: dueDays}, nil
}
