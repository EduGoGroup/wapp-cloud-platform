package iampostgres

// canje.go — LOS CUATRO PASOS DEL CANJE, EN UNA SOLA TRANSACCIÓN
// (Plan 047 · Ola A · T-A3 + T-A4 + T-A5).
//
// Las tres tareas comparten fichero porque comparten TRANSACCIÓN, y no por
// conveniencia de quien las escribió: si el canje diera el acceso y no marcara
// la invitación, ese token seguiría abriendo la puerta; si la marcara y no diera
// el acceso, la persona se quedaría sin empresa y sin token con el que volver a
// intentarlo; y si diera el acceso sin cerrar la solicitud, el operador de
// plataforma seguiría viendo en SU bandeja a alguien que ya está dentro. Todo o
// nada.
//
// ------------------------------------------------------------
// EL ORDEN DE LOS CUATRO PASOS, QUE ES LA MITAD DEL DISEÑO
// ------------------------------------------------------------
//  1. LEER la invitación por su digest — UNA sola consulta (ver leerInvitacion).
//  2. GrantTenantAccess — la guarda de «una sola empresa», la membresía y el rol.
//  3. MARCAR la invitación como canjeada, con UPDATE condicionado.
//  4. CERRAR la solicitud huérfana que el invitado dejó en la bandeja del
//     operador al registrarse.
//
// 🔴 EL 2 VA ANTES QUE EL 3, Y NO ES INDIFERENTE (T-A5). Quien ya es miembro de
// otra empresa no puede canjear: GrantTenantAccess devuelve domain.ErrConflict
// antes de insertar nada. Si se marcara primero, ese rechazo dejaría la
// invitación QUEMADA —terminal, sin membresía detrás y sin forma de reemitirla
// para la misma persona salvo pidiéndole a la dueña otra—. Con este orden, un
// canje rechazado deja la invitación EXACTAMENTE como estaba: sigue viva y sigue
// siendo usable. La transacción hace que el rollback lo garantice, pero el orden
// hace que ni siquiera dependa del rollback.
//
// ------------------------------------------------------------
// DÓNDE VIVE EL «UN SOLO USO»
// ------------------------------------------------------------
// En el UPDATE del paso 3, condicionado y contando filas afectadas — NO en el
// SELECT del paso 1, y NO en un CHECK de la tabla (la migración 0085 lo explica:
// dos transacciones simultáneas pasarían las dos el CHECK y una perdería igual).
// Dos canjes a la vez del mismo token: los dos leen «pendiente», los dos llaman
// a GrantTenantAccess, y en el UPDATE el segundo espera al primero, reevalúa su
// WHERE bajo READ COMMITTED, ve `redeemed_at` ya escrito, afecta CERO filas y se
// va con conflicto. Sin `FOR UPDATE` y sin lock explícito.
//
// ⚠️ VENTANA DE CARRERA HEREDADA, NO CREADA AQUÍ. La guarda de «una sola
// empresa» cuenta bajo READ COMMITTED, así que dos altas simultáneas de la MISMA
// persona en DOS empresas distintas pueden contar cero las dos y escribir las
// dos (memberships.go:133-139). El canje HEREDA esa ventana; no la abre ni la
// ensancha, y no se cierra aquí a propósito: cerrarla pide un advisory lock sobre
// el user_id, que cambiaría el camino que hoy corre en campo por la vía del
// operador. Queda dicho para que quien levante MD-055.2 lo encuentre escrito.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// InvitationRedeemRepo implementa out.InvitationRedeemRepo sobre
// public.tenant_invitations (migración 0085), public.tenant_members (0037) y
// public.access_requests (0060).
type InvitationRedeemRepo struct {
	db *sql.DB
}

// NewInvitationRedeemRepo construye el repositorio sobre el pool dado.
func NewInvitationRedeemRepo(db *sql.DB) *InvitationRedeemRepo {
	return &InvitationRedeemRepo{db: db}
}

var _ out.InvitationRedeemRepo = (*InvitationRedeemRepo)(nil)

// Redeem implementa out.InvitationRedeemRepo: los cuatro pasos de la cabecera,
// en una transacción.
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

	// (1) LEER. `ahora` viene de la MISMA consulta y por tanto del MISMO reloj que
	// escribió `expires_at`; ver leerInvitacion.
	inv, ahora, err := leerInvitacion(ctx, tx, tokenHash)
	if err != nil {
		return err
	}

	// El veredicto lo da una función PURA del dominio, que no puede consultar
	// nada: es la mitad estructural del anti-oráculo (domain/canje.go). Los tres
	// desenlaces de rechazo salen de aquí sin tocar la base una segunda vez.
	switch domain.EvaluarCanje(inv, ahora) {
	case domain.CanjeAusente:
		return domain.ErrNotFound
	case domain.CanjeCaducado:
		return domain.ErrInvitationExpired
	case domain.CanjeConsumido:
		return fmt.Errorf("%w: la invitación ya no se puede usar", domain.ErrConflict)
	case domain.CanjeProcede:
		// sigue abajo
	}

	// (2) DAR EL ACCESO — ANTES de marcar nada. La empresa sale de la FILA
	// (inv.TenantID), que es la que eligió quien emitió la invitación: ni del
	// cuerpo de la petición ni del token de quien canjea, que no trae ninguna.
	//
	// 🔴 SE PASA POR GrantTenantAccess Y NO SE INSERTA AQUÍ. public.tenant_members
	// tiene UN SOLO escritor en todo el código y lo vigila un candado sobre el AST
	// (membresia_unica_ast_test.go): un INSERT propio en este fichero pondría ese
	// test en rojo, y con razón — se saltaría la guarda de «una sola empresa» y
	// dejaría a esa persona con dos membresías sin que nadie lo hubiera decidido.
	//
	// 🔧 Lo que este comentario decía hasta el 2026-08-29 —«o sea sin poder volver
	// a entrar»— YA NO ES CIERTO: desde el Plan 047 · Ola 5 · T5.1 el canje
	// resuelve con dos membresías (empresa activa, D-047.14). El 409 de aquí sigue
	// siendo correcto, pero por lo que la guarda protege HOY: que el alta en una
	// segunda empresa sea una decisión y no un efecto colateral de un canje.
	if err := GrantTenantAccess(ctx, tx, userID, inv.TenantID, inv.RoleID); err != nil {
		return err
	}

	// (3) MARCARLA CANJEADA, condicionado y contando filas: aquí vive el «un solo
	// uso». `revoked_at IS NULL` está en el WHERE aunque el paso (1) ya haya
	// descartado las revocadas, y la redundancia es deliberada: entre la lectura y
	// esta escritura cabe una revocación de la dueña, y sin esta condición el
	// canje pisaría su decisión.
	res, err := tx.ExecContext(ctx, `
		UPDATE public.tenant_invitations
		SET redeemed_at = now(), redeemed_by = $1
		WHERE token_hash = $2 AND redeemed_at IS NULL AND revoked_at IS NULL
	`, userID, tokenHash)
	if err != nil {
		return fmt.Errorf("iam: marcar la invitación canjeada: %w", err)
	}
	afectadas, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("iam: filas afectadas al marcar la invitación: %w", err)
	}
	if afectadas == 0 {
		// Alguien ganó la carrera entre el paso (1) y este, o la dueña la revocó
		// entre medias. Mismo desenlace que si hubiera llegado terminal: conflicto,
		// y el rollback deshace la membresía que el paso (2) acababa de escribir.
		return fmt.Errorf("%w: la invitación ya no se puede usar", domain.ErrConflict)
	}

	// (4) CERRAR LA SOLICITUD HUÉRFANA (T-A4).
	if err := cerrarSolicitudDeAcceso(ctx, tx, userID); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("iam: confirmar el canje de la invitación: %w", err)
	}
	return nil
}

// leerInvitacion trae la fila del digest y, EN LA MISMA CONSULTA, el instante
// del servidor.
//
// 🔴 UNA SOLA CONSULTA, Y ES UN REQUISITO, NO UNA OPTIMIZACIÓN. Es lo que hace
// que «no existe» y «caducada» cuesten lo mismo: la ausencia no dispara una
// segunda pregunta a la base ni ninguna otra rama con E/S — se convierte en un
// puntero nil y sigue por el mismo código que la presencia. Un candado sobre el
// AST cuenta las consultas de este fichero para que nadie añada la segunda sin
// enterarse (canje_una_consulta_ast_test.go).
//
// 🔴 Y `now()` VIENE DE POSTGRES, NO DE time.Now(). `expires_at` lo escribió el
// reloj del servidor de base de datos; compararlo con el reloj del proceso Go
// sería comparar DOS relojes, y su deriva no da un fallo ruidoso: da
// invitaciones que caducan un poco antes o un poco después de lo que dice su
// propia fila, para siempre y sin que nada lo señale. Pedir el instante en la
// misma consulta que la fila cuesta cero y elimina el segundo reloj.
//
// Devuelve (nil, cero, nil) cuando no hay fila: la ausencia NO es un error de
// infraestructura, es uno de los cuatro veredictos posibles y lo clasifica
// domain.EvaluarCanje. El `ahora` cero que la acompaña es intrascendente — la
// rama de la ausencia no lo mira.
func leerInvitacion(ctx context.Context, q Executor, tokenHash []byte) (*domain.Invitation, time.Time, error) {
	var (
		inv   domain.Invitation
		rolID sql.NullString
		redBy sql.NullString
		redAt sql.NullTime
		revAt sql.NullTime
		ahora time.Time
	)
	err := q.QueryRowContext(ctx, `
		SELECT id::text, tenant_id::text, role_id::text, expires_at,
		       redeemed_by::text, redeemed_at, revoked_at, now()
		FROM public.tenant_invitations
		WHERE token_hash = $1
	`, tokenHash).Scan(
		&inv.ID, &inv.TenantID, &rolID, &inv.ExpiresAt,
		&redBy, &redAt, &revAt, &ahora,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, time.Time{}, nil
	case err != nil:
		return nil, time.Time{}, fmt.Errorf("iam: leer la invitación por su digest: %w", err)
	}

	if rolID.Valid {
		inv.RoleID = &rolID.String
	}
	if redBy.Valid {
		inv.RedeemedBy = &redBy.String
	}
	if redAt.Valid {
		inv.RedeemedAt = &redAt.Time
	}
	if revAt.Valid {
		inv.RevokedAt = &revAt.Time
	}
	return &inv, ahora, nil
}

// cerrarSolicitudDeAcceso resuelve la fila `pending` que el invitado dejó en
// public.access_requests al registrarse por el signup público (T-A4).
//
// POR QUÉ ESTE PASO EXISTE. El invitado no llega a wApp por la puerta del
// operador: se registra él mismo (platformadmin.SignupHandler), y ese registro
// deja SIEMPRE una solicitud 'pending' —signup.go paso 3, CreateAccessRequest—
// que aterriza en la bandeja del OPERADOR DE PLATAFORMA. Si el canje no la
// tocara, cada persona incorporada por su dueña dejaría una solicitud eterna
// pidiéndonos a nosotros un acceso que ya tiene.
//
// POR QUÉ LO HACE EL LLAMANTE Y NO GrantTenantAccess. Porque marcar la solicitud
// es del flujo de esa bandeja y de ninguna otra: está escrito en
// memberships.go:118-119 y es la razón de que aquella función reciba la
// transacción en vez de abrir la suya. El operador hace exactamente esto mismo
// en su cuarto paso (platformadmin.executeApprovalTx); aquí es el mismo trato.
//
// 'approved' Y NO 'rejected': el estado terminal describe cómo acabó la
// solicitud, y acabó con esa persona DENTRO de una empresa. 'rejected' diría que
// se le negó el acceso, que es exactamente lo contrario de lo que pasó, y además
// dejaría a la bandeja del operador contando como rechazos las incorporaciones
// que mejor funcionaron.
//
// `decided_by` se queda NULL a propósito, y es la única diferencia con el cuarto
// paso del operador: ahí hay un operador que decidió y aquí no lo hay. Rellenarlo
// con el user_id del invitado diría que se aprobó a sí mismo; rellenarlo con el
// de la dueña diría que un operador de plataforma actuó, y no actuó ninguno. La
// columna es NULLable y su ausencia es el dato: esta solicitud se resolvió sin
// pasar por la bandeja. La fecha sí se pone: `decided_at` es CUÁNDO dejó de
// estar pendiente, y eso sí ocurrió.
//
// CERO FILAS AFECTADAS NO ES UN ERROR, y por eso no se cuentan. Puede no haber
// solicitud pendiente: alguien a quien el operador ya atendió, alguien que llegó
// por otra vía, o una segunda invitación para una persona cuya solicitud se
// cerró en la primera. El estado que se pedía —«esta persona no aparece en la
// bandeja»— ya se cumple, y convertir eso en un fallo abortaría un canje bueno.
// Mismo criterio que MembershipRepo.Remove.
//
// El índice único PARCIAL sobre (user_id) WHERE status='pending' (0060:132-134)
// garantiza que este UPDATE toca UNA fila como mucho, así que no hace falta
// acotarlo ni ordenarlo.
func cerrarSolicitudDeAcceso(ctx context.Context, exec Executor, userID string) error {
	if _, err := exec.ExecContext(ctx, `
		UPDATE public.access_requests
		SET status = 'approved', decided_at = now()
		WHERE user_id = $1 AND status = 'pending'
	`, userID); err != nil {
		return fmt.Errorf("iam: cerrar la solicitud de acceso del invitado: %w", err)
	}
	return nil
}
