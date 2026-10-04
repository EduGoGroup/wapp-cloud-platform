// Porta internal/iam/infra/postgres/invitations.go @ 9a77307

package iampostgres

// invitations.go — EL ADAPTADOR DE public.tenant_invitations (migración 0085; Plan 047 · Ola A ·
// T-A2 y T-A8).
//
// Nada aquí sabe qué es un token: recibe y devuelve el DIGEST de 32 bytes que
// domain.HashInvitationToken produce. El texto en claro no entra ni sale por este fichero, y por
// eso no hay forma de recuperarlo desde la base.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// InvitationRepo implementa out.InvitationRepo sobre public.tenant_invitations. La tabla no lleva
// FK hacia el usuario (created_by/redeemed_by): esa identidad vive en identity, en otra base de
// datos.
type InvitationRepo struct {
	db *sql.DB
}

// NewInvitationRepo construye el repositorio sobre el pool dado. No valida el pool ni lo toca.
func NewInvitationRepo(db *sql.DB) *InvitationRepo { return &InvitationRepo{db: db} }

var _ out.InvitationRepo = (*InvitationRepo)(nil)

// invitationCols es la proyección COMPLETA de la fila, en el orden que espera scanInvitation. Va
// en una constante compartida para que el SELECT del listado y el RETURNING del alta no puedan
// divergir: dos listas escritas a mano acaban desincronizadas en la primera columna nueva, y el
// error sale como un Scan contra el campo equivocado, no como un fallo de compilación.
//
// Los UUID se leen con ::text (convención del repo: los identificadores viajan como string en el
// dominio). token_hash NO: es BYTEA y se lee como []byte tal cual, que es lo que el canje
// comparará.
const invitationCols = `id::text, tenant_id::text, token_hash, role_id::text,
	expires_at, created_by::text, redeemed_by::text, redeemed_at, revoked_at, created_at`

// scanInvitation escanea una fila de tenant_invitations (en el orden de invitationCols) a
// domain.Invitation. Las cuatro columnas NULLables se leen a través de sus tipos Null* y se
// convierten a puntero: nil = NULL, que es como el dominio modela la ausencia.
func scanInvitation(row rowScanner) (domain.Invitation, error) {
	var (
		inv        domain.Invitation
		roleID     sql.NullString
		redeemedBy sql.NullString
		redeemedAt sql.NullTime
		revokedAt  sql.NullTime
	)
	if err := row.Scan(
		&inv.ID, &inv.TenantID, &inv.TokenHash, &roleID,
		&inv.ExpiresAt, &inv.CreatedBy, &redeemedBy, &redeemedAt, &revokedAt, &inv.CreatedAt,
	); err != nil {
		return domain.Invitation{}, err
	}
	inv.RoleID = strPtr(roleID)
	inv.RedeemedBy = strPtr(redeemedBy)
	inv.RedeemedAt = timePtr(redeemedAt)
	inv.RevokedAt = timePtr(revokedAt)
	return inv, nil
}

// Create implementa out.InvitationRepo: escribe la invitación PENDIENTE (las cuatro columnas de
// estado nacen NULL) y devuelve la fila que la base escribió, con su id y su created_at reales.
//
// Un digest que no mida 32 bytes lo rechaza la base (CHECK
// tenant_invitations_token_hash_len_check) y sale como error, no como fila. Un digest repetido →
// error que envuelve domain.ErrConflict y dice que se revise el generador; cualquier otro fallo
// de la base → "iam: emitir invitación: …", nunca ErrConflict.
//
// El INSERT escribe las columnas que vienen de la invitación y deja que la base ponga `id` y
// `created_at` con sus defaults. El RETURNING trae la fila entera para que quien emita reciba el
// id y el instante REALES —los que la base escribió— y no una reconstrucción hecha en Go que
// podría diferir en el reloj.
//
// 🔴 Que el CHECK de la tabla rechace el digest mal medido es deliberado: la única forma de
// enterarse de que alguien guardó un `[]byte(token)` en claro por descuido es que la base se
// niegue.
func (r *InvitationRepo) Create(ctx context.Context, inv domain.Invitation) (domain.Invitation, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO public.tenant_invitations (tenant_id, token_hash, role_id, expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+invitationCols,
		inv.TenantID, inv.TokenHash, nullString(inv.RoleID), inv.ExpiresAt, inv.CreatedBy,
	)
	created, err := scanInvitation(row)
	if err != nil {
		if isUniqueViolation(err) {
			// El índice único es sobre token_hash: un choque aquí NO es un caso de negocio, es la
			// señal de que el generador dejó de ser aleatorio. Se mapea a ErrConflict igual que el
			// resto del repo, pero el mensaje dice lo que hay que ir a mirar.
			return domain.Invitation{}, fmt.Errorf("%w: ya existe una invitación con ese digest "+
				"(dos tokens distintos no pueden colisionar: revisa el generador)", domain.ErrConflict)
		}
		return domain.Invitation{}, fmt.Errorf("iam: emitir invitación: %w", err)
	}
	return created, nil
}

// ListByTenant implementa out.InvitationRepo: las invitaciones de tenantID y solo de él, en
// orden (created_at DESC, id DESC) —lo más nuevo arriba, con desempate estable—, en todos sus
// estados. Sin invitaciones, lista vacía no nil. Fallo de la base → (nil, "iam: listar
// invitaciones: …").
//
// EL ORDEN LO FIJA T-A2: la dueña abre la pantalla para ver la que acaba de emitir. El `id`
// detrás no es adorno: dos emisiones del mismo instante —el reloj de Postgres tiene
// microsegundos, pero un script que emita en lote los agota— dejarían el orden a merced del plan
// de ejecución, y un listado que cambia de orden entre dos recargas está roto para quien lo
// pagine. Mismo criterio que MembersOf, que desempata por user_id.
//
// ⚠️ El índice que hoy sirve esta consulta es idx_tenant_invitations_tenant, que es SOLO por
// tenant_id: Postgres filtra por él y ordena después. La migración 0085 dejó dicho que cuando
// T-A2 fijara el ORDER BY se ampliaría a (tenant_id, created_at DESC) — está fijado aquí, y la
// ampliación es la que se reporta a quien lleva la migración.
func (r *InvitationRepo) ListByTenant(ctx context.Context, tenantID string) ([]domain.Invitation, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+invitationCols+`
		FROM public.tenant_invitations
		WHERE tenant_id = $1
		ORDER BY created_at DESC, id DESC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("iam: listar invitaciones: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	// invitations (era invitaciones) nace NO nula: sin invitaciones, lista vacía.
	invitations := make([]domain.Invitation, 0)
	for rows.Next() {
		inv, scanErr := scanInvitation(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("iam: listar invitaciones: %w", scanErr)
		}
		invitations = append(invitations, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iam: listar invitaciones: %w", err)
	}
	return invitations, nil
}

// Revoke implementa out.InvitationRepo (T-A8): marca revoked_at y NO borra la fila. La condición
// (pendiente y de ese tenant) viaja DENTRO del UPDATE: es lo único que resuelve la carrera con el
// canje. Si no se revocó nada, el motivo: inexistente o de otra empresa → domain.ErrNotFound (el
// mismo, anti-oráculo); ya canjeada → error que envuelve domain.ErrConflict; ya revocada → nil
// (idempotente). Un fallo de la base → "iam: revocar invitación: …", nunca ErrNotFound ni nil.
//
// 🔴 LA CONDICIÓN VIAJA DENTRO DEL UPDATE, Y AHÍ ESTÁ TODA LA CORRECCIÓN. La exclusividad entre
// los dos estados terminales —canjeada y revocada— NO la vigila ningún CHECK de la tabla, y la
// migración 0085 explica por qué: un CHECK dejaría pasar a las dos transacciones simultáneas
// igual, porque cada una lo evalúa contra el estado que vio al empezar. Lo único que resuelve la
// carrera es que `redeemed_at IS NULL AND revoked_at IS NULL` esté en el WHERE de la escritura.
// Un SELECT previo seguido de un UPDATE incondicional sería el mismo código en apariencia y
// admitiría revocar una invitación que se acaba de canjear.
//
// El `tenant_id` está en el WHERE por lo mismo: el aislamiento no puede depender de un `if`
// posterior a la escritura.
func (r *InvitationRepo) Revoke(ctx context.Context, id, tenantID string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE public.tenant_invitations
		SET revoked_at = now()
		WHERE id = $1
		  AND tenant_id = $2
		  AND redeemed_at IS NULL
		  AND revoked_at IS NULL
	`, id, tenantID)
	if err != nil {
		return fmt.Errorf("iam: revocar invitación: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("iam: revocar invitación: %w", err)
	}
	if n > 0 {
		return nil
	}
	return r.whyNotRevoked(ctx, id, tenantID)
}

// whyNotRevoked (era porQueNoSeRevoco) explica un UPDATE que no tocó ninguna fila.
//
// Corre SOLO fuera del camino feliz y NO debilita la atomicidad: la decisión ya la tomó el UPDATE
// de arriba, y esto solo traduce «no se aplicó» en el motivo, que es lo único con lo que quien
// llama puede hacer algo distinto. Sin esta lectura, revocar dos veces daría el mismo 404 que
// revocar la invitación de otra empresa, y revocar una ya canjeada daría un 404 que sugiere «no
// existe» cuando lo que pasa es que existe y ya dejó una membresía detrás.
func (r *InvitationRepo) whyNotRevoked(ctx context.Context, id, tenantID string) error {
	var redeemedAt, revokedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT redeemed_at, revoked_at
		FROM public.tenant_invitations
		WHERE id = $1 AND tenant_id = $2
	`, id, tenantID).Scan(&redeemedAt, &revokedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// No existe, o es de otra empresa. Los dos comparten código a propósito: un "prohibido"
		// confirmaría que ese id existe fuera.
		return domain.ErrNotFound
	case err != nil:
		return fmt.Errorf("iam: revocar invitación: %w", err)
	case redeemedAt.Valid:
		return fmt.Errorf("%w: la invitación ya fue canjeada y revocarla no deshace la membresía", domain.ErrConflict)
	case revokedAt.Valid:
		// Idempotente: la baja de algo ya dado de baja es el estado que se pedía.
		return nil
	default:
		// Ni canjeada ni revocada y aun así el UPDATE no la tocó: no hay camino que lleve aquí
		// con este esquema (las dos marcas son monótonas, nunca vuelven a NULL). Se contesta
		// ErrNotFound —lo conservador: NO se revocó— en vez de un nil que fingiría éxito.
		return domain.ErrNotFound
	}
}
