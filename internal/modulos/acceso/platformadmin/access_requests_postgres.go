// Porta internal/platformadmin/access_requests.go @ 9a77307, líneas 135-336 y 481-526: el SQL de
// la bandeja de solicitudes de acceso, separado de sus reglas (D-F2-3). Es la mitad Postgres del
// puerto AccessRequestStore (ports.go); las reglas que lo orquestan viven en access_requests.go.

package platformadmin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/postgres"
)

// ListAccessRequests implementa AccessRequestStore.ListAccessRequests: un SELECT por status
// ("" ⇒ 'pending') con ORDER BY created_at ASC. Un fallo de la consulta, del escaneo o de la
// iteración se devuelve envuelto ("platformadmin: list|scan|iterate access request(s): …").
func (r *Repository) ListAccessRequests(ctx context.Context, status string) ([]AccessRequestItem, error) {
	if status == "" {
		status = "pending"
	}

	query := `
		SELECT id::text, user_id::text, email, origin, status, created_at
		FROM public.access_requests
		WHERE status = $1
		ORDER BY created_at ASC
	`
	rows, err := r.db.QueryContext(ctx, query, status)
	if err != nil {
		return nil, fmt.Errorf("platformadmin: list access requests: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	var items []AccessRequestItem
	for rows.Next() {
		var it AccessRequestItem
		if err := rows.Scan(&it.ID, &it.UserID, &it.Email, &it.Origin, &it.Status, &it.CreatedAt); err != nil {
			return nil, fmt.Errorf("platformadmin: scan access request: %w", err)
		}
		// C-05 (lado servidor): sin lectura de systems en identity, no hay nada que devolver --
		// SystemsKnown=false se lo dice a la consola explícitamente en vez de dejarla adivinar
		// sobre un arreglo vacío.
		it.Systems = []string{}
		it.SystemsKnown = false
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platformadmin: iterate access requests: %w", err)
	}
	if items == nil {
		items = []AccessRequestItem{}
	}
	return items, nil
}

// CreateAccessRequest implementa AccessRequestStore.CreateAccessRequest. Valida ANTES de tocar la
// base (ErrInvalidInput sin consulta) y escribe con INSERT … ON CONFLICT (user_id) WHERE status =
// 'pending' DO NOTHING: la idempotencia la da el índice único parcial access_requests_one_pending.
// Un fallo de la sentencia se devuelve envuelto ("platformadmin: create access request: …").
func (r *Repository) CreateAccessRequest(ctx context.Context, userID, email, origin string) error {
	if userID == "" || email == "" || (origin != "bff" && origin != "edge") {
		return ErrInvalidInput
	}

	query := `
		INSERT INTO public.access_requests (user_id, email, origin, status)
		VALUES ($1, $2, $3, 'pending')
		ON CONFLICT (user_id) WHERE status = 'pending' DO NOTHING
	`
	_, err := r.db.ExecContext(ctx, query, userID, email, origin)
	if err != nil {
		return fmt.Errorf("platformadmin: create access request: %w", err)
	}
	return nil
}

// RejectAccessRequest implementa AccessRequestStore.RejectAccessRequest. Valida ANTES de tocar la
// base (requestID vacío o motivo en blanco ⇒ ErrInvalidInput sin consulta). Escribe con un UPDATE
// … WHERE id = $3 AND status = 'pending'; si no toca ninguna fila, una segunda consulta distingue
// (M-06) «no existe» (ErrNotFound) de «ya resuelta» (ErrConflict), y un fallo de ESA consulta se
// devuelve envuelto ("platformadmin: check access request existence: …"), NUNCA como
// ErrNotFound: un corte de conexión no es «otro ya la resolvió». decided_by es el operador si es
// un UUID y NULL si no.
//
// M-02: el motivo es OBLIGATORIO (criterio (5) de T3.4 y el de T3.6). Antes solo lo garantizaba
// el `required` del HTML, que cualquier `curl` se salta.
func (r *Repository) RejectAccessRequest(ctx context.Context, requestID, reason, operatorID string) error {
	if requestID == "" {
		return ErrInvalidInput
	}
	if strings.TrimSpace(reason) == "" {
		return ErrInvalidInput
	}

	res, err := r.db.ExecContext(ctx, `
		UPDATE public.access_requests
		SET status = 'rejected', reason = $1, decided_by = $2, decided_at = now()
		WHERE id = $3 AND status = 'pending'
	`, reason, operatorArg(operatorID), requestID)
	if err != nil {
		return fmt.Errorf("platformadmin: reject access request: %w", err)
	}
	rowsAff, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("platformadmin: rows affected: %w", err)
	}
	if rowsAff == 0 {
		// M-06: distinguir "no existe" de cualquier otro fallo al comprobarlo. Antes, un corte de
		// conexión aquí se traducía en el mismo 404 que un id inexistente, y el operador asumía
		// "otro ya la resolvió" cuando en realidad la comprobación ni siquiera llegó a correr.
		var exists bool
		qErr := r.db.QueryRowContext(ctx, `SELECT true FROM public.access_requests WHERE id = $1`, requestID).Scan(&exists)
		switch {
		case errors.Is(qErr, sql.ErrNoRows):
			return ErrNotFound
		case qErr != nil:
			return fmt.Errorf("platformadmin: check access request existence: %w", qErr)
		}
		return ErrConflict
	}
	return nil
}

// LookupAccessRequestStatus implementa AccessRequestStore.LookupAccessRequestStatus: un SELECT
// de user_id y status por id. Sin fila ⇒ ErrNotFound; otro fallo, envuelto ("platformadmin: read
// access request: …").
//
// ⚠️ NO comprueba la membresía cruzada con otra empresa (M-04): esa comprobación vive DENTRO de
// la transacción de ExecuteApprovalTx (GrantTenantAccess), contable en vez de leer una fila
// arbitraria de una PK compuesta con N filas por usuario.
func (r *Repository) LookupAccessRequestStatus(ctx context.Context, requestID string) (userID, status string, err error) {
	err = r.db.QueryRowContext(ctx, `
		SELECT user_id::text, status
		FROM public.access_requests
		WHERE id = $1
	`, requestID).Scan(&userID, &status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", ErrNotFound
	case err != nil:
		return "", "", fmt.Errorf("platformadmin: read access request: %w", err)
	}
	return userID, status, nil
}

// ResolveRoleID implementa AccessRequestStore.ResolveRoleID: SELECT id FROM iam_roles WHERE name
// = $1 OR id::text = $1. Sin fila ⇒ ErrInvalidInput; otro fallo, envuelto ("platformadmin:
// resolve role: …").
func (r *Repository) ResolveRoleID(ctx context.Context, role string) (string, error) {
	var roleID string
	err := r.db.QueryRowContext(ctx, `
		SELECT id::text
		FROM public.iam_roles
		WHERE name = $1 OR id::text = $1
	`, role).Scan(&roleID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidInput
	} else if err != nil {
		return "", fmt.Errorf("platformadmin: resolve role: %w", err)
	}
	return roleID, nil
}

// CheckRetryApproved implementa AccessRequestStore.CheckRetryApproved con dos SELECT EXISTS, EN
// ESTE ORDEN: la membresía en tenant_members (no ⇒ ErrConflict, sin mirar el rol) y el rol en
// iam_user_roles de ESA empresa (no ⇒ ErrRetryRoleMismatch). Un fallo de cualquiera de las dos se
// devuelve envuelto ("platformadmin: check retry membership|role: …").
//
// Las DOS condiciones son necesarias (Tanda 6 · 1.2): sin la membresía, 'approved' apunta a otra
// empresa (o el commit local nunca llegó) y no hay nada que converger; sin la del rol, saltar
// ExecuteApprovalTx en el reintento también saltaba la validación del rol, y un rol distinto
// convergía con 204 sin cambiar nada.
func (r *Repository) CheckRetryApproved(ctx context.Context, userID, tenantID, roleID string) error {
	var exists bool
	err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM public.tenant_members
			WHERE user_id = $1 AND tenant_id = $2
		)
	`, userID, tenantID).Scan(&exists)
	if err != nil {
		return fmt.Errorf("platformadmin: check retry membership: %w", err)
	}
	if !exists {
		return ErrConflict
	}

	var roleMatches bool
	err = r.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM public.iam_user_roles
			WHERE user_id = $1 AND tenant_id = $2 AND role_id = $3
		)
	`, userID, tenantID, roleID).Scan(&roleMatches)
	if err != nil {
		return fmt.Errorf("platformadmin: check retry role: %w", err)
	}
	if !roleMatches {
		return ErrRetryRoleMismatch
	}
	return nil
}

// ExecuteApprovalTx implementa AccessRequestStore.ExecuteApprovalTx en UNA transacción, en este
// orden:
//  1. iampostgres.GrantTenantAccess(ctx, tx, features, userID, tenantID, &roleID): cerrojo de la
//     persona, guarda de una sola empresa con multi_empresa (features del constructor), alta en
//     tenant_members y rol en iam_user_roles. Recibe la TRANSACCIÓN y no el pool: si commiteara
//     por su cuenta, una aprobación podría dar el acceso y dejar la solicitud en 'pending'. Su
//     rechazo de «una sola empresa» (domain.ErrConflict de iam) sale como ErrConflict de este
//     paquete; cualquier otro error, tal cual.
//  2. UPDATE access_requests SET status = 'approved', decided_by, decided_at = now() WHERE id = $2
//     AND status = 'pending'. Un fallo ⇒ envuelto ("platformadmin: update access request status:
//     …"); 0 filas (o no poder contarlas) ⇒ ErrConflict.
//  3. COMMIT; un fallo ⇒ envuelto ("platformadmin: commit tx: …").
//
// Cualquier error antes del COMMIT deshace TODO (ROLLBACK): ni membresía, ni rol, ni status (R-A7).
// No poder abrir la transacción ⇒ envuelto ("platformadmin: begin tx: …"). decided_by es el
// operador si es un UUID y NULL si no.
func (r *Repository) ExecuteApprovalTx(ctx context.Context, requestID, tenantID, userID, roleID, operatorID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("platformadmin: begin tx: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			_ = rerr
		}
	}()

	// 🔴 LOS TRES PRIMEROS PASOS DE ESTA TX NO VIVEN AQUÍ (Plan 047 · Ola 1.0 · T1.0-2, REQ-17).
	// La guarda de membresía cruzada (M-04), el INSERT en tenant_members y la asignación del rol
	// son «dar acceso a una empresa», y eso es exactamente lo que hace la vía del plano de
	// administración del tenant: el caso de uso común es iampostgres.GrantTenantAccess, y
	// public.tenant_members tiene UN solo escritor en todo el código (candado estructural en
	// iam/infra/postgres/single_membership_writer_ast_test.go).
	//
	// Lo que NO se comparte es el paso de abajo —marcar la solicitud como 'approved'—, que es de
	// ESTA bandeja y de ninguna otra. Por eso GrantTenantAccess recibe la transacción en vez de
	// abrir la suya.
	if err := iampostgres.GrantTenantAccess(ctx, tx, r.features, userID, tenantID, &roleID); err != nil {
		// El conflicto de «una sola empresa» sale como domain.ErrConflict y esta bandeja lo
		// expresa con SU centinela, que es el que sus handlers traducen.
		if errors.Is(err, domain.ErrConflict) {
			return ErrConflict
		}
		return err
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE public.access_requests
		SET status = 'approved', decided_by = $1, decided_at = now()
		WHERE id = $2 AND status = 'pending'
	`, operatorArg(operatorID), requestID)
	if err != nil {
		return fmt.Errorf("platformadmin: update access request status: %w", err)
	}
	rowsAff, err := res.RowsAffected()
	if err != nil || rowsAff == 0 {
		return ErrConflict
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("platformadmin: commit tx: %w", err)
	}
	return nil
}

// operatorArg es el valor de decided_by: el operador como UUID, o NULL si no lo es (un sujeto que
// no es un UUID no cabe en la columna). En el viejo, el mismo bloque copiado en la aprobación y en
// el rechazo.
func operatorArg(operatorID string) any {
	if opUUID, err := uuid.Parse(operatorID); err == nil {
		return opUUID
	}
	return nil
}
