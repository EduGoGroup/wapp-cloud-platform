package integrations_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
)

// pgClosing es una de las tres transiciones del adaptador que cierran un claim.
type pgClosing struct {
	name string
	// what es como la transición se nombra en sus errores.
	what string
	// query es su sentencia literal y own los argumentos propios (desde $3).
	query string
	own   []driver.Value
	apply func(store *integrations.Postgres, claim integrations.WebhookOutbox) error
}

// Los argumentos de las transiciones que los llevan.
var (
	pgNextAttempt = time.Date(2026, 10, 8, 13, 30, 0, 0, time.UTC)
	pgSeal        = time.Date(2026, 10, 8, 12, 0, 0, 123456000, time.UTC)
)

const pgReason = "respuesta 500 del puente"

func pgClosings() []pgClosing {
	ctx := context.Background()
	return []pgClosing{
		{"MarkWebhookDelivered", "delivered", deliveredSQL, []driver.Value{"delivered", "delivering"},
			func(s *integrations.Postgres, c integrations.WebhookOutbox) error {
				return s.MarkWebhookDelivered(ctx, c)
			}},
		{"MarkWebhookFailed", "reintento", failedSQL, []driver.Value{"pending", pgNextAttempt, pgReason, "delivering"},
			func(s *integrations.Postgres, c integrations.WebhookOutbox) error {
				return s.MarkWebhookFailed(ctx, c, pgNextAttempt, pgReason)
			}},
		{"MarkWebhookDead", "dead", deadSQL, []driver.Value{"dead", pgReason, "delivering"},
			func(s *integrations.Postgres, c integrations.WebhookOutbox) error {
				return s.MarkWebhookDead(ctx, c, pgReason)
			}},
	}
}

// pgClaim es el claim que presentan estos tests: lo que cuenta de él son el id y el sello.
func pgClaim() integrations.WebhookOutbox {
	return integrations.WebhookOutbox{ID: 7, TenantID: pgTenant, Status: "delivering", Attempts: 2, ClaimedAt: pgSeal}
}

// TestPostgres_Mark_EmitsTheStatementWithTheFence: cada transición es UNA sentencia, la literal, y
// sus dos primeros argumentos son SIEMPRE la valla —el id y el sello del claim, tal cual—; los
// propios van detrás, con el estado `delivering` que la valla exige al final.
func TestPostgres_Mark_EmitsTheStatementWithTheFence(t *testing.T) {
	for _, c := range pgClosings() {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			if err := c.apply(store, pgClaim()); err != nil {
				t.Fatalf("%s: error inesperado %v", c.name, err)
			}
			got := only(t, fake, eventExec, c.query)
			requireArgs(t, got.args, append([]driver.Value{int64(7), pgSeal}, c.own...)...)
		})
	}
}

// TestPostgres_Mark_NoRowAffected_ErrClaimLost: si la sentencia no toca ninguna fila, la valla no
// casó: el claim se perdió. Sale ErrClaimLost ENVUELTO, con el id y la transición en su texto.
func TestPostgres_Mark_NoRowAffected_ErrClaimLost(t *testing.T) {
	for _, c := range pgClosings() {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			fake.script(touched(0))
			err := c.apply(store, pgClaim())
			if !errors.Is(err, integrations.ErrClaimLost) {
				t.Fatalf("err = %v, quería uno que envuelva ErrClaimLost", err)
			}
			want := fmt.Sprintf("integrations: entrega 7 hacia %s: integrations: el claim de la entrega ya no es vigente", c.what)
			if err.Error() != want {
				t.Errorf("texto del rechazo =\n%s\nquería, byte a byte:\n%s", err, want)
			}
		})
	}
}

// TestPostgres_Mark_Errors: un fallo de la sentencia, o un driver que no sabe decir cuántas filas
// tocó, sale con su prefijo —que nombra el id y la transición— y NO es un claim perdido.
func TestPostgres_Mark_Errors(t *testing.T) {
	for _, c := range pgClosings() {
		failures := []struct {
			name   string
			reply  reply
			prefix string
		}{
			{"the statement fails", reply{err: errDB}, "integrations: marcar entrega 7 " + c.what + ": "},
			{"rows affected unknown", reply{affectedErr: errDB}, "integrations: filas afectadas al marcar la entrega 7 " + c.what + ": "},
		}
		for _, f := range failures {
			t.Run(c.name+"/"+f.name, func(t *testing.T) {
				store, fake := newPostgres(t)
				fake.script(f.reply)
				err := c.apply(store, pgClaim())
				requireWrapped(t, err, errDB, f.prefix)
				if errors.Is(err, integrations.ErrClaimLost) {
					t.Error("un fallo de la base pasa por claim perdido: el worker lo callaría")
				}
			})
		}
	}
}

// TestPostgres_RecoverOrphanDeliveries_EmitsTheStatement: el rescate es UNA sentencia, la literal,
// con el estado al que vuelve, el last_error LITERAL del rescate, el estado que busca y el lease
// en SEGUNDOS (con sus decimales); devuelve las filas que la base dice haber tocado.
func TestPostgres_RecoverOrphanDeliveries_EmitsTheStatement(t *testing.T) {
	const reason = "claim vencido: el worker que reclamó la entrega no la resolvió dentro del lease"
	cases := []struct {
		name    string
		lease   time.Duration
		seconds float64
		touched int64
	}{
		{"one minute", time.Minute, 60, 3},
		{"fraction of a second", 1500 * time.Millisecond, 1.5, 1},
		{"zero lease", 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			fake.script(touched(c.touched))
			n, err := store.RecoverOrphanDeliveries(context.Background(), c.lease)
			if err != nil {
				t.Fatalf("RecoverOrphanDeliveries: error inesperado %v", err)
			}
			if int64(n) != c.touched {
				t.Errorf("recuperadas = %d, quería las que tocó la base (%d)", n, c.touched)
			}
			got := only(t, fake, eventExec, recoverSQL)
			requireArgs(t, got.args, "pending", reason, "delivering", c.seconds)
		})
	}
}

// TestPostgres_RecoverOrphanDeliveries_Errors: cada fallo sale con su prefijo y con cero como
// cuenta.
func TestPostgres_RecoverOrphanDeliveries_Errors(t *testing.T) {
	cases := []struct {
		name   string
		reply  reply
		prefix string
	}{
		{"the statement fails", reply{err: errDB}, "integrations: recuperar entregas huérfanas: "},
		{"rows affected unknown", reply{affectedErr: errDB}, "integrations: contar entregas recuperadas: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			fake.script(c.reply)
			n, err := store.RecoverOrphanDeliveries(context.Background(), time.Minute)
			requireWrapped(t, err, errDB, c.prefix)
			if n != 0 {
				t.Errorf("recuperadas = %d con error, quería 0", n)
			}
		})
	}
}
