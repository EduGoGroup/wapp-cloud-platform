//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out/outhelpertest"
)

// Este fichero prueba POR CONDUCTA, contra Postgres, la carrera entre el canje de una invitación
// y su revocación (R-P8, D-F2-12 de Jhoan, 2026-10-04). La suite de contrato no puede: sus casos
// son secuenciales y con los dobles de memoria no hay transacciones que intercalar.
//
// Es hermano de iam_contrato_test.go y vive bajo la misma excepción de R9.4.d: importa la suite
// (por sus tipos de siembra y de foto) y, del paquete de los adaptadores, solo los constructores.

const (
	// iamMembershipLock es el espacio de nombres del advisory lock del alta de membresía: el
	// valor de membershipWriteLock en iam/infra/postgres/memberships.go, copiado literal porque de
	// ese paquete solo se pueden usar los constructores. Si cambia allí, este test deja de ver la
	// espera y FALLA (no pasa en falso).
	iamMembershipLock = 47052
	// iamTakeMembershipLock es la sentencia del cerrojo, copiada literal de GrantTenantAccess.
	iamTakeMembershipLock = `SELECT pg_advisory_xact_lock($1, hashtext($2))`
	// iamRaceTimeout acota el canje entero y cada espera del test.
	iamRaceTimeout = 30 * time.Second
	// iamRacePoll es el paso del sondeo de pg_locks.
	iamRacePoll = 20 * time.Millisecond
	// iamRaceUser es la persona que canjea. La columna no tiene FK: la identidad vive en identity.
	iamRaceUser = "7b1d0c7e-2f0a-4d53-9a55-0c5a1f6e8d21"
	// iamConflictText es el texto de domain.ErrConflict, que aquí no se puede importar (R9.4.d).
	iamConflictText = "iam: conflicto de unicidad"
)

// TestIAMRedeem_RevokedWhileRedeeming_Conflict: si la dueña revoca la invitación entre la lectura
// del canje (paso 1) y su marca (paso 3), el canje acaba en conflicto, la invitación queda
// revocada y NO canjeada, y la persona no queda con membresía ni con rol.
//
// El intercalado es determinista: el paso 2 (GrantTenantAccess) empieza tomando el cerrojo
// advisory de la persona, así que el test lo toma ANTES desde otra transacción. El canje lee la
// invitación como pendiente y se queda esperando el cerrojo; el test lo ve esperar en pg_locks,
// revoca, y suelta. Si el canje no llega a esperar, el test falla: sin esa espera la revocación
// caería antes de la lectura y el conflicto lo daría el paso 1, no el WHERE del paso 3.
func TestIAMRedeem_RevokedWhileRedeeming_Conflict(t *testing.T) {
	b := newIAMBase(t)
	tables := &iamRedeemTables{db: b.db}
	roleA := iamSeedRole(t, b.db, b.tenantA, "operador")
	digest := []byte("digest-de-la-carrera-con-revocar")
	tables.SeedInvitation(t, outhelpertest.InvitationSeed{
		TenantID: b.tenantA, TokenHash: digest, RoleID: &roleA, ExpiresIn: time.Hour,
	})
	before := tables.Snapshot(t, digest, iamRaceUser)
	if before.Invitation == nil || before.Invitation.RevokedAt != nil || before.Invitation.RedeemedAt != nil {
		t.Fatalf("la invitación sembrada no está pendiente: %+v", before.Invitation)
	}

	ctx, cancel := context.WithTimeout(t.Context(), iamRaceTimeout)
	defer cancel()

	// El cerrojo de la persona, tomado desde OTRA transacción del mismo pool.
	release := holdMembershipLock(ctx, t, b.db)

	repo := iampostgres.NewInvitationRedeemRepo(b.db, newIAMFeatures())
	done := make(chan error, 1)
	go func() {
		done <- repo.Redeem(ctx, digest, iamRaceUser)
		close(done) // quien vuelva a leer tras el resultado no se queda colgado
	}()
	// Pase lo que pase, el cerrojo se suelta y el canje termina antes de que se borre la base.
	defer func() {
		release()
		<-done
	}()

	// CONTROL: el canje tiene que ESTAR ESPERANDO el cerrojo —ya leyó la invitación— antes de
	// revocar.
	waitForRedeemBlocked(ctx, t, b.db, done)

	if err := iampostgres.NewInvitationRepo(b.db).Revoke(ctx, before.Invitation.ID, b.tenantA); err != nil {
		t.Fatalf("revocar la invitación en mitad del canje: %v", err)
	}
	if mid := tables.Snapshot(t, digest, iamRaceUser); mid.Invitation == nil || mid.Invitation.RevokedAt == nil {
		t.Fatalf("la revocación no quedó escrita antes de soltar el cerrojo: %+v", mid.Invitation)
	}

	release()

	select {
	case redeemErr := <-done:
		if redeemErr == nil || !strings.Contains(redeemErr.Error(), iamConflictText) {
			t.Errorf("Redeem = %v, quiero un error de conflicto (%q): la dueña revocó entre la lectura y la marca",
				redeemErr, iamConflictText)
		}
	case <-ctx.Done():
		t.Fatalf("el canje no terminó tras soltar el cerrojo: %v", ctx.Err())
	}

	wantRevokedAndUntouched(t, tables.Snapshot(t, digest, iamRaceUser))
}

// holdMembershipLock abre una transacción en db, toma en ella el cerrojo advisory de iamRaceUser
// —la misma sentencia que GrantTenantAccess— y devuelve la función que lo suelta (ROLLBACK), que
// se puede llamar más de una vez. Falla el test si no puede tomarlo.
func holdMembershipLock(ctx context.Context, t *testing.T, db *sql.DB) (release func()) {
	t.Helper()
	holder, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("abrir la transacción que retiene el cerrojo: %v", err)
	}
	release = func() {
		if rerr := holder.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			t.Errorf("soltar el cerrojo: %v", rerr)
		}
	}
	if _, err := holder.ExecContext(ctx, iamTakeMembershipLock, iamMembershipLock, iamRaceUser); err != nil {
		release()
		t.Fatalf("tomar el cerrojo de la persona: %v", err)
	}
	return release
}

// wantRevokedAndUntouched afirma el desenlace de R-P8 sobre la foto de después: la invitación
// sigue ahí, revocada y sin canjear, y la persona no tiene membresía ni rol.
func wantRevokedAndUntouched(t *testing.T, after outhelpertest.RedeemState) {
	t.Helper()
	if after.Invitation == nil {
		t.Fatal("la invitación desapareció")
	}
	if after.Invitation.RevokedAt == nil {
		t.Error("revoked_at es NULL: el canje deshizo la revocación de la dueña")
	}
	if after.Invitation.RedeemedAt != nil || after.Invitation.RedeemedBy != nil {
		t.Errorf("la invitación revocada quedó canjeada (redeemed_at = %v, redeemed_by nulo = %t): el canje "+
			"pisó la decisión de la dueña", after.Invitation.RedeemedAt, after.Invitation.RedeemedBy == nil)
	}
	if len(after.Memberships) != 0 {
		t.Errorf("la persona quedó con membresía tras un canje en conflicto: %+v", after.Memberships)
	}
	if len(after.Roles) != 0 {
		t.Errorf("la persona quedó con rol tras un canje en conflicto: %+v", after.Roles)
	}
}

// waitForRedeemBlocked sondea pg_locks hasta ver UNA sesión de esta base esperando (granted =
// false) el cerrojo advisory de iamRaceUser, y falla el test si el canje termina antes o si vence
// ctx sin verla. hashtext da un int4 y pg_locks.objid es un oid sin signo: se comparan en bigint.
func waitForRedeemBlocked(ctx context.Context, t *testing.T, db *sql.DB, done <-chan error) {
	t.Helper()
	const waiting = `
		SELECT count(*) FROM pg_locks l
		JOIN pg_database d ON d.oid = l.database
		WHERE d.datname = current_database()
		  AND l.locktype = 'advisory'
		  AND l.classid::bigint = $1
		  AND l.objid::bigint = (hashtext($2)::bigint & 4294967295)
		  AND l.objsubid = 2
		  AND NOT l.granted`
	for {
		var n int
		if err := db.QueryRowContext(ctx, waiting, iamMembershipLock, iamRaceUser).Scan(&n); err != nil {
			t.Fatalf("sondear pg_locks: %v", err)
		}
		if n == 1 {
			t.Logf("control: el canje está esperando el cerrojo de la persona (pg_locks, granted = false)")
			return
		}
		select {
		case err := <-done:
			t.Fatalf("el canje terminó (err = %v) SIN esperar el cerrojo de la persona: el test no puede "+
				"intercalar la revocación entre la lectura y la marca", err)
		case <-ctx.Done():
			t.Fatalf("el canje no llegó a esperar el cerrojo de la persona en %s (%d esperas vistas)", iamRaceTimeout, n)
		case <-time.After(iamRacePoll):
		}
	}
}
