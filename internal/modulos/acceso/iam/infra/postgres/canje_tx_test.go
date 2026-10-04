package iampostgres

// Parte del gemelo de canje.go: Redeem entero sobre el driver guionizado de canje_fakesql_test.go.
// Fija dos promesas que desde fuera no se ven sin una base: que un canje que no confirma DESHACE
// y suelta su transacción, y que la caducidad se mide con el now() de la base (R-P6).

import (
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
)

// errScripted es la causa de los fallos guionizados.
var errScripted = errors.New("fallo guionizado (test)")

// Fechas fijas de las filas guionizadas: una invitación viva según el reloj de la base.
var (
	scriptedNow     = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	scriptedExpires = scriptedNow.Add(48 * time.Hour)
)

// invitationValues arma la fila que lee readInvitation: una invitación pendiente y sin rol del
// tenant de prueba, que vence en expiresAt, leída cuando la base decía dbNow.
func invitationValues(expiresAt, dbNow time.Time) []driver.Value {
	return []driver.Value{testInvitationID, testTenantID, nil, expiresAt, nil, nil, nil, dbNow}
}

// redeemScripted canjea sobre el guion s y devuelve el error de Redeem. Exige además que la
// conexión haya vuelto al pool: una transacción sin cerrar la retiene.
func redeemScripted(t *testing.T, s *scriptedSQL) error {
	t.Helper()
	db := s.pool(t)
	err := NewInvitationRedeemRepo(db, &recordingResolver{}).
		Redeem(t.Context(), domain.HashInvitationToken("token-de-prueba"), testUserID)
	if inUse := db.Stats().InUse; inUse != 0 {
		t.Errorf("tras Redeem quedan %d conexiones en uso; quiere 0 (la transacción no se cerró)", inUse)
	}
	return err
}

// wantSteps exige exactamente esos pasos, en ese orden.
func wantSteps(t *testing.T, s *scriptedSQL, want ...string) {
	t.Helper()
	if got := s.Steps(); !slices.Equal(got, want) {
		t.Errorf("pasos = %v;\nquiere  %v", got, want)
	}
}

// TestRedeem_HappyPathCommits: un canje limpio da sus cuatro pasos DENTRO de la transacción y la
// confirma; no hay ROLLBACK ni ninguna sentencia por el pool.
func TestRedeem_HappyPathCommits(t *testing.T) {
	t.Run("without_role", func(t *testing.T) {
		s := &scriptedSQL{invitation: invitationValues(scriptedExpires, scriptedNow)}
		if err := redeemScripted(t, s); err != nil {
			t.Fatalf("Redeem = %v; quiere nil", err)
		}
		wantSteps(t, s, stepBegin, stepRead, stepLock, stepCount, stepInsertMember, stepMark, stepCloseRequest, stepCommit)
	})
	t.Run("with_role", func(t *testing.T) {
		row := invitationValues(scriptedExpires, scriptedNow)
		row[2] = companyRoleID
		s := &scriptedSQL{invitation: row}
		if err := redeemScripted(t, s); err != nil {
			t.Fatalf("Redeem = %v; quiere nil", err)
		}
		wantSteps(t, s, stepBegin, stepRead, stepLock, stepCount, stepInsertMember, stepInsertRole,
			stepMark, stepCloseRequest, stepCommit)
	})
}

// TestRedeem_RollsBackWithoutCommit: un canje que termina sin Commit —rechazado por su veredicto
// o con un fallo a mitad— DESHACE y suelta su transacción: el último paso es ROLLBACK, no hay
// COMMIT y la conexión vuelve al pool. Sin ese ROLLBACK, la membresía que el paso (2) escribió
// antes de un «cero filas» en el paso (3) quedaría en una transacción abierta.
func TestRedeem_RollsBackWithoutCommit(t *testing.T) {
	alive := func() []driver.Value { return invitationValues(scriptedExpires, scriptedNow) }
	redeemed := alive()
	redeemed[4], redeemed[5] = testUserID, scriptedNow.Add(-time.Hour)
	granted := []string{stepBegin, stepRead, stepLock, stepCount, stepInsertMember}

	cases := []struct {
		name   string
		script *scriptedSQL
		// wantIs es el centinela que el error envuelve; wantPrefix, su texto de infraestructura.
		wantIs     error
		wantPrefix string
		wantSteps  []string
	}{
		{"missing", &scriptedSQL{}, domain.ErrNotFound, "", []string{stepBegin, stepRead}},
		{
			"expired", &scriptedSQL{invitation: invitationValues(scriptedNow.Add(-time.Hour), scriptedNow)},
			domain.ErrInvitationExpired, "", []string{stepBegin, stepRead},
		},
		{"already_redeemed", &scriptedSQL{invitation: redeemed}, domain.ErrConflict, "", []string{stepBegin, stepRead}},
		{
			"read_fails", &scriptedSQL{fail: map[string]error{stepRead: errScripted}},
			errScripted, "iam: leer la invitación por su digest: ", []string{stepBegin, stepRead},
		},
		{
			"member_of_another_company", &scriptedSQL{invitation: alive(), others: 1},
			domain.ErrConflict, "", []string{stepBegin, stepRead, stepLock, stepCount},
		},
		{
			"mark_fails", &scriptedSQL{invitation: alive(), fail: map[string]error{stepMark: errScripted}},
			errScripted, "iam: marcar la invitación canjeada: ", append(slices.Clone(granted), stepMark),
		},
		{
			"lost_the_race_on_mark", &scriptedSQL{invitation: alive(), zeroRows: map[string]bool{stepMark: true}},
			domain.ErrConflict, "", append(slices.Clone(granted), stepMark),
		},
		{
			"close_request_fails", &scriptedSQL{invitation: alive(), fail: map[string]error{stepCloseRequest: errScripted}},
			errScripted, "iam: cerrar la solicitud de acceso del invitado: ",
			append(slices.Clone(granted), stepMark, stepCloseRequest),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := redeemScripted(t, c.script)
			if !errors.Is(err, c.wantIs) {
				t.Fatalf("Redeem = %v; quiere un error que envuelva %v", err, c.wantIs)
			}
			if !strings.HasPrefix(err.Error(), c.wantPrefix) {
				t.Errorf("texto %q; quiere el prefijo %q", err.Error(), c.wantPrefix)
			}
			wantSteps(t, c.script, append(slices.Clone(c.wantSteps), stepRollback)...)
		})
	}
}

// TestRedeem_CommitFailureIsReported: si la base no confirma, el canje lo dice con su texto y no
// queda registrado ningún COMMIT.
func TestRedeem_CommitFailureIsReported(t *testing.T) {
	s := &scriptedSQL{invitation: invitationValues(scriptedExpires, scriptedNow), commitErr: errScripted}
	err := redeemScripted(t, s)
	if !errors.Is(err, errScripted) {
		t.Fatalf("Redeem = %v; quiere un error que envuelva %v", err, errScripted)
	}
	if want := "iam: confirmar el canje de la invitación: "; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("texto %q; quiere el prefijo %q", err.Error(), want)
	}
	if steps := s.Steps(); slices.Contains(steps, stepCommit) {
		t.Errorf("pasos = %v; no debe haber COMMIT", steps)
	}
}

// TestRedeem_ExpiryFollowsDatabaseClock es R-P6: la caducidad se decide con el now() que la base
// devuelve en la MISMA consulta que la invitación, no con el reloj del proceso. Las dos filas
// ponen el reloj de la base a siglos del de cualquier proceso que corra este test, en un sentido
// y en el otro, y el veredicto sigue a la base en los dos.
func TestRedeem_ExpiryFollowsDatabaseClock(t *testing.T) {
	t.Run("alive_for_the_database_though_long_past_for_the_process", func(t *testing.T) {
		s := &scriptedSQL{invitation: invitationValues(
			time.Date(2001, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC))}
		if err := redeemScripted(t, s); err != nil {
			t.Fatalf("Redeem = %v; quiere nil: para la base (2001-01-01) no ha vencido (2001-06-01)", err)
		}
		if steps := s.Steps(); !slices.Contains(steps, stepCommit) {
			t.Errorf("pasos = %v; quiere un canje confirmado", steps)
		}
	})
	t.Run("expired_for_the_database_though_far_ahead_for_the_process", func(t *testing.T) {
		s := &scriptedSQL{invitation: invitationValues(
			time.Date(2998, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC))}
		err := redeemScripted(t, s)
		if !errors.Is(err, domain.ErrInvitationExpired) {
			t.Fatalf("Redeem = %v; quiere %v: para la base (2999) venció en 2998", err, domain.ErrInvitationExpired)
		}
		wantSteps(t, s, stepBegin, stepRead, stepRollback)
	})
}
