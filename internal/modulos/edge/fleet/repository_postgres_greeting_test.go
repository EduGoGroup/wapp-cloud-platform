package fleet

// Los tests de fichero del aviso de sesión pasiva en fleet.PostgresRepository: PendingGreeting
// («¿hay que avisar a esta sesión, y a qué número?») y MarkGreeted (el compare-and-set que deja
// constancia). No están en fleet.Repository: son de su consumidor, el emisor del saludo.

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Las dos sentencias, byte a byte y escritas a mano (ver repository_postgres_test.go).
const (
	// PendingGreeting: tres condiciones de la FILA además de la identidad.
	sqlPendingGreeting = "\n" +
		"\t\tSELECT self_pn_enc, self_pn_dek, self_pn_kek_id\n" +
		"\t\tFROM public.fleet_sessions\n" +
		"\t\tWHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3\n" +
		"\t\t  AND greeted_at IS NULL\n" +
		"\t\t  AND self_pn_bidx IS NOT NULL\n" +
		"\t\t  AND profile = 'passive'\n" +
		"\t"
	// MarkGreeted: el centinela `greeted_at IS NULL` es la idempotencia.
	sqlMarkGreeted = "\n" +
		"\t\tUPDATE public.fleet_sessions\n" +
		"\t\tSET greeted_at = now(), updated_at = now()\n" +
		"\t\tWHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3\n" +
		"\t\t  AND greeted_at IS NULL\n" +
		"\t"
)

// envelopeRow es la fila de tres columnas de PendingGreeting con plain sellado por cipher.
func envelopeRow(t *testing.T, cipher *crypto.FieldCipher, plain string) []driver.Value {
	t.Helper()
	enc, dek, kekID, err := cipher.Encrypt(plain)
	if err != nil {
		t.Fatalf("sellar el self_pn de la fila: %v", err)
	}
	return []driver.Value{enc, dek, kekID}
}

// requireNotPending afirma que la respuesta no lleva número ni pendiente.
func requireNotPending(t *testing.T, to string, pending bool) {
	t.Helper()
	if to != "" || pending {
		t.Errorf("PendingGreeting = (%q, %v), quería (\"\", false)", to, pending)
	}
}

// TestPostgresRepository_PendingGreeting_Pending: la sentencia exacta y sus tres argumentos; con
// fila, devuelve el número DESCIFRADO y pending=true. Y nunca loguea.
func TestPostgresRepository_PendingGreeting_Pending(t *testing.T) {
	f := newFixture(t)
	f.fake.script(reply{rows: [][]driver.Value{envelopeRow(t, f.cipher, "34600111222")}})
	to, pending, err := f.repo.PendingGreeting(context.Background(), pgTenant, pgEdge, pgSession)
	if to != "34600111222" || !pending || err != nil {
		t.Errorf("PendingGreeting = (%q, %v, %v), quería (\"34600111222\", true, nil)", to, pending, err)
	}
	requireStatements(t, f.fake, statement{sqlPendingGreeting, []driver.Value{pgTenant, pgEdge, pgSession}})
	if got := f.log.seen(); len(got) != 0 {
		t.Errorf("PendingGreeting dejó avisos en el log: %+v", got)
	}
}

// TestPostgresRepository_PendingGreeting_RowConditions: las tres condiciones viven en la
// sentencia: sin saludar, con número conocido y en perfil PASIVO (`= 'passive'`, no `<> 'active'`).
func TestPostgresRepository_PendingGreeting_RowConditions(t *testing.T) {
	for _, predicate := range []string{
		"\n\t\t  AND greeted_at IS NULL\n",
		"\n\t\t  AND self_pn_bidx IS NOT NULL\n",
		"\n\t\t  AND profile = 'passive'\n",
	} {
		if !strings.Contains(sqlPendingGreeting, predicate) {
			t.Errorf("a la sentencia le falta la condición %q", predicate)
		}
	}
	if strings.Contains(sqlPendingGreeting, "<>") {
		t.Errorf("la sentencia filtra el perfil por exclusión:\n%s", sqlPendingGreeting)
	}
}

// TestPostgresRepository_PendingGreeting_NoRow: cero filas es la respuesta normal (ya saludada,
// activa, sin emparejar…), no un error.
func TestPostgresRepository_PendingGreeting_NoRow(t *testing.T) {
	f := newFixture(t)
	to, pending, err := f.repo.PendingGreeting(context.Background(), pgTenant, pgEdge, pgSession)
	if err != nil {
		t.Errorf("PendingGreeting sin fila: error inesperado %v", err)
	}
	requireNotPending(t, to, pending)
}

// TestPostgresRepository_PendingGreeting_DriverError: el fallo del driver vuelve envuelto.
func TestPostgresRepository_PendingGreeting_DriverError(t *testing.T) {
	f := newFixture(t, reply{err: errBoom})
	to, pending, err := f.repo.PendingGreeting(context.Background(), pgTenant, pgEdge, pgSession)
	requireNotPending(t, to, pending)
	requireWrapped(t, err, errBoom, "fleet: consultar saludo pendiente: ")
}

// TestPostgresRepository_PendingGreeting_UndecryptableIsHard: aquí el número ES el destino del
// mensaje, así que un sobre que no abre es un ERROR (no pending=false, y no el fallo blando de
// Get y List): callarlo sería un saludo que no se manda nunca. Y sigue sin loguear.
func TestPostgresRepository_PendingGreeting_UndecryptableIsHard(t *testing.T) {
	f := newFixture(t)
	f.fake.script(reply{rows: [][]driver.Value{envelopeRow(t, foreignCipher(t), "34600111222")}})
	to, pending, err := f.repo.PendingGreeting(context.Background(), pgTenant, pgEdge, pgSession)
	requireNotPending(t, to, pending)
	requireWrapped(t, err, crypto.ErrKEKNotInKeyring, "fleet: descifrar self_pn: ")
	if got := f.log.seen(); len(got) != 0 {
		t.Errorf("PendingGreeting dejó avisos en el log: %+v", got)
	}
}

// TestPostgresRepository_PendingGreeting_BrokenEnvelopes: un sobre a medias y una fila con índice
// ciego pero sin sobre son errores, cada uno con su texto literal.
func TestPostgresRepository_PendingGreeting_BrokenEnvelopes(t *testing.T) {
	cases := []struct {
		name string
		row  func(full []driver.Value) []driver.Value
		want string
	}{
		{"incomplete envelope", func(full []driver.Value) []driver.Value { return []driver.Value{full[0], nil, full[2]} },
			"fleet: sobre de self_pn incompleto (enc/dek/kek_id no viajan juntos)"},
		{"blind index without envelope", func([]driver.Value) []driver.Value { return []driver.Value{nil, nil, nil} },
			"fleet: la fila tiene índice ciego de self_pn pero no sobre; no hay destino para el saludo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.fake.script(reply{rows: [][]driver.Value{c.row(envelopeRow(t, f.cipher, "34600111222"))}})
			to, pending, err := f.repo.PendingGreeting(context.Background(), pgTenant, pgEdge, pgSession)
			requireNotPending(t, to, pending)
			if err == nil || err.Error() != c.want {
				t.Errorf("err = %v, quería %q", err, c.want)
			}
		})
	}
}

// TestPostgresRepository_MarkGreeted: la sentencia exacta; marked=true solo si ESTA llamada puso
// la marca (una fila), y false sin error si otro latido llegó antes (cero filas).
func TestPostgresRepository_MarkGreeted(t *testing.T) {
	for affected, want := range map[int64]bool{0: false, 1: true} {
		f := newFixture(t, reply{affected: affected})
		marked, err := f.repo.MarkGreeted(context.Background(), pgTenant, pgEdge, pgSession)
		if marked != want || err != nil {
			t.Errorf("MarkGreeted con %d filas = (%v, %v), quería (%v, nil)", affected, marked, err, want)
		}
		requireStatements(t, f.fake, statement{sqlMarkGreeted, []driver.Value{pgTenant, pgEdge, pgSession}})
	}
}

// TestPostgresRepository_MarkGreeted_Sentinel: el centinela está en la sentencia, y la marca no
// toca el perfil ni el reloj del eje (profile_updated_at solo lo mueve SetProfile).
func TestPostgresRepository_MarkGreeted_Sentinel(t *testing.T) {
	if !strings.HasSuffix(sqlMarkGreeted, "\n\t\t  AND greeted_at IS NULL\n\t") {
		t.Errorf("la sentencia no termina en el centinela greeted_at IS NULL:\n%s", sqlMarkGreeted)
	}
	if strings.Contains(sqlMarkGreeted, "profile") {
		t.Errorf("la sentencia toca el perfil o su reloj:\n%s", sqlMarkGreeted)
	}
}

// TestPostgresRepository_MarkGreeted_Errors: el fallo del driver y el de leer las filas afectadas
// vuelven envueltos, cada uno con su prefijo, y con marked=false.
func TestPostgresRepository_MarkGreeted_Errors(t *testing.T) {
	cases := []struct {
		name   string
		reply  reply
		prefix string
	}{
		{"driver error", reply{err: errBoom}, "fleet: marcar saludo entregado: "},
		{"rows affected error", reply{affected: 1, affectedErr: errBoom}, "fleet: filas afectadas al marcar el saludo: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, c.reply)
			marked, err := f.repo.MarkGreeted(context.Background(), pgTenant, pgEdge, pgSession)
			if marked {
				t.Error("marked = true con error, quería false")
			}
			requireWrapped(t, err, errBoom, c.prefix)
		})
	}
}
