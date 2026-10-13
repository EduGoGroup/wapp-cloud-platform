package contact

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Aserciones de compilación de lo que PostgresResolver promete sin base de datos: es un Resolver
// (el puerto), el constructor recibe el pool, el cipher y el KeyProvider y NO devuelve un error (no
// valida sus argumentos), y Destino tiene la firma del puerto. Destino y el SQL de Resolve solo
// se ejercitan contra un Postgres real: la suite contacthelpertest.Contrato (T1.13, T1.18) y F9.
var _ Resolver = (*PostgresResolver)(nil)

var _ func(*sql.DB, *crypto.FieldCipher, crypto.KeyProvider) *PostgresResolver = NewPostgresResolver

var _ func(*PostgresResolver, context.Context, string, string) (Ref, error) = (*PostgresResolver).Destino

// R-18: con la lista vacía (nil o []Ref{}) Resolve devuelve ErrNoRefs, con el texto observable
// exacto, el contactID "" y SIN tocar la base de datos. El resolver se construye con db, cipher y kp
// nil: cualquier acceso a uno de los tres fallaría con un pánico, así que un resultado limpio prueba
// que ni la transacción, ni el cifrado, ni el índice ciego se tocaron.
func TestPostgresResolver_SinRefs_ErrNoRefs(t *testing.T) {
	const (
		tenant   = "6f0f3c2e-8d1b-4a57-9c64-2b7a1d5e9f10"
		pushName = "Ana Perez"
		texto    = "contact: se requiere al menos una contact_ref"
	)
	r := NewPostgresResolver(nil, nil, nil)
	for _, c := range []struct {
		caso string
		refs []Ref
	}{
		{"lista nil", nil},
		{"lista vacía", []Ref{}},
	} {
		t.Run(c.caso, func(t *testing.T) {
			id, err := r.Resolve(t.Context(), tenant, c.refs, pushName)
			if !errors.Is(err, ErrNoRefs) {
				t.Fatalf("Resolve sin refs = %q, %v; quiere un error que envuelva ErrNoRefs", id, err)
			}
			if err.Error() != texto {
				t.Errorf("texto observable = %q; quiere %q", err.Error(), texto)
			}
			if id != "" {
				t.Errorf("con error el contactID debe ser \"\"; dio %q", id)
			}
		})
	}
}

// R1.4.d: el error de Resolve sin refs no filtra PII. Con un pushName y un tenant en la mano, ni
// uno ni otro aparecen en el texto del error.
func TestPostgresResolver_SinRefs_NoFiltraPII(t *testing.T) {
	const (
		tenant   = "6f0f3c2e-8d1b-4a57-9c64-2b7a1d5e9f10"
		pushName = "Ana Perez"
	)
	_, err := NewPostgresResolver(nil, nil, nil).Resolve(t.Context(), tenant, nil, pushName)
	if err == nil {
		t.Fatal("Resolve sin refs no dio error")
	}
	for _, secreto := range []string{pushName, tenant} {
		if strings.Contains(err.Error(), secreto) {
			t.Errorf("el error contiene %q, que no debe salir en un mensaje: %v", secreto, err)
		}
	}
}

// El constructor no valida sus argumentos: con db, cipher y kp nil devuelve igualmente un
// resolver, sin pánico y sin error (su firma no tiene uno: ver las aserciones de compilación).
func TestNewPostgresResolver_NoValidaArgumentos(t *testing.T) {
	if r := NewPostgresResolver(nil, nil, nil); r == nil {
		t.Fatal("NewPostgresResolver(nil, nil, nil) = nil; quiere un *PostgresResolver: el constructor no valida")
	}
}

// ── Funciones puras del adaptador (sin base de datos) ──────────────────────────────────────────

const pgTestTenant = "6f0f3c2e-8d1b-4a57-9c64-2b7a1d5e9f10"

// key32 devuelve una clave de 32 bytes rellena con b, en base64 estándar.
func key32(b byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

// compatKP es un KeyProvider en modo compat (una sola KEK maestra, key_id "1") con una indexKey
// explícita, como en producción pero con claves fijas.
func compatKP(t *testing.T) crypto.KeyProvider {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{MasterB64: key32(0x01), IndexB64: key32(0x44)})
	if err != nil {
		t.Fatalf("KeyProvider compat: %v", err)
	}
	return kp
}

// keyringKP es un KeyProvider con keyring versionado y la KEK current dada.
func keyringKP(t *testing.T, keyring, current string) crypto.KeyProvider {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{KeyringB64: keyring, CurrentID: current, IndexB64: key32(0x44)})
	if err != nil {
		t.Fatalf("KeyProvider %s (current %s): %v", keyring, current, err)
	}
	return kp
}

// errWrap es el fallo del stack de claves que simula failingKP.
var errWrap = errors.New("kms caído")

// failingKP es un KeyProvider cuyo WrapDEK siempre falla: hace fallar cipher.Encrypt.
type failingKP struct{}

func (failingKP) WrapDEK([]byte) ([]byte, string, error)   { return nil, "", errWrap }
func (failingKP) UnwrapDEK([]byte, string) ([]byte, error) { return nil, errWrap }
func (failingKP) BlindIndex(string, string) string         { return "bidx" }
func (failingKP) CurrentKeyID() string                     { return "X" }

func TestPostgres_encodeRef_BlindIndexIsKPOfTenantAndValue(t *testing.T) {
	kp := compatKP(t)
	ref := Ref{Kind: KindPhoneE164, Value: "573001112233"}
	bidx, _, _, _, err := encodeRef(kp, crypto.NewFieldCipher(kp), pgTestTenant, ref)
	if err != nil {
		t.Fatalf("encodeRef: %v", err)
	}
	if want := kp.BlindIndex(pgTestTenant, ref.Value); bidx != want {
		t.Errorf("bidx = %q; quiere kp.BlindIndex(tenant, value) = %q", bidx, want)
	}
}

func TestPostgres_encodeRef_OtherTenantOtherBlindIndex(t *testing.T) {
	kp := compatKP(t)
	cipher := crypto.NewFieldCipher(kp)
	ref := Ref{Kind: KindPhoneE164, Value: "573001112233"}
	a, _, _, _, errA := encodeRef(kp, cipher, pgTestTenant, ref)
	b, _, _, _, errB := encodeRef(kp, cipher, "0b7e2d9a-1c3f-4e8b-a5d6-7f9e0c1b2a34", ref)
	if errA != nil || errB != nil {
		t.Fatalf("encodeRef: %v / %v", errA, errB)
	}
	if a == b {
		t.Errorf("la misma ref en dos tenants dio el mismo value_bidx %q; quiere índices distintos (N-01)", a)
	}
}

func TestPostgres_encodeRef_EncDoesNotContainValue(t *testing.T) {
	kp := compatKP(t)
	ref := Ref{Kind: KindPhoneE164, Value: "573001112233"}
	_, enc, dek, _, err := encodeRef(kp, crypto.NewFieldCipher(kp), pgTestTenant, ref)
	if err != nil {
		t.Fatalf("encodeRef: %v", err)
	}
	if len(enc) == 0 || len(dek) == 0 {
		t.Fatalf("enc y dek deben venir poblados; enc=%d bytes, dek=%d bytes", len(enc), len(dek))
	}
	if bytes.Contains(enc, []byte(ref.Value)) {
		t.Errorf("value_enc contiene el value en claro (R-23)")
	}
}

func TestPostgres_encodeRef_RoundTripWithCurrentKEK(t *testing.T) {
	kp := compatKP(t)
	cipher := crypto.NewFieldCipher(kp)
	ref := Ref{Kind: KindWALID, Value: "88887777"}
	_, enc, dek, kekID, err := encodeRef(kp, cipher, pgTestTenant, ref)
	if err != nil {
		t.Fatalf("encodeRef: %v", err)
	}
	if kekID != "1" {
		t.Errorf("kekID = %q; en modo compat quiere \"1\" (la KEK vigente, R-24)", kekID)
	}
	if got, err := cipher.Decrypt(enc, dek, kekID); err != nil || got != ref.Value {
		t.Errorf("Decrypt(encodeRef) = %q, %v; quiere %q", got, err, ref.Value)
	}
}

func TestPostgres_encodeRef_EncryptErrorWrappedWithoutValue(t *testing.T) {
	ref := Ref{Kind: KindPhoneE164, Value: "573001112233"}
	bidx, enc, dek, kekID, err := encodeRef(failingKP{}, crypto.NewFieldCipher(failingKP{}), pgTestTenant, ref)
	if !errors.Is(err, errWrap) {
		t.Fatalf("encodeRef con el cifrado roto = %v; quiere un error que envuelva el del stack de claves", err)
	}
	if !strings.HasPrefix(err.Error(), "contact: cifrar value: ") {
		t.Errorf("texto = %q; quiere el prefijo «contact: cifrar value: »", err.Error())
	}
	if strings.Contains(err.Error(), ref.Value) {
		t.Errorf("el error contiene el value (R1.4.d): %v", err)
	}
	if bidx != "" || enc != nil || dek != nil || kekID != "" {
		t.Errorf("con error quiere las cuatro piezas vacías; dio %q, %v, %v, %q", bidx, enc, dek, kekID)
	}
}

// R-26: con el nombre vacío no se cifra nada. El cipher nil lo prueba: cualquier uso daría pánico.
func TestPostgres_pushNameEnvelope_EmptyNameNoEncryption(t *testing.T) {
	enc, dek, kekID, err := pushNameEnvelope(nil, "")
	if err != nil || enc != nil || dek != nil || kekID != "" {
		t.Errorf("pushNameEnvelope(\"\") = %v, %v, %q, %v; quiere tres piezas vacías y nil", enc, dek, kekID, err)
	}
}

func TestPostgres_pushNameEnvelope_NameSealedInThreePieces(t *testing.T) {
	kp := compatKP(t)
	cipher := crypto.NewFieldCipher(kp)
	const name = "Ana Perez"
	enc, dek, kekID, err := pushNameEnvelope(cipher, name)
	if err != nil {
		t.Fatalf("pushNameEnvelope: %v", err)
	}
	if len(enc) == 0 || len(dek) == 0 || kekID != "1" {
		t.Fatalf("quiere las tres piezas pobladas con kekID \"1\" (compat); dio enc=%d, dek=%d, kekID=%q", len(enc), len(dek), kekID)
	}
	if bytes.Contains(enc, []byte(name)) {
		t.Errorf("push_name_enc contiene el nombre en claro")
	}
	if got, err := cipher.Decrypt(enc, dek, kekID); err != nil || got != name {
		t.Errorf("Decrypt(sobre) = %q, %v; quiere %q", got, err, name)
	}
}

func TestPostgres_pushNameEnvelope_EncryptErrorWithoutName(t *testing.T) {
	const name = "Ana Perez"
	enc, dek, kekID, err := pushNameEnvelope(crypto.NewFieldCipher(failingKP{}), name)
	if !errors.Is(err, errWrap) {
		t.Fatalf("pushNameEnvelope con el cifrado roto = %v; quiere un error que envuelva el del stack", err)
	}
	if !strings.HasPrefix(err.Error(), "contact: cifrar push_name: ") {
		t.Errorf("texto = %q; quiere el prefijo «contact: cifrar push_name: »", err.Error())
	}
	if strings.Contains(err.Error(), name) {
		t.Errorf("el error contiene el nombre (CERO PII): %v", err)
	}
	if enc != nil || dek != nil || kekID != "" {
		t.Errorf("con error quiere las tres piezas vacías; dio %v, %v, %q", enc, dek, kekID)
	}
}

func TestPostgres_nullStr(t *testing.T) {
	if got := nullStr(""); got.Valid {
		t.Errorf("nullStr(\"\") = %+v; quiere NULL (Valid=false)", got)
	}
	if got := nullStr("1"); !got.Valid || got.String != "1" {
		t.Errorf("nullStr(\"1\") = %+v; quiere {String:\"1\", Valid:true}", got)
	}
}

func TestPostgres_pickCanonical(t *testing.T) {
	t0 := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	const (
		idA = "1a1a1a1a-0000-4000-8000-000000000001"
		idB = "2b2b2b2b-0000-4000-8000-000000000002"
		idC = "3c3c3c3c-0000-4000-8000-000000000003"
	)
	for _, c := range []struct {
		name  string
		cands []canonicalCandidate
		want  string
	}{
		{"oldest created_at wins", []canonicalCandidate{{idA, t0.Add(time.Second)}, {idB, t0}}, idB},
		{"oldest wins whatever the input order", []canonicalCandidate{{idB, t0}, {idA, t0.Add(time.Second)}}, idB},
		{"tie breaks by lower id", []canonicalCandidate{{idC, t0}, {idA, t0}, {idB, t0}}, idA},
		{"an older one beats a lower id", []canonicalCandidate{{idA, t0}, {idC, t0.Add(-time.Nanosecond)}}, idC},
		{"single candidate", []canonicalCandidate{{idB, t0}}, idB},
		{"no candidates", nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := pickCanonical(c.cands); got != c.want {
				t.Errorf("pickCanonical = %q; quiere %q", got, c.want)
			}
		})
	}
}

// fakeRows simula el cursor de Destino: filas (kind, value_enc, value_dek, value_kek_id), un
// error de Scan en la fila scanErrAt (si >= 0) y el error final del cursor.
type fakeRows struct {
	rows      []fakeRow
	next      int
	scanErrAt int
	scanErr   error
	err       error
}

// fakeRow es una fila de public.contacts tal como la lee Destino.
type fakeRow struct {
	kind     string
	enc, dek []byte
	kekID    string
}

// errFakeDest es el error de fakeRows.Scan si los destinos no son los que escanea openRows.
var errFakeDest = errors.New("fakeRows: destinos de Scan inesperados")

func (f *fakeRows) Next() bool {
	if f.next >= len(f.rows) {
		return false
	}
	f.next++
	return true
}

func (f *fakeRows) Scan(dest ...any) error {
	i := f.next - 1
	if i == f.scanErrAt {
		return f.scanErr
	}
	if len(dest) != 4 {
		return errFakeDest
	}
	kind, ok1 := dest[0].(*string)
	enc, ok2 := dest[1].(*[]byte)
	dek, ok3 := dest[2].(*[]byte)
	kekID, ok4 := dest[3].(*string)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return errFakeDest
	}
	row := f.rows[i]
	*kind, *enc, *dek, *kekID = row.kind, row.enc, row.dek, row.kekID
	return nil
}

func (f *fakeRows) Err() error { return f.err }

func newFakeRows(rows ...fakeRow) *fakeRows { return &fakeRows{rows: rows, scanErrAt: -1} }

// sealRow cifra value con cipher y devuelve la fila tal como la guardaría Postgres.
func sealRow(t *testing.T, cipher *crypto.FieldCipher, kind, value string) fakeRow {
	t.Helper()
	enc, dek, kekID, err := cipher.Encrypt(value)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	return fakeRow{kind: kind, enc: enc, dek: dek, kekID: kekID}
}

// R-24: cada fila se abre con SU kek_id. Dos filas cerradas por KEK distintas (A y B) se abren con
// un keyring que tiene las dos, aunque la vigente sea solo una.
func TestPostgres_openRows_EachRowWithItsOwnKEK(t *testing.T) {
	keyring := "A:" + key32(0x11) + ",B:" + key32(0x22)
	cipherA := crypto.NewFieldCipher(keyringKP(t, keyring, "A"))
	cipherB := crypto.NewFieldCipher(keyringKP(t, keyring, "B"))
	rowA := sealRow(t, cipherA, KindPhoneE164, "573001112233")
	rowB := sealRow(t, cipherB, KindWALID, "88887777")
	if rowA.kekID != "A" || rowB.kekID != "B" {
		t.Fatalf("preparación: kek_id de las filas = %q, %q; quiere A y B", rowA.kekID, rowB.kekID)
	}
	refs, err := openRows(cipherB, newFakeRows(rowA, rowB), pgTestTenant)
	if err != nil {
		t.Fatalf("openRows: %v", err)
	}
	want := []Ref{{KindPhoneE164, "573001112233"}, {KindWALID, "88887777"}}
	if len(refs) != len(want) || refs[0] != want[0] || refs[1] != want[1] {
		t.Errorf("openRows = %v; quiere %v, en el orden de las filas", refs, want)
	}
}

func TestPostgres_openRows_KEKMissingFromKeyring(t *testing.T) {
	const value = "573001112233"
	cipherA := crypto.NewFieldCipher(keyringKP(t, "A:"+key32(0x11), "A"))
	cipherB := crypto.NewFieldCipher(keyringKP(t, "B:"+key32(0x22), "B"))
	refs, err := openRows(cipherB, newFakeRows(
		sealRow(t, cipherB, KindWALID, "88887777"),
		sealRow(t, cipherA, KindPhoneE164, value),
	), pgTestTenant)
	if err == nil || !strings.HasPrefix(err.Error(), "contact: descifrar value: ") {
		t.Fatalf("openRows con una KEK ausente = %v, %v; quiere «contact: descifrar value: …»", refs, err)
	}
	if refs != nil {
		t.Errorf("con una fila que no abre no hay destino parcial; dio %v", refs)
	}
	if strings.Contains(err.Error(), value) {
		t.Errorf("el error contiene el value (R1.4.d): %v", err)
	}
}

func TestPostgres_openRows_ScanError(t *testing.T) {
	errScan := errors.New("columna rara")
	cipher := crypto.NewFieldCipher(compatKP(t))
	rows := newFakeRows(sealRow(t, cipher, KindPhoneE164, "573001112233"))
	rows.scanErrAt, rows.scanErr = 0, errScan
	refs, err := openRows(cipher, rows, pgTestTenant)
	if !errors.Is(err, errScan) || !strings.HasPrefix(err.Error(), "contact: escanear ref: ") || refs != nil {
		t.Errorf("openRows con fallo de Scan = %v, %v; quiere nil y «contact: escanear ref: %%w»", refs, err)
	}
}

func TestPostgres_openRows_CursorError(t *testing.T) {
	errCursor := errors.New("conexión perdida")
	cipher := crypto.NewFieldCipher(compatKP(t))
	rows := newFakeRows(sealRow(t, cipher, KindPhoneE164, "573001112233"))
	rows.err = errCursor
	refs, err := openRows(cipher, rows, pgTestTenant)
	if !errors.Is(err, errCursor) || !strings.HasPrefix(err.Error(), "contact: iterar refs: ") || refs != nil {
		t.Errorf("openRows con fallo del cursor = %v, %v; quiere nil y «contact: iterar refs: %%w»", refs, err)
	}
}

// R-21: sin filas, ErrContactNotFound con el id entre comillas. El cipher nil prueba que no se
// descifra nada.
func TestPostgres_openRows_NoRowsContactNotFound(t *testing.T) {
	const id = "9d8c7b6a-5f4e-4d3c-8b2a-1f0e9d8c7b6a"
	refs, err := openRows(nil, newFakeRows(), id)
	if !errors.Is(err, ErrContactNotFound) || refs != nil {
		t.Fatalf("openRows sin filas = %v, %v; quiere nil y ErrContactNotFound", refs, err)
	}
	if want := `contact: contact_id no encontrado: "` + id + `"`; err.Error() != want {
		t.Errorf("texto = %q; quiere %q", err.Error(), want)
	}
}

// Hallazgo 15: el fallo de cerrar el cursor solo se ve si no hubo otro error, con su prefijo.
func TestPostgres_closeRowsErr(t *testing.T) {
	errBody := errors.New("del cuerpo")
	errClose := errors.New("del cierre")
	if got := closeRowsErr(nil, nil); got != nil {
		t.Errorf("sin errores = %v; quiere nil", got)
	}
	if got := closeRowsErr(errBody, nil); !errors.Is(got, errBody) || got.Error() != errBody.Error() {
		t.Errorf("solo el del cuerpo = %v; quiere ese mismo error", got)
	}
	if got := closeRowsErr(errBody, errClose); !errors.Is(got, errBody) || errors.Is(got, errClose) {
		t.Errorf("los dos = %v; quiere el del cuerpo y el del cierre descartado", got)
	}
	got := closeRowsErr(nil, errClose)
	if !errors.Is(got, errClose) || got.Error() != "contact: cerrar filas: del cierre" {
		t.Errorf("solo el del cierre = %v; quiere «contact: cerrar filas: %%w»", got)
	}
}
