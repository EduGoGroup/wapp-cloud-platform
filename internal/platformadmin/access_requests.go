package platformadmin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/google/uuid"
)

// systemWappPlatform es el namespace de la consola de plataforma
// (== usecase.SystemWappPlatform, internal/iam/usecase/exchange.go:28). Se
// re-declara aquí en vez de importar el paquete usecase -- que arrastra el
// canje de tokens completo -- porque lo único que hace falta es el valor de
// catálogo, no el comportamiento.
const systemWappPlatform = "wapp.platform"

var (
	// ErrPlatformSystemForbidden se devuelve cuando la aprobación intenta
	// conceder wapp.platform desde la bandeja de solicitudes de acceso.
	// Decisión de Jhoan (2026-08-15, Plan 056 Tanda 2): la consola de
	// plataforma NO se concede por esta vía; el servidor es el SEGUNDO
	// cerrojo -- la consola quita la casilla, pero esto no se fía del cliente.
	ErrPlatformSystemForbidden = errors.New("platformadmin: wapp.platform no se concede desde la bandeja de solicitudes de acceso")

	// ErrSystemsUnionUnavailable se devuelve cuando la aprobación tendría que
	// UNIR los systems nuevos con los que el usuario YA tiene, pero identity
	// no expone ninguna lectura puntual de "qué systems tiene hoy" (C-05,
	// lado servidor: identity-core solo ofrece PUT/DELETE declarativos sobre
	// /users/{id}/systems y un POST /users/systems/reconcile en LOTE pensado
	// para el actor "ecosistema completo", no para esta bandeja). Sin esa
	// lectura, llamar a ReplaceUserSystems sobre una cuenta que YA pasó antes
	// por esta bandeja arriesgaría REEMPLAZAR -- no sumar -- su conjunto real
	// y borrarle acceso que nadie pidió tocar. Se prefiere fallar alto: lo
	// local (tenant + rol) queda escrito igual, los systems de identity NO se
	// tocan.
	ErrSystemsUnionUnavailable = errors.New("platformadmin: no se puede unir con los systems actuales del usuario en identity (sin lectura)")

	// ErrIdentityM2MUnavailable se devuelve cuando la aprobación traía systems
	// que conceder pero NO hay cliente M2M configurado hacia identity (hoy:
	// WAPP_IDENTITY_API_KEY ausente, T0.2 pendiente). Antes de este arreglo
	// (Tanda 6 · 1.1), `m2m == nil` compartía la misma rama de salida
	// silenciosa que `len(systems) == 0` -- un caso legítimo (no había nada
	// que conceder) tapaba uno que NO lo es (había algo que conceder y no se
	// pudo). Lo local (tenant + rol) queda escrito igual; los systems de
	// identity NO se tocan porque no hay con qué.
	ErrIdentityM2MUnavailable = errors.New("platformadmin: no hay cliente M2M configurado hacia identity; no se pudieron conceder los systems solicitados")

	// ErrRetryRoleMismatch se devuelve cuando un reintento sobre una solicitud
	// YA 'approved' pide un ROL distinto del que quedó escrito en la primera
	// pasada (Tanda 6 · 1.2). Saltar executeApprovalTx
	// en el reintento significa saltar también resolveRoleID: sin esta
	// comprobación, un rol distinto -- incluso uno que no existe -- convergía
	// en silencio con 204 sin cambiar el rol, y de paso disparaba
	// ReplaceUserSystems con los systems de ESTA llamada, reemplazando lo que
	// hubiera antes. Converger significa reproducir el MISMO estado, no
	// aplicar en silencio lo que pida el segundo clic.
	ErrRetryRoleMismatch = errors.New("platformadmin: el reintento pide un rol distinto del ya aprobado la primera vez; no converge")

	// ErrTenantNotFound se devuelve cuando el tenant_id de la aprobación es
	// sintácticamente válido pero no existe (Tanda 6 · P3). Antes reventaba
	// como 500 al violar la FK tenant_members.tenant_id -> tenants(id) DENTRO
	// de executeApprovalTx; se comprueba ANTES de abrir la tx para devolver
	// un 404 legible en vez de un error de Postgres crudo.
	ErrTenantNotFound = errors.New("platformadmin: el tenant_id de la aprobación no existe")

	// ErrSystemsSyncFailed envuelve CUALQUIER fallo de ReplaceUserSystems
	// tras un commit local exitoso: identity caído, rate-limit, credencial de
	// máquina inválida, etc. Todos comparten el mismo problema de fondo (C-04)
	// -- lo local ya quedó escrito y hace falta poder reintentar sin duplicar
	// filas -- así que el handler los trata a todos igual: 502 con el cuerpo
	// que le dice a la consola "local: ok, identity: failed".
	ErrSystemsSyncFailed = errors.New("platformadmin: fallo al sincronizar systems en identity tras aprobar localmente")
)

// AccessRequestItem representa una solicitud en la bandeja de acceso.
type AccessRequestItem struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Origin    string    `json:"origin"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	// Systems son los systems que el usuario YA tiene hoy (C-05). Nunca null:
	// arreglo vacío tanto si de verdad no tiene ninguno como si no se pudo
	// averiguar -- el segundo caso lo distingue SystemsKnown.
	Systems []string `json:"systems"`
	// SystemsKnown es false mientras identity-core no exponga una lectura
	// puntual de los systems actuales de un usuario (ver ErrSystemsUnionUnavailable
	// arriba). La consola NO debe leer Systems==[] como "no tiene nada" cuando
	// esto es false: es "no lo sabemos", y precargar casillas sobre esa base
	// sería la misma mitigación-de-mentira que D-056.7 vino a cerrar.
	SystemsKnown bool `json:"systems_known"`
}

// ApprovePartialResult es el cuerpo JSON de una aprobación cuya mitad LOCAL
// (tenant + rol) quedó escrita pero la sincronización de systems en identity
// no se hizo -- porque falló (identity="failed") o porque se saltó a
// propósito por seguridad (identity="skipped", ErrSystemsUnionUnavailable).
// Existe para que la consola pueda decírselo al operador en vez de un texto
// plano indistinguible de cualquier otro error (C-04).
type ApprovePartialResult struct {
	Local    string `json:"local"`
	Identity string `json:"identity"`
	Reason   string `json:"reason"`
}

// ListAccessRequestsResponse es el cuerpo JSON para GET /admin/access-requests.
type ListAccessRequestsResponse struct {
	Items []AccessRequestItem `json:"items"`
}

// ApproveAccessRequestRequest es el cuerpo JSON para POST /admin/access-requests/{id}/approve.
type ApproveAccessRequestRequest struct {
	TenantID string   `json:"tenant_id"`
	Role     string   `json:"role"`
	Systems  []string `json:"systems"`
}

// RejectAccessRequestRequest es el cuerpo JSON para POST /admin/access-requests/{id}/reject.
type RejectAccessRequestRequest struct {
	Reason string `json:"reason"`
}

// ListAccessRequests consulta las solicitudes de acceso filtradas por estado.
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
		// C-05 (lado servidor): sin lectura de systems en identity, no hay
		// nada que devolver -- SystemsKnown=false se lo dice a la consola
		// explícitamente en vez de dejarla adivinar sobre un arreglo vacío.
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

// CreateAccessRequest siembra una solicitud de acceso en estado pending.
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

// lookupAccessRequestStatus resuelve el usuario y estado actual de una
// solicitud por su id. ErrNotFound si no existe.
//
// ⚠️ Ya NO comprueba aquí la membresía cruzada con otro tenant (M-04): esa
// comprobación se movió DENTRO de la tx de executeApprovalTx, contable en vez
// de leer una fila arbitraria de una PK compuesta con N filas por usuario.
func (r *Repository) lookupAccessRequestStatus(ctx context.Context, requestID string) (userID, status string, err error) {
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

// checkRetryApproved decide si una solicitud YA aprobada puede reintentarse
// DE VERDAD -- converger, no aplicar en silencio lo que pida el segundo clic
// (Tanda 6 · 1.2). Dos condiciones, las DOS necesarias:
//
//  1. El usuario está efectivamente en tenant_members para ESE tenant -- si
//     no lo está, 'approved' apunta a otro tenant (o el commit local nunca
//     llegó a pasar) y no hay nada que converger: conflicto real. Esto ya
//     lo comprobaba el criterio (4) de T3.4.
//  2. roleID -- el id YA RESUELTO del rol que se pide AHORA -- coincide con
//     el rol que de verdad quedó escrito en iam_user_roles la primera vez.
//     Sin esta comprobación, saltar executeApprovalTx en el reintento
//     también saltaba resolveRoleID: un rol distinto (incluso uno que no
//     existe) convergía con 204 sin cambiar nada, y de paso disparaba
//     ReplaceUserSystems con los systems de ESTA llamada -- el peligro que
//     C-05 vino a cerrar, entrando por la puerta que abrió C-04.
func (r *Repository) checkRetryApproved(ctx context.Context, userID, tenantID, roleID string) error {
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

// resolveRoleID encuentra el id canónico del rol por nombre o id.
func (r *Repository) resolveRoleID(ctx context.Context, role string) (string, error) {
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

// executeApprovalTx ejecuta la transacción local de aprobación.
func (r *Repository) executeApprovalTx(ctx context.Context, requestID, tenantID, userID, roleID, operatorID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("platformadmin: begin tx: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			_ = rerr
		}
	}()

	// 🔴 LOS TRES PRIMEROS PASOS DE ESTA TX YA NO VIVEN AQUÍ (Plan 047 · Ola 1.0 ·
	// T1.0-2, REQ-17). La guarda de membresía cruzada (M-04), el INSERT en
	// tenant_members y la asignación del rol son «dar acceso a una empresa», y eso
	// es exactamente lo que hace la vía nueva del plano de administración del
	// tenant. Compartirlo era lo deseable y se hizo: el caso de uso común es
	// iampostgres.GrantTenantAccess y public.tenant_members tiene UN solo escritor
	// en todo el código (candado estructural en
	// iam/infra/postgres/membresia_unica_ast_test.go).
	//
	// Lo que NO se comparte es el paso de abajo —marcar la solicitud como
	// 'approved'—, que es de ESTA bandeja y de ninguna otra. Por eso
	// GrantTenantAccess recibe la transacción en vez de abrir la suya: si
	// commiteara por su cuenta, una aprobación podría dar el acceso y dejar la
	// solicitud en 'pending'.
	if err := iampostgres.GrantTenantAccess(ctx, tx, r.features, userID, tenantID, &roleID); err != nil {
		// El conflicto de «una sola empresa» sale como domain.ErrConflict y esta
		// bandeja lo expresa con SU sentinel, que es el que sus handlers traducen.
		if errors.Is(err, domain.ErrConflict) {
			return ErrConflict
		}
		return err
	}

	var opArg any
	if opUUID, parseErr := uuid.Parse(operatorID); parseErr == nil {
		opArg = opUUID
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE public.access_requests
		SET status = 'approved', decided_by = $1, decided_at = now()
		WHERE id = $2 AND status = 'pending'
	`, opArg, requestID)
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

// ApproveAccessRequest aprueba una solicitud asignando empresa, rol y actualizando sistemas en identity.
//
// El reintento de una aprobación que falló al sincronizar systems CONVERGE
// (C-04): si la solicitud ya está 'approved' hacia el MISMO tenant Y con el
// MISMO rol (checkRetryApproved, Tanda 6 · 1.2), se salta por completo la
// escritura local -- ya está hecha -- y va directo a reintentar solo la mitad
// que pudo haber fallado. Un reintento que pide un rol distinto NO converge:
// se rechaza (ErrRetryRoleMismatch) en vez de fingir que se aplicó.
func (r *Repository) ApproveAccessRequest(ctx context.Context, requestID, tenantID, role, operatorID string, systems []string, m2m out.IdentityM2MClient) error {
	if requestID == "" || tenantID == "" || role == "" {
		return ErrInvalidInput
	}
	// (C) wapp.platform NO se concede desde la bandeja -- segundo cerrojo,
	// el servidor no se fía de que la consola haya quitado la casilla.
	if slices.Contains(systems, systemWappPlatform) {
		return ErrPlatformSystemForbidden
	}

	userID, status, err := r.lookupAccessRequestStatus(ctx, requestID)
	if err != nil {
		return err
	}

	// El rol se resuelve UNA vez, ANTES de bifurcar por status: tanto el
	// camino 'pending' (executeApprovalTx lo necesita para el INSERT) como el
	// camino 'approved' (checkRetryApproved lo necesita para comparar contra
	// lo ya escrito) lo usan -- resolverlo aquí evita que el reintento se
	// salte la validación por saltarse executeApprovalTx entero (1.2).
	roleID, rerr := r.resolveRoleID(ctx, role)
	if rerr != nil {
		return rerr
	}

	if err := r.resolveApprovalWrite(ctx, status, requestID, tenantID, userID, roleID, operatorID); err != nil {
		return err
	}

	return r.syncApprovedSystems(ctx, userID, requestID, systems, m2m)
}

// resolveApprovalWrite ejecuta -- o converge sobre -- la escritura LOCAL
// (tenant + rol) de la aprobación, según el status con el que llegó la
// solicitud. Extraída de ApproveAccessRequest sin cambiar ni el orden ni las
// comprobaciones: mismo cuerpo, mismo comportamiento.
func (r *Repository) resolveApprovalWrite(ctx context.Context, status, requestID, tenantID, userID, roleID, operatorID string) error {
	switch status {
	case "pending":
		// (P3) Un tenant_id sintácticamente válido pero inexistente violaba
		// la FK tenant_members.tenant_id -> tenants(id) DENTRO de la tx y
		// salía como 500 genérico. Comprobarlo aquí, antes de abrir la tx,
		// lo convierte en un ErrNotFound legible -- no hace falta este
		// chequeo en el camino 'approved': un 'approved' de verdad ya exige
		// que exista una fila en tenant_members para ese tenant_id (esa
		// misma FK, satisfecha la primera vez).
		exists, existsErr := r.ExistsTenant(ctx, tenantID)
		if existsErr != nil {
			return fmt.Errorf("platformadmin: comprobar existencia de tenant: %w", existsErr)
		}
		if !exists {
			return ErrTenantNotFound
		}
		if txErr := r.executeApprovalTx(ctx, requestID, tenantID, userID, roleID, operatorID); txErr != nil {
			return txErr
		}
		return nil
	case "approved":
		if convErr := r.checkRetryApproved(ctx, userID, tenantID, roleID); convErr != nil {
			return convErr
		}
		// Lo local (tenant + rol) ya está escrito de una pasada anterior Y
		// coincide con lo que se pide ahora: NO se vuelve a tocar
		// executeApprovalTx, solo se reintenta systems.
		return nil
	default:
		return ErrConflict
	}
}

// syncApprovedSystems sincroniza en identity los systems pedidos, DESPUÉS de
// que la escritura local ya quedó resuelta. Extraída de ApproveAccessRequest
// sin cambiar ni el orden ni las comprobaciones: mismo cuerpo, mismo
// comportamiento -- incluidos los tres desenlaces documentados en (1.1)/(D).
func (r *Repository) syncApprovedSystems(ctx context.Context, userID, requestID string, systems []string, m2m out.IdentityM2MClient) error {
	// (1.1) len(systems)==0 es un caso LEGÍTIMO -- no había nada que
	// conceder -- y sigue devolviendo 204 sin más. m2m==nil es DISTINTO: SÍ
	// había algo que conceder y no hay con qué. Antes ambos compartían la
	// misma salida silenciosa; con T0.2 pendiente (sin
	// WAPP_IDENTITY_API_KEY en ningún despliegue), esa rama se tomaba en
	// TODAS las aprobaciones con systems, callada.
	if len(systems) == 0 {
		return nil
	}
	if m2m == nil {
		return ErrIdentityM2MUnavailable
	}

	// (D) Unión, no reemplazo: ReplaceUserSystems es declarativo, así que
	// mandarle solo los systems de ESTA solicitud reemplazaría -- no sumaría --
	// el conjunto real de la persona.
	//
	// 🔑 Hasta el 2026-08-28 aquí se APROXIMABA la unión con una señal local
	// («¿le aprobamos algo antes?», hasOtherApprovedRequest) y, si la respuesta
	// era sí, se rehusaba: falso negativo en la primera aprobación y falso
	// positivo permanente desde la segunda. Era un proxy estructuralmente
	// equivocado, y su excusa —«no hay lectura de identity»— CADUCÓ con el Plan
	// 047 · Ola B, que trajo GetUserSystems. Ahora se une de verdad, igual que
	// la vía de la dueña (iam/usecase/memberships.go): leer, unir, declarar.
	vigentes, gerr := m2m.GetUserSystems(ctx, userID)
	if gerr != nil {
		// Sin poder LEER no se puede unir, y un ReplaceUserSystems a ciegas
		// borraría accesos que no son de esta bandeja. Se rehúsa, que es lo
		// mismo que se hacía antes -- pero ahora por un fallo MEDIDO y no por
		// una presunción sobre una tabla local.
		return fmt.Errorf("%w: %w", ErrSystemsUnionUnavailable, gerr)
	}

	// slices.Clone: no se escribe en el arreglo que devolvió el puerto, que no
	// es nuestro. El orden es el que dio identity con los nuevos al final:
	// estable y reproducible, que es lo que permite afirmar sobre el conjunto
	// exacto que viaja por el cable.
	deseados := slices.Clone(vigentes)
	for _, s := range systems {
		if !slices.Contains(deseados, s) {
			deseados = append(deseados, s)
		}
	}
	// Si no hay nada que añadir, no se escribe: identity no tiene por qué
	// recibir un PUT que no cambia nada (mismo criterio que T-B4).
	if len(deseados) == len(vigentes) {
		return nil
	}

	// ⚠️ La unión PRESERVA lo que la persona ya tuviera, incluido wapp.platform.
	// Eso NO contradice ErrPlatformSystemForbidden: esa guarda prohíbe
	// CONCEDERLO desde esta bandeja, y aquí no se concede nada nuevo -- se evita
	// borrar lo que otra vía otorgó. El reemplazo ciego sí lo habría borrado.
	if _, err := m2m.ReplaceUserSystems(ctx, userID, deseados); err != nil {
		return fmt.Errorf("%w: %w", ErrSystemsSyncFailed, err)
	}

	return nil
}

// RejectAccessRequest rechaza una solicitud guardando el motivo y operador.
//
// M-02: el motivo es OBLIGATORIO (criterio (5) de T3.4 y el de T3.6). Antes
// solo lo garantizaba el `required` del HTML, que cualquier `curl` se salta.
func (r *Repository) RejectAccessRequest(ctx context.Context, requestID, reason, operatorID string) error {
	if requestID == "" {
		return ErrInvalidInput
	}
	if strings.TrimSpace(reason) == "" {
		return ErrInvalidInput
	}

	var opArg any
	if opUUID, parseErr := uuid.Parse(operatorID); parseErr == nil {
		opArg = opUUID
	}

	res, err := r.db.ExecContext(ctx, `
		UPDATE public.access_requests
		SET status = 'rejected', reason = $1, decided_by = $2, decided_at = now()
		WHERE id = $3 AND status = 'pending'
	`, reason, opArg, requestID)
	if err != nil {
		return fmt.Errorf("platformadmin: reject access request: %w", err)
	}
	rowsAff, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("platformadmin: rows affected: %w", err)
	}
	if rowsAff == 0 {
		// M-06: distinguir "no existe" de cualquier otro fallo al comprobarlo.
		// Antes, un corte de conexión aquí se traducía en el mismo 404 que un
		// id inexistente, y el operador asumía "otro ya la resolvió" cuando en
		// realidad la comprobación ni siquiera llegó a correr.
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

// accessRequestIDFromPath extrae y valida el {id} de solicitud del path,
// mismo criterio que tenantIDFromPath (handlers.go) para M-03 (Tanda 6 · P3):
// un id vacío es 400 (falta el parámetro); un id que no es UUID es 404, no
// 500 -- sin esto, un `WHERE id = $1` sobre una columna UUID con un valor que
// no codifica revienta con un error de pgx que no es sql.ErrNoRows, y el
// switch de más abajo lo mapeaba al 500 genérico de "err != nil".
func accessRequestIDFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id de solicitud requerido", http.StatusBadRequest)
		return "", false
	}
	if _, err := uuid.Parse(id); err != nil {
		http.Error(w, "solicitud no encontrada", http.StatusNotFound)
		return "", false
	}
	return id, true
}

// ListAccessRequestsHandler devuelve el handler para GET /admin/access-requests.
func ListAccessRequestsHandler(repo *Repository, platformTenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		status := r.URL.Query().Get("status")
		if status == "" {
			status = "pending"
		}

		items, err := repo.ListAccessRequests(r.Context(), status)
		if err != nil {
			http.Error(w, "error al listar solicitudes de acceso", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, ListAccessRequestsResponse{
			Items: items,
		})
	})
}

// ApproveAccessRequestHandler devuelve el handler para POST /admin/access-requests/{id}/approve.
func ApproveAccessRequestHandler(repo *Repository, m2m out.IdentityM2MClient, platformTenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		requestID, ok := accessRequestIDFromPath(w, r)
		if !ok {
			return
		}

		req, ok := decodeApproveAccessRequestBody(w, r)
		if !ok {
			return
		}

		httpapi.SetAuditTargetTenant(r.Context(), req.TenantID)

		var operatorID string
		if id, ok := httpapi.IdentityFromContext(r.Context()); ok {
			operatorID = id.Subject
		}

		err := repo.ApproveAccessRequest(r.Context(), requestID, req.TenantID, req.Role, operatorID, req.Systems, m2m)
		writeApproveAccessRequestResult(w, err)
	})
}

// decodeApproveAccessRequestBody decodifica y valida el cuerpo JSON de
// POST /admin/access-requests/{id}/approve. Si el cuerpo es inválido o le
// faltan tenant_id/role, ya escribió la respuesta de error y devuelve
// ok=false -- mismo criterio y mismos mensajes que antes de extraerla.
func decodeApproveAccessRequestBody(w http.ResponseWriter, r *http.Request) (ApproveAccessRequestRequest, bool) {
	var req ApproveAccessRequestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
		return req, false
	}
	if req.TenantID == "" || req.Role == "" {
		http.Error(w, "tenant_id y role son requeridos", http.StatusBadRequest)
		return req, false
	}
	return req, true
}

// writeApproveAccessRequestResult mapea el resultado de ApproveAccessRequest
// al status y cuerpo HTTP de la respuesta. Mismo switch, mismos casos, mismo
// orden que antes de extraerla -- incluido que ninguno de los errors.Is
// coincide cuando err es nil, así que ese caso cae al 204 explícito de
// abajo, igual que caía al w.WriteHeader posterior al switch original.
func writeApproveAccessRequestResult(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, "solicitud no encontrada", http.StatusNotFound)
		return
	case errors.Is(err, ErrTenantNotFound):
		http.Error(w, "empresa no encontrada", http.StatusNotFound)
		return
	case errors.Is(err, ErrConflict):
		http.Error(w, "la solicitud ya fue resuelta o la persona ya pertenece a otra empresa", http.StatusConflict)
		return
	case errors.Is(err, ErrInvalidInput):
		http.Error(w, "datos de solicitud o rol inválidos", http.StatusBadRequest)
		return
	case errors.Is(err, ErrPlatformSystemForbidden):
		http.Error(w, "wapp.platform no se concede desde la bandeja de solicitudes de acceso", http.StatusBadRequest)
		return
	case errors.Is(err, ErrSystemsUnionUnavailable):
		// (D) Lo local quedó escrito; los systems de identity NO se
		// tocaron porque FALLÓ LA LECTURA de su conjunto actual, y sin
		// leerlo un PUT declarativo borraría lo que otra vía concedió.
		// 409 y no 502 porque hace falta mirar: reintentar a ciegas no
		// lo arregla.
		//
		// 🔧 Antes esta rama saltaba por PRESUNCIÓN («esta cuenta ya
		// pasó por la bandeja»), sin intentar leer. Desde el
		// 2026-08-28 solo salta si la lectura real falló.
		writeJSON(w, http.StatusConflict, ApprovePartialResult{
			Local: "ok", Identity: "skipped",
			Reason: "no se pudo leer el conjunto actual de systems del usuario en identity; para no reemplazarlo por accidente no se tocó nada en identity",
		})
		return
	case errors.Is(err, ErrSystemsSyncFailed):
		// (C-04) Lo local (tenant + rol) quedó escrito; solo falló la
		// sincronización con identity. 502 distinguible para que la
		// consola pueda decírselo al operador y reintentar más tarde.
		writeJSON(w, http.StatusBadGateway, ApprovePartialResult{
			Local: "ok", Identity: "failed", Reason: err.Error(),
		})
		return
	case errors.Is(err, ErrIdentityM2MUnavailable):
		// (1.1) Mismo cuerpo que ErrSystemsSyncFailed y
		// ErrSystemsUnionUnavailable (Local:"ok", Identity:"skipped")
		// -- lo local quedó escrito -- pero 503, no 409/502: no es un
		// conflicto de datos ni un fallo transitorio de red, es que
		// este despliegue no tiene cliente M2M configurado en
		// absoluto (mismo criterio y mismo código que usa
		// SignupHandler para el mismo m2m==nil, signup.go:136-139).
		writeJSON(w, http.StatusServiceUnavailable, ApprovePartialResult{
			Local: "ok", Identity: "skipped",
			Reason: "no hay cliente M2M configurado hacia identity en este despliegue; lo local (empresa y rol) quedó escrito pero los systems solicitados NO se concedieron",
		})
		return
	case errors.Is(err, ErrRetryRoleMismatch):
		// (1.2) El reintento pide un rol distinto del que ya quedó
		// aprobado la primera vez: NO converge, así que no se toca
		// nada (ni el rol local ni los systems de identity). 409:
		// hace falta que el operador reconcilie a mano -- p. ej.
		// rechazando y creando una solicitud nueva, o resolviendo el
		// cambio de rol por otra vía -- esta bandeja no lo hace por
		// su cuenta.
		http.Error(w, "la solicitud ya fue aprobada con un rol distinto; el reintento no converge", http.StatusConflict)
		return
	case err != nil:
		http.Error(w, "error al aprobar solicitud", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// RejectAccessRequestHandler devuelve el handler para POST /admin/access-requests/{id}/reject.
func RejectAccessRequestHandler(repo *Repository, platformTenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		requestID, ok := accessRequestIDFromPath(w, r)
		if !ok {
			return
		}

		var req RejectAccessRequestRequest
		if r.Body != nil && r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
				return
			}
		}

		var operatorID string
		if id, ok := httpapi.IdentityFromContext(r.Context()); ok {
			operatorID = id.Subject
		}

		err := repo.RejectAccessRequest(r.Context(), requestID, req.Reason, operatorID)
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "solicitud no encontrada", http.StatusNotFound)
			return
		case errors.Is(err, ErrConflict):
			http.Error(w, "la solicitud ya fue resuelta", http.StatusConflict)
			return
		case errors.Is(err, ErrInvalidInput):
			http.Error(w, "entrada inválida", http.StatusBadRequest)
			return
		case err != nil:
			http.Error(w, "error al rechazar solicitud", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})
}
