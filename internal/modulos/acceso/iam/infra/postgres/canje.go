// Porta internal/iam/infra/postgres/canje.go @ 9a77307

package iampostgres

// canje.go — LOS CUATRO PASOS DEL CANJE, EN UNA SOLA TRANSACCIÓN (Plan 047 · Ola A · T-A3 + T-A4
// + T-A5). Conserva el nombre del fichero viejo (T-15): lo buscan por nombre los candados
// redeem_order y redeem_single_query.
//
// Las tres tareas comparten fichero porque comparten TRANSACCIÓN, y no por conveniencia de quien
// las escribió: si el canje diera el acceso y no marcara la invitación, ese token seguiría
// abriendo la puerta; si la marcara y no diera el acceso, la persona se quedaría sin empresa y
// sin token con el que volver a intentarlo; y si diera el acceso sin cerrar la solicitud, el
// operador de plataforma seguiría viendo en SU bandeja a alguien que ya está dentro. Todo o nada.
//
// ------------------------------------------------------------
// EL ORDEN DE LOS CUATRO PASOS, QUE ES LA MITAD DEL DISEÑO
// ------------------------------------------------------------
//  1. LEER la invitación por su digest — UNA sola consulta (ver readInvitation).
//  2. GrantTenantAccess — la guarda de la segunda empresa, la membresía y el rol.
//  3. MARCAR la invitación como canjeada, con UPDATE condicionado.
//  4. CERRAR la solicitud huérfana que el invitado dejó en la bandeja del operador al
//     registrarse.
//
// 🔴 EL 2 VA ANTES QUE EL 3, Y NO ES INDIFERENTE (T-A5). Quien ya es miembro de otra empresa
// puede o no puede canjear —lo decide el entitlement multi_empresa del tenant que invitó—, y
// cuando NO puede, GrantTenantAccess devuelve domain.ErrConflict antes de insertar nada. Si se
// marcara primero, ese rechazo dejaría la invitación QUEMADA —terminal, sin membresía detrás y
// sin forma de reemitirla para la misma persona salvo pidiéndole a la dueña otra—. Con este orden,
// un canje rechazado deja la invitación EXACTAMENTE como estaba. La transacción hace que el
// rollback lo garantice, pero el orden hace que ni siquiera dependa del rollback.
//
// ------------------------------------------------------------
// DÓNDE VIVE EL «UN SOLO USO»
// ------------------------------------------------------------
// En el UPDATE del paso 3, condicionado y contando filas afectadas — NO en el SELECT del paso 1,
// y NO en un CHECK de la tabla (la migración 0085 lo explica: dos transacciones simultáneas
// pasarían las dos el CHECK y una perdería igual). Dos canjes a la vez del mismo token: los dos
// leen «pendiente», los dos llaman a GrantTenantAccess, y en el UPDATE el segundo espera al
// primero, reevalúa su WHERE bajo READ COMMITTED, ve `redeemed_at` ya escrito, afecta CERO filas
// y se va con conflicto. Sin `FOR UPDATE` y sin lock explícito.
//
// 🔒 LA VENTANA DE CARRERA QUE ESTE CANJE HEREDABA: CERRADA (Plan 047 · Ola 5 · T5.2).
// GrantTenantAccess toma un pg_advisory_xact_lock sobre el user_id antes de contar, y ese cerrojo
// lo hereda este canje porque la transacción es la SUYA y el lock vive en ella.
//
// ⚠️ Por eso el paso (2) tiene que seguir recibiendo `tx` y nunca `r.db`: un advisory lock «xact»
// sobre una conexión en autocommit se suelta en el acto. (El viejo decía que lo vigila
// canje_orden_ast_test.go; ese candado —y su porta redeem_order— vigila el ORDEN, no el
// argumento: hallazgo de F2-03.)

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// InvitationRedeemRepo implementa out.InvitationRedeemRepo sobre public.tenant_invitations
// (migración 0085), public.tenant_members (0037) y public.access_requests (0060).
type InvitationRedeemRepo struct {
	db *sql.DB
	// features viaja hasta GrantTenantAccess y solo hasta ahí: el canje no consulta ningún
	// derecho por su cuenta. Es el resolver que decide si quien ya es miembro de otra empresa
	// puede entrar en ésta (multi_empresa).
	features FeatureResolver
}

// NewInvitationRedeemRepo construye el repositorio sobre el pool dado. No toca el pool.
//
// `features` es obligatorio por la misma razón que en NewMembershipRepo: canjear una invitación
// DA DE ALTA, y el desenlace de un alta depende del entitlement multi_empresa del tenant que
// invitó. Un nil aquí no desactiva el gate: lo deja contestando que no (fail-closed).
func NewInvitationRedeemRepo(db *sql.DB, features FeatureResolver) *InvitationRedeemRepo {
	return &InvitationRedeemRepo{db: db, features: features}
}

var _ out.InvitationRedeemRepo = (*InvitationRedeemRepo)(nil)

// Redeem implementa out.InvitationRedeemRepo (R-P5…R-P8): canjea la invitación del digest
// tokenHash para userID, con los cuatro pasos en UNA transacción —todo o nada—:
//
//  1. LEER la invitación por su digest en UNA sola consulta, que trae también el `now()` de la
//     base (R-P6): «no existe» y «caducada» cuestan lo mismo, y la caducidad se mide con el
//     reloj que escribió `expires_at`, no con el del proceso. El veredicto lo da
//     domain.EvaluateRedemption: inexistente → domain.ErrNotFound; caducada →
//     domain.ErrInvitationExpired; canjeada o revocada → error que envuelve domain.ErrConflict
//     («la invitación ya no se puede usar»). En los tres casos no se escribe nada.
//  2. DAR EL ACCESO con GrantTenantAccess —membresía y, si la invitación trae rol, ese rol— en
//     el tenant DE LA INVITACIÓN (ni del cuerpo de la petición ni del token de quien canjea),
//     dentro de la transacción. Si ya es miembro de otra empresa y el tenant no tiene
//     multi_empresa → ErrConflict y la invitación queda EXACTAMENTE como estaba: viva y usable.
//  3. MARCARLA canjeada (redeemed_at, redeemed_by) con un UPDATE condicionado a
//     `redeemed_at IS NULL AND revoked_at IS NULL` (R-P8): ahí vive el «un solo uso» y la
//     carrera con la revocación. Cero filas → ErrConflict y el rollback deshace el paso 2.
//     🔴 Va DESPUÉS del paso 2 (R-P5, candado redeem_order).
//  4. CERRAR la solicitud de acceso `pending` que el invitado dejó al registrarse ('approved',
//     decided_at = now(), decided_by NULL). Que no haya ninguna no es un fallo.
//
// No abrir la transacción → "iam: abrir tx de canje de invitación: …"; no confirmarla → "iam:
// confirmar el canje de la invitación: …".
func (r *InvitationRedeemRepo) Redeem(ctx context.Context, tokenHash []byte, userID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("iam: abrir tx de canje de invitación: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			_ = rerr
		}
	}()

	// (1) LEER. `now` (era ahora) viene de la MISMA consulta y por tanto del MISMO reloj que
	// escribió `expires_at`; ver readInvitation.
	inv, now, err := readInvitation(ctx, tx, tokenHash)
	if err != nil {
		return err
	}

	// El veredicto lo da una función PURA del dominio, que no puede consultar nada: es la mitad
	// estructural del anti-oráculo (domain/canje.go). Los tres desenlaces de rechazo salen de
	// aquí sin tocar la base una segunda vez.
	switch domain.EvaluateRedemption(inv, now) {
	case domain.RedemptionMissing:
		return domain.ErrNotFound
	case domain.RedemptionExpired:
		return domain.ErrInvitationExpired
	case domain.RedemptionConsumed:
		return fmt.Errorf("%w: la invitación ya no se puede usar", domain.ErrConflict)
	case domain.RedemptionProceeds:
		// sigue abajo
	}

	// (2) DAR EL ACCESO — ANTES de marcar nada. La empresa sale de la FILA (inv.TenantID), que
	// es la que eligió quien emitió la invitación: ni del cuerpo de la petición ni del token de
	// quien canjea, que no trae ninguna.
	//
	// 🔴 SE PASA POR GrantTenantAccess Y NO SE INSERTA AQUÍ. public.tenant_members tiene UN SOLO
	// escritor y lo vigila un candado sobre el AST (single_membership_writer_ast_test.go): un
	// INSERT propio en este fichero pondría ese test en rojo, y con razón — se saltaría la guarda
	// de «una sola empresa» y dejaría a esa persona con dos membresías sin que nadie lo hubiera
	// decidido. El alta en una segunda empresa tiene que ser una decisión y no un efecto
	// colateral de un canje.
	if err := GrantTenantAccess(ctx, tx, r.features, userID, inv.TenantID, inv.RoleID); err != nil {
		return err
	}

	// (3) MARCARLA CANJEADA, condicionado y contando filas: aquí vive el «un solo uso».
	// `revoked_at IS NULL` está en el WHERE aunque el paso (1) ya haya descartado las revocadas,
	// y la redundancia es deliberada: entre la lectura y esta escritura cabe una revocación de la
	// dueña, y sin esta condición el canje pisaría su decisión.
	res, err := tx.ExecContext(ctx, `
		UPDATE public.tenant_invitations
		SET redeemed_at = now(), redeemed_by = $1
		WHERE token_hash = $2 AND redeemed_at IS NULL AND revoked_at IS NULL
	`, userID, tokenHash)
	if err != nil {
		return fmt.Errorf("iam: marcar la invitación canjeada: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("iam: filas afectadas al marcar la invitación: %w", err)
	}
	if affected == 0 {
		// Alguien ganó la carrera entre el paso (1) y este, o la dueña la revocó entre medias.
		// Mismo desenlace que si hubiera llegado terminal: conflicto, y el rollback deshace la
		// membresía que el paso (2) acababa de escribir.
		return fmt.Errorf("%w: la invitación ya no se puede usar", domain.ErrConflict)
	}

	// (4) CERRAR LA SOLICITUD HUÉRFANA (T-A4).
	if err := closeAccessRequest(ctx, tx, userID); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("iam: confirmar el canje de la invitación: %w", err)
	}
	return nil
}

// invitationRow es la fila que lee readInvitation, tal como llega de la base: las cuatro columnas
// NULLables (role_id, redeemed_by, redeemed_at, revoked_at) en sus tipos Null*. No trae token_hash,
// created_by ni created_at: el veredicto del canje no los mira.
type invitationRow struct {
	ID         string
	TenantID   string
	RoleID     sql.NullString
	ExpiresAt  time.Time
	RedeemedBy sql.NullString
	RedeemedAt sql.NullTime
	RevokedAt  sql.NullTime
}

// invitationFromRow (la filaAInvitacion de diseño F2 R-P7) traslada la fila leída a la entidad
// que clasifica domain.EvaluateRedemption: cada NULLable es nil si es NULL y apunta a su valor si
// no.
//
// 🔴 POR QUÉ ES UNA FUNCIÓN APARTE, CON SU TEST. El canje rechaza una invitación revocada por DOS
// guardas independientes: la clasificación (que mira inv.RevokedAt) y el `revoked_at IS NULL` del
// UPDATE. Se midió contra Postgres (2026-08-28): NO trasladar revoked_at aquí deja la entidad
// diciendo «no revocada» sobre una fila que SÍ lo está, la clasificación da vía libre y la única
// protección que queda es el UPDATE — y desde fuera no se ve NADA (mismo ErrConflict, mismo
// rollback). El viejo lo vigilaba con un candado AST; aquí lo afirma un test de conducta sobre
// esta función pura (D-F2-1). Las otras tres van con ella: `redeemed_at` decide el «ya se usó» y
// `role_id` es el rol que la persona recibe al entrar.
func invitationFromRow(row invitationRow) domain.Invitation {
	return domain.Invitation{
		ID:         row.ID,
		TenantID:   row.TenantID,
		RoleID:     strPtr(row.RoleID),
		ExpiresAt:  row.ExpiresAt,
		RedeemedBy: strPtr(row.RedeemedBy),
		RedeemedAt: timePtr(row.RedeemedAt),
		RevokedAt:  timePtr(row.RevokedAt),
	}
}

// readInvitation (era leerInvitacion) trae la fila del digest y, EN LA MISMA CONSULTA, el
// instante del servidor.
//
// 🔴 UNA SOLA CONSULTA, Y ES UN REQUISITO, NO UNA OPTIMIZACIÓN. Es lo que hace que «no existe» y
// «caducada» cuesten lo mismo: la ausencia no dispara una segunda pregunta a la base ni ninguna
// otra rama con E/S — se convierte en un puntero nil y sigue por el mismo código que la
// presencia. Un candado sobre el AST cuenta sus consultas para que nadie añada la segunda sin
// enterarse (redeem_single_query_ast_test.go).
//
// 🔴 Y `now()` VIENE DE POSTGRES, NO DE time.Now(). `expires_at` lo escribió el reloj del
// servidor de base de datos; compararlo con el reloj del proceso Go sería comparar DOS relojes, y
// su deriva no da un fallo ruidoso: da invitaciones que caducan un poco antes o un poco después de
// lo que dice su propia fila, para siempre y sin que nada lo señale.
//
// Devuelve (nil, cero, nil) cuando no hay fila: la ausencia NO es un error de infraestructura, es
// uno de los cuatro veredictos posibles y lo clasifica domain.EvaluateRedemption. El instante cero
// que la acompaña es intrascendente — la rama de la ausencia no lo mira.
func readInvitation(ctx context.Context, q Executor, tokenHash []byte) (*domain.Invitation, time.Time, error) {
	var (
		row invitationRow
		now time.Time
	)
	err := q.QueryRowContext(ctx, `
		SELECT id::text, tenant_id::text, role_id::text, expires_at,
		       redeemed_by::text, redeemed_at, revoked_at, now()
		FROM public.tenant_invitations
		WHERE token_hash = $1
	`, tokenHash).Scan(
		&row.ID, &row.TenantID, &row.RoleID, &row.ExpiresAt,
		&row.RedeemedBy, &row.RedeemedAt, &row.RevokedAt, &now,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, time.Time{}, nil
	case err != nil:
		return nil, time.Time{}, fmt.Errorf("iam: leer la invitación por su digest: %w", err)
	}

	inv := invitationFromRow(row)
	return &inv, now, nil
}

// closeAccessRequest (era cerrarSolicitudDeAcceso) resuelve la fila `pending` que el invitado dejó
// en public.access_requests al registrarse por el signup público (T-A4).
//
// POR QUÉ ESTE PASO EXISTE. El invitado no llega a wApp por la puerta del operador: se registra él
// mismo (el signup de platformadmin), y ese registro deja SIEMPRE una solicitud 'pending' que
// aterriza en la bandeja del OPERADOR DE PLATAFORMA. Si el canje no la tocara, cada persona
// incorporada por su dueña dejaría una solicitud eterna pidiéndonos a nosotros un acceso que ya
// tiene.
//
// POR QUÉ LO HACE EL LLAMANTE Y NO GrantTenantAccess. Porque marcar la solicitud es del flujo de
// esa bandeja y de ninguna otra: está escrito en la cabecera de GrantTenantAccess y es la razón de
// que aquella función reciba la transacción en vez de abrir la suya. El operador hace exactamente
// esto mismo en su cuarto paso (platformadmin.executeApprovalTx); aquí es el mismo trato.
//
// 'approved' Y NO 'rejected': el estado terminal describe cómo acabó la solicitud, y acabó con esa
// persona DENTRO de una empresa. 'rejected' diría que se le negó el acceso, que es exactamente lo
// contrario de lo que pasó.
//
// `decided_by` se queda NULL a propósito, y es la única diferencia con el cuarto paso del
// operador: ahí hay un operador que decidió y aquí no lo hay. La columna es NULLable y su ausencia
// es el dato: esta solicitud se resolvió sin pasar por la bandeja. La fecha sí se pone:
// `decided_at` es CUÁNDO dejó de estar pendiente.
//
// CERO FILAS AFECTADAS NO ES UN ERROR, y por eso no se cuentan. Puede no haber solicitud
// pendiente: alguien a quien el operador ya atendió, alguien que llegó por otra vía, o una segunda
// invitación para una persona cuya solicitud se cerró en la primera. El estado que se pedía —«esta
// persona no aparece en la bandeja»— ya se cumple. Mismo criterio que MembershipRepo.Remove.
//
// El índice único PARCIAL sobre (user_id) WHERE status='pending' (0060:132-134) garantiza que este
// UPDATE toca UNA fila como mucho, así que no hace falta acotarlo ni ordenarlo.
func closeAccessRequest(ctx context.Context, exec Executor, userID string) error {
	if _, err := exec.ExecContext(ctx, `
		UPDATE public.access_requests
		SET status = 'approved', decided_at = now()
		WHERE user_id = $1 AND status = 'pending'
	`, userID); err != nil {
		return fmt.Errorf("iam: cerrar la solicitud de acceso del invitado: %w", err)
	}
	return nil
}
