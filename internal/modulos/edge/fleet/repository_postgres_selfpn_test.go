//go:build pendiente

package fleet

// Los tests de fichero de la mitad del self_pn de fleet.PostgresRepository: SetSelfPn y
// CountLiveBySelfPn. El número se guarda CIFRADO y se busca por ÍNDICE CIEGO del valor canónico;
// aquí se afirma, sin Postgres, lo que viaja al driver. Los auxiliares no exportados (el sobre y
// el recuento de fallos) tienen sus tests en repository_postgres_selfpn_envelope_test.go.

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Las dos sentencias, byte a byte y escritas a mano (ver repository_postgres_test.go).
const (
	// SetSelfPn: el sobre, el key_id y el índice ciego; la columna en claro ya no existe. La
	// guarda del WHERE deja entrar el UPDATE solo si cambió el número ($7) o la KEK ($6).
	sqlSetSelfPn = "\n" +
		"\t\tUPDATE public.fleet_sessions\n" +
		"\t\tSET self_pn_enc    = $4,\n" +
		"\t\t    self_pn_dek    = $5,\n" +
		"\t\t    self_pn_kek_id = $6,\n" +
		"\t\t    self_pn_bidx   = $7,\n" +
		"\t\t    updated_at     = now()\n" +
		"\t\tWHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3\n" +
		"\t\t  AND (self_pn_bidx   IS DISTINCT FROM $7\n" +
		"\t\t    OR self_pn_kek_id IS DISTINCT FROM $6)\n" +
		"\t"
	sqlCountLive = "\n" +
		"\t\tSELECT count(*) FROM public.fleet_sessions\n" +
		"\t\tWHERE tenant_id = $1 AND self_pn_bidx = $2 AND state <> 'loggedout'\n" +
		"\t"
)

// El mismo número en la grafía que reporta el Edge y en su forma canónica (solo dígitos).
const (
	pnSpelled   = "+34 600-111-222"
	pnCanonical = "34600111222"
)

// errWrap es el fallo del stack de claves que simula failingKP.
var errWrap = errors.New("kms caído")

// failingKP es un KeyProvider cuyo WrapDEK siempre falla: hace fallar cipher.Encrypt.
type failingKP struct{}

func (failingKP) WrapDEK([]byte) ([]byte, string, error)   { return nil, "", errWrap }
func (failingKP) UnwrapDEK([]byte, string) ([]byte, error) { return nil, errWrap }
func (failingKP) BlindIndex(string, string) string         { return "bidx" }
func (failingKP) CurrentKeyID() string                     { return "X" }

// selfPnWrite son los cuatro argumentos cifrados de un SetSelfPn tal como llegaron al driver.
type selfPnWrite struct {
	enc, dek    []byte
	kekID, bidx string
}

// writeSelfPn llama a SetSelfPn sobre un montaje nuevo y devuelve lo que llegó al driver, tras
// afirmar que fue UNA sentencia, la exacta, con la identidad en $1-$3.
func writeSelfPn(t *testing.T, f fixture, tenantID, selfPn string) selfPnWrite {
	t.Helper()
	before := len(f.fake.seen())
	if err := f.repo.SetSelfPn(context.Background(), tenantID, pgEdge, pgSession, selfPn); err != nil {
		t.Fatalf("SetSelfPn(%q): error inesperado %v", selfPn, err)
	}
	stmts := f.fake.seen()[before:]
	if len(stmts) != 1 {
		t.Fatalf("SetSelfPn(%q) emitió %d sentencias, quería 1", selfPn, len(stmts))
	}
	if stmts[0].query != sqlSetSelfPn {
		t.Errorf("SQL emitido:\n%q\nquería, byte a byte:\n%q", stmts[0].query, sqlSetSelfPn)
	}
	args := stmts[0].args
	if len(args) != 7 || args[0] != tenantID || args[1] != pgEdge || args[2] != pgSession {
		t.Fatalf("argumentos = %#v, quería siete con (tenant, edge, sesión) en $1-$3", args)
	}
	var w selfPnWrite
	var ok [4]bool
	w.enc, ok[0] = args[3].([]byte)
	w.dek, ok[1] = args[4].([]byte)
	w.kekID, ok[2] = args[5].(string)
	w.bidx, ok[3] = args[6].(string)
	if ok != [4]bool{true, true, true, true} {
		t.Fatalf("tipos de $4-$7 = %T, %T, %T, %T; quería []byte, []byte, string, string", args[3], args[4], args[5], args[6])
	}
	return w
}

// TestPostgresRepository_SetSelfPn_EmptyIsNoOp: sin número no se escribe nada (no pisa uno bueno).
func TestPostgresRepository_SetSelfPn_EmptyIsNoOp(t *testing.T) {
	f := newFixture(t)
	if err := f.repo.SetSelfPn(context.Background(), pgTenant, pgEdge, pgSession, ""); err != nil {
		t.Errorf("SetSelfPn vacío: error inesperado %v", err)
	}
	requireStatements(t, f.fake)
}

// TestPostgresRepository_SetSelfPn_WritesTheEnvelope: $6 es el key_id de la KEK current, $7 el
// índice ciego de la forma CANÓNICA bajo ese tenant, y $4/$5 —que no son deterministas— un sobre
// que, abierto con el mismo cifrador, da el número CANÓNICO y no la grafía recibida. Cero filas
// afectadas (la sesión aún no se registró) no es un error.
func TestPostgresRepository_SetSelfPn_WritesTheEnvelope(t *testing.T) {
	f := newFixture(t)
	w := writeSelfPn(t, f, pgTenant, pnSpelled)

	if w.kekID != fixtureCurrent || w.kekID != f.kp.CurrentKeyID() {
		t.Errorf("$6 = %q, quería el key_id de la KEK current %q", w.kekID, fixtureCurrent)
	}
	if want := f.kp.BlindIndex(pgTenant, pnCanonical); w.bidx != want {
		t.Errorf("$7 = %q, quería el índice ciego de (tenant, número canónico) %q", w.bidx, want)
	}
	if w.bidx == f.kp.BlindIndex(pgTenant, pnSpelled) || w.bidx == f.kp.BlindIndex(pnCanonical, pgTenant) {
		t.Errorf("$7 = %q es el índice de la grafía sin normalizar, o el de (número, tenant) al revés", w.bidx)
	}
	plain, err := f.cipher.Decrypt(w.enc, w.dek, w.kekID)
	if err != nil {
		t.Fatalf("el sobre de $4/$5 no abre con la KEK de $6: %v", err)
	}
	if plain != pnCanonical {
		t.Errorf("el sobre guarda %q, quería el número canónico %q", plain, pnCanonical)
	}
}

// TestPostgresRepository_SetSelfPn_SpellingsCollapse: dos grafías del mismo número dan el MISMO
// índice ciego (si no, el tope de dispositivos contaría dos veces al mismo teléfono); otro número
// u otro tenant dan otro.
func TestPostgresRepository_SetSelfPn_SpellingsCollapse(t *testing.T) {
	f := newFixture(t)
	const otherTenant = "0d8f6a0e-51f4-4b7b-9d0a-7d7c1f2e3a44"
	base := writeSelfPn(t, f, pgTenant, pnCanonical)
	for _, spelling := range []string{pnSpelled, "+34600111222", "34 600 111 222", "(34) 600.111.222"} {
		w := writeSelfPn(t, f, pgTenant, spelling)
		if w.bidx != base.bidx {
			t.Errorf("la grafía %q dio el índice %q, quería el mismo que la canónica %q", spelling, w.bidx, base.bidx)
		}
		if plain, err := f.cipher.Decrypt(w.enc, w.dek, w.kekID); err != nil || plain != pnCanonical {
			t.Errorf("la grafía %q guardó (%q, %v), quería el canónico %q", spelling, plain, err, pnCanonical)
		}
	}
	if w := writeSelfPn(t, f, pgTenant, "34600111223"); w.bidx == base.bidx {
		t.Error("otro número dio el mismo índice ciego")
	}
	if w := writeSelfPn(t, f, otherTenant, pnCanonical); w.bidx == base.bidx {
		t.Error("el mismo número bajo otro tenant dio el mismo índice ciego")
	}
}

// TestPostgresRepository_SetSelfPn_NumberNeverTravelsInClear: ningún argumento de la sentencia
// lleva los dígitos del número, ni en la grafía recibida ni en la canónica.
func TestPostgresRepository_SetSelfPn_NumberNeverTravelsInClear(t *testing.T) {
	f := newFixture(t)
	writeSelfPn(t, f, pgTenant, pnSpelled)
	for i, arg := range f.fake.seen()[0].args {
		var raw []byte
		switch v := arg.(type) {
		case string:
			raw = []byte(v)
		case []byte:
			raw = v
		}
		if bytes.Contains(raw, []byte(pnCanonical)) || bytes.Contains(raw, []byte(pnSpelled)) {
			t.Errorf("el argumento $%d lleva el número en claro", i+1)
		}
	}
}

// TestPostgresRepository_SetSelfPn_GuardAgainstRewriting: el sobre sale DISTINTO en cada llamada
// (DEK fresca) aunque el número sea el mismo, y por eso la sentencia lleva la guarda con sus dos
// mitades: solo reescribe si cambió el índice ciego o si la fila la envolvió otra KEK. Y no
// escribe ninguna columna en claro.
func TestPostgresRepository_SetSelfPn_GuardAgainstRewriting(t *testing.T) {
	f := newFixture(t)
	first := writeSelfPn(t, f, pgTenant, pnSpelled)
	second := writeSelfPn(t, f, pgTenant, pnSpelled)
	if bytes.Equal(first.enc, second.enc) || bytes.Equal(first.dek, second.dek) {
		t.Error("dos escrituras del mismo número dieron el mismo sobre: la DEK no es fresca")
	}
	if first.bidx != second.bidx || first.kekID != second.kekID {
		t.Errorf("los comparandos de la guarda no son estables: bidx %q/%q, kek_id %q/%q",
			first.bidx, second.bidx, first.kekID, second.kekID)
	}
	for _, half := range []string{
		"AND (self_pn_bidx   IS DISTINCT FROM $7\n",
		"OR self_pn_kek_id IS DISTINCT FROM $6)\n",
	} {
		if !strings.Contains(sqlSetSelfPn, half) {
			t.Errorf("a la sentencia le falta una mitad de la guarda: %q", half)
		}
	}
	if strings.Contains(sqlSetSelfPn, "self_pn =") || strings.Contains(sqlSetSelfPn, "self_pn  ") {
		t.Errorf("la sentencia escribe la columna en claro self_pn:\n%s", sqlSetSelfPn)
	}
}

// unnormalizable son números que contact.Normalize rechaza: sin un solo dígito, o con más de 15.
var unnormalizable = []string{"sin-digitos", "+", "1234567890123456"}

// TestPostgresRepository_SetSelfPn_Unnormalizable: un número que no normaliza devuelve error, con
// su prefijo y SIN el número dentro, y no llega nada al driver.
func TestPostgresRepository_SetSelfPn_Unnormalizable(t *testing.T) {
	for _, selfPn := range unnormalizable {
		f := newFixture(t)
		err := f.repo.SetSelfPn(context.Background(), pgTenant, pgEdge, pgSession, selfPn)
		requireWrapped(t, err, contact.ErrInvalidRef, "fleet: normalizar self_pn: ")
		if strings.Contains(err.Error(), selfPn) {
			t.Errorf("el error lleva el valor recibido: %q", err)
		}
		requireStatements(t, f.fake)
	}
}

// TestPostgresRepository_SetSelfPn_Errors: si el cifrado falla no se escribe nada, y el fallo del
// driver vuelve envuelto; cada uno con su prefijo.
func TestPostgresRepository_SetSelfPn_Errors(t *testing.T) {
	t.Run("cipher fails", func(t *testing.T) {
		fake := &fakeDB{}
		repo := NewPostgresRepository(fake.open(t), crypto.NewFieldCipher(failingKP{}), failingKP{})
		err := repo.SetSelfPn(context.Background(), pgTenant, pgEdge, pgSession, pnSpelled)
		requireWrapped(t, err, errWrap, "fleet: cifrar self_pn: ")
		requireStatements(t, fake)
	})
	t.Run("driver fails", func(t *testing.T) {
		f := newFixture(t, reply{err: errBoom})
		err := f.repo.SetSelfPn(context.Background(), pgTenant, pgEdge, pgSession, pnSpelled)
		requireWrapped(t, err, errBoom, "fleet: fijar self_pn: ")
	})
}

// TestPostgresRepository_CountLiveBySelfPn_EmptyIsZero: sin número no hay nada que contar, y no
// se consulta.
func TestPostgresRepository_CountLiveBySelfPn_EmptyIsZero(t *testing.T) {
	f := newFixture(t, reply{rows: [][]driver.Value{{int64(9)}}})
	n, err := f.repo.CountLiveBySelfPn(context.Background(), pgTenant, "")
	if n != 0 || err != nil {
		t.Errorf("CountLiveBySelfPn vacío = (%d, %v), quería (0, nil)", n, err)
	}
	requireStatements(t, f.fake)
}

// TestPostgresRepository_CountLiveBySelfPn: la sentencia exacta, con el tenant y el índice ciego
// de la forma CANÓNICA (la misma que escribe SetSelfPn: si escritor y lector normalizaran
// distinto, el conteo daría 0 siempre); y devuelve lo que cuenta la base.
func TestPostgresRepository_CountLiveBySelfPn(t *testing.T) {
	for _, spelling := range []string{pnCanonical, pnSpelled, "+34600111222"} {
		f := newFixture(t, reply{rows: [][]driver.Value{{int64(3)}}})
		n, err := f.repo.CountLiveBySelfPn(context.Background(), pgTenant, spelling)
		if n != 3 || err != nil {
			t.Errorf("CountLiveBySelfPn(%q) = (%d, %v), quería (3, nil)", spelling, n, err)
		}
		requireStatements(t, f.fake, statement{sqlCountLive, []driver.Value{pgTenant, f.kp.BlindIndex(pgTenant, pnCanonical)}})

		written := writeSelfPn(t, f, pgTenant, pnSpelled)
		if read := f.fake.seen()[0].args[1]; read != written.bidx {
			t.Errorf("el lector busca por %q y el escritor guardó %q: no casarían nunca", read, written.bidx)
		}
	}
}

// TestPostgresRepository_CountLiveBySelfPn_Errors: un número que no normaliza es un ERROR y no un
// cero («no puedo contar» ≠ «hay cero»), sin consultar; y el fallo del driver vuelve envuelto.
func TestPostgresRepository_CountLiveBySelfPn_Errors(t *testing.T) {
	for _, selfPn := range unnormalizable {
		f := newFixture(t, reply{rows: [][]driver.Value{{int64(9)}}})
		n, err := f.repo.CountLiveBySelfPn(context.Background(), pgTenant, selfPn)
		if n != 0 {
			t.Errorf("CountLiveBySelfPn(%q) = %d con error, quería 0", selfPn, n)
		}
		requireWrapped(t, err, contact.ErrInvalidRef, "fleet: normalizar self_pn para contar: ")
		requireStatements(t, f.fake)
	}
	f := newFixture(t, reply{err: errBoom})
	n, err := f.repo.CountLiveBySelfPn(context.Background(), pgTenant, pnSpelled)
	if n != 0 {
		t.Errorf("CountLiveBySelfPn = %d con error del driver, quería 0", n)
	}
	requireWrapped(t, err, errBoom, "fleet: contar sesiones vivas por self_pn: ")
}
