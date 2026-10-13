package fleet

// Tests de los auxiliares no exportados de repository_postgres_selfpn.go (nacen con el verde, 05
// E-4): abrir el sobre de una fila y el recuento de los que no abren. Llevan regla de negocio
// —qué es «todavía no hay número» y qué es un dato corrupto; una línea de aviso por llamada— que
// desde el contrato se diagnosticaría mal.

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

const incompleteEnvelope = "fleet: sobre de self_pn incompleto (enc/dek/kek_id no viajan juntos)"

// sealed sella plain con cipher y devuelve el sobre como lo trae una fila.
func sealed(t *testing.T, cipher *crypto.FieldCipher, plain string) (enc, dek []byte, kekID sql.NullString) {
	t.Helper()
	enc, dek, id, err := cipher.Encrypt(plain)
	if err != nil {
		t.Fatalf("sellar %q: %v", plain, err)
	}
	return enc, dek, sql.NullString{String: id, Valid: true}
}

// TestDecryptSelfPn_AbsentEnvelope: las tres columnas NULL (o vacías) son «todavía no hay
// número»: cadena vacía y sin error.
func TestDecryptSelfPn_AbsentEnvelope(t *testing.T) {
	f := newFixture(t)
	for _, empty := range [][]byte{nil, {}} {
		pn, err := f.repo.decryptSelfPn(empty, empty, sql.NullString{})
		if pn != "" || err != nil {
			t.Errorf("sobre ausente = (%q, %v), quería (\"\", nil)", pn, err)
		}
	}
}

// TestDecryptSelfPn_IncompleteEnvelope: si falta una o dos de las tres piezas, el sobre está a
// medias y eso es un error con su texto literal: NO se trata como ausente.
func TestDecryptSelfPn_IncompleteEnvelope(t *testing.T) {
	f := newFixture(t)
	enc, dek, kekID := sealed(t, f.cipher, pnCanonical)
	none := sql.NullString{}
	cases := []struct {
		name     string
		enc, dek []byte
		kekID    sql.NullString
	}{
		{"only enc", enc, nil, none},
		{"only dek", nil, dek, none},
		{"only kek id", nil, nil, kekID},
		{"missing enc", nil, dek, kekID},
		{"missing dek", enc, nil, kekID},
		{"missing kek id", enc, dek, none},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pn, err := f.repo.decryptSelfPn(c.enc, c.dek, c.kekID)
			if pn != "" || err == nil || err.Error() != incompleteEnvelope {
				t.Errorf("sobre incompleto = (%q, %v), quería (\"\", %q)", pn, err, incompleteEnvelope)
			}
		})
	}
}

// TestDecryptSelfPn_Opens: el sobre se abre con la KEK que envolvió ESA fila y no con la current:
// tras una rotación conviven filas de varias KEK.
func TestDecryptSelfPn_Opens(t *testing.T) {
	f := newFixture(t)
	retired := crypto.NewFieldCipher(keyringKP(t, fixtureKeyring, "A"))
	for name, cipher := range map[string]*crypto.FieldCipher{"current kek": f.cipher, "retired kek": retired} {
		enc, dek, kekID := sealed(t, cipher, pnCanonical)
		pn, err := f.repo.decryptSelfPn(enc, dek, kekID)
		if pn != pnCanonical || err != nil {
			t.Errorf("%s (key_id %q): (%q, %v), quería (%q, nil)", name, kekID.String, pn, err, pnCanonical)
		}
	}
}

// TestDecryptSelfPn_DoesNotOpen: una KEK que no está en el keyring, o un sobre manipulado, es un
// error envuelto con su prefijo, y no devuelve nada.
func TestDecryptSelfPn_DoesNotOpen(t *testing.T) {
	f := newFixture(t)
	enc, dek, kekID := sealed(t, foreignCipher(t), pnCanonical)
	pn, err := f.repo.decryptSelfPn(enc, dek, kekID)
	if pn != "" {
		t.Errorf("un sobre que no abre devolvió %q", pn)
	}
	requireWrapped(t, err, crypto.ErrKEKNotInKeyring, "fleet: descifrar self_pn: ")

	enc, dek, kekID = sealed(t, f.cipher, pnCanonical)
	enc[len(enc)-1] ^= 0xff
	pn, err = f.repo.decryptSelfPn(enc, dek, kekID)
	if pn != "" || err == nil {
		t.Errorf("un sobre manipulado = (%q, %v), quería (\"\", error)", pn, err)
	}
}

// TestSelfPnDecryptTally_NoFailuresIsSilent: sin fallos (el caso normal) no se emite nada.
func TestSelfPnDecryptTally_NoFailuresIsSilent(t *testing.T) {
	log := &spyLogger{}
	var tally selfPnDecryptTally
	tally.flush(log)
	if got := log.seen(); len(got) != 0 {
		t.Errorf("un recuento sin fallos emitió %d avisos: %+v", len(got), got)
	}
}

// TestSelfPnDecryptTally_OneLineWithTheFirstSample: todos los fallos suman al contador, la
// muestra es la del PRIMERO (los siguientes no la pisan) y flush emite UNA línea con las doce
// claves y valores literales.
func TestSelfPnDecryptTally_OneLineWithTheFirstSample(t *testing.T) {
	firstErr, laterErr := errors.New("primero"), errors.New("después")
	log := &spyLogger{}
	var tally selfPnDecryptTally
	tally.record("t-1", "e-1", "s-1", "k-1", firstErr)
	tally.record("t-2", "e-2", "s-2", "k-2", laterErr)
	tally.record("t-3", "e-3", "s-3", "k-3", laterErr)
	tally.flush(log)

	want := []warning{{
		msg: warnUndecryptable,
		args: []any{
			"filas_afectadas", 3,
			"muestra_tenant_id", "t-1", "muestra_edge_id", "e-1",
			"muestra_session_id", "s-1",
			"muestra_kek_id", "k-1", "error", firstErr,
		},
	}}
	if got := log.seen(); !reflect.DeepEqual(got, want) {
		t.Errorf("avisos =\n%#v\nquería\n%#v", got, want)
	}
}
