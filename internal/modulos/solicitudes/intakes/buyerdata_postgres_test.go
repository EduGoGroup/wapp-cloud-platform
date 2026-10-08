package intakes

// Los tests de fichero de PostgresBuyerData, con un driver de database/sql de mentira
// (buyerdata_postgres_fakedb_test.go) y un cifrador de verdad sobre un keyring de prueba: el
// texto EXACTO de cada sentencia, sus argumentos, la transacción, el sobre (qué se cifra y con
// qué KEK se descifra) y el texto de cada error. Que ese SQL haga en un Postgres de verdad lo que
// el contrato promete lo prueban los procesos de F9. Ningún test usa una clave real.
//
// 🔴 T-1: la «DEK» de este fichero es la llave por-fila del sobre de PII de negocio (data_dek),
// envuelta por la KEK del keyring de esta pieza. No es la DEK del ADR-0007.

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Las cuatro sentencias, byte a byte, escritas aquí a mano y NO copiadas de una constante de
// producción. Los espacios en blanco son los del literal del paquete viejo
// (internal/intakes/buyerdata.go @ 64c181a).
const (
	// La lectura de la fusión: bloquea la fila.
	bdSQLSelectForUpdate = "\n" +
		"\t\tSELECT data_enc, data_dek, data_kek_id\n" +
		"\t\tFROM public.intake_buyer_data\n" +
		"\t\tWHERE intake_id = $1\n" +
		"\t\tFOR UPDATE\n" +
		"\t"
	bdSQLUpdate = "\n" +
		"\t\t\tUPDATE public.intake_buyer_data\n" +
		"\t\t\tSET data_enc = $2, data_dek = $3, data_kek_id = $4, updated_at = now()\n" +
		"\t\t\tWHERE intake_id = $1\n" +
		"\t\t"
	bdSQLInsert = "\n" +
		"\t\t\tINSERT INTO public.intake_buyer_data (intake_id, data_enc, data_dek, data_kek_id)\n" +
		"\t\t\tVALUES ($1, $2, $3, $4)\n" +
		"\t\t"
	// La lectura del worker: la misma, SIN bloqueo.
	bdSQLGet = "\n" +
		"\t\tSELECT data_enc, data_dek, data_kek_id\n" +
		"\t\tFROM public.intake_buyer_data\n" +
		"\t\tWHERE intake_id = $1\n" +
		"\t"
)

const (
	bdIntake = "7c1f3f0e-2b0a-4d6e-9a53-0f4c2d9b7e21"
	// Valores RAROS a propósito: se buscan como subcadena en errores y argumentos.
	bdRUT     = "11.222.333-Q-xkq"
	bdAddress = "Camino del Alba 909-xkq, casa 3"

	bdKEKOldID  = "test-kek-old"
	bdKEKNewID  = "test-kek-new"
	bdKEKOldB64 = "ERERERERERERERERERERERERERERERERERERERERERE="
	bdKEKNewB64 = "IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiI="
	bdIndexB64  = "RERERERERERERERERERERERERERERERERERERERERES="
	bdKeyring   = bdKEKOldID + ":" + bdKEKOldB64 + "," + bdKEKNewID + ":" + bdKEKNewB64
)

// newBDCipher arma un cifrador de campo de verdad sobre el keyring de prueba, con currentID como
// la KEK que envuelve. Las dos KEK están siempre en el keyring: es una rotación a medias.
func newBDCipher(t *testing.T, currentID string) *crypto.FieldCipher {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{KeyringB64: bdKeyring, CurrentID: currentID, IndexB64: bdIndexB64})
	if err != nil {
		t.Fatalf("KeyProvider de prueba: %v", err)
	}
	return crypto.NewFieldCipher(kp)
}

// bdBrokenKeyProvider es un KeyProvider que no sabe envolver: hace fallar a FieldCipher.Encrypt.
type bdBrokenKeyProvider struct{ cause error }

func (b bdBrokenKeyProvider) WrapDEK([]byte) ([]byte, string, error)   { return nil, "", b.cause }
func (b bdBrokenKeyProvider) UnwrapDEK([]byte, string) ([]byte, error) { return nil, b.cause }
func (bdBrokenKeyProvider) BlindIndex(string, string) string           { return "" }
func (bdBrokenKeyProvider) CurrentKeyID() string                       { return bdKEKNewID }

// newBDStore monta el adaptador sobre el driver de mentira, con bdKEKNewID como KEK current.
func newBDStore(t *testing.T) (*PostgresBuyerData, *bdFakeDB) {
	t.Helper()
	fake := &bdFakeDB{}
	return NewPostgresBuyerData(fake.open(t), newBDCipher(t, bdKEKNewID)), fake
}

// bdSealedRow devuelve la fila que guardaría la base para ese texto en claro, sellada con la
// KEK kekID.
func bdSealedRow(t *testing.T, kekID, plain string) bdReply {
	t.Helper()
	enc, dek, gotID, err := newBDCipher(t, kekID).Encrypt(plain)
	if err != nil || gotID != kekID {
		t.Fatalf("sellando la fila de prueba: id=%q err=%v", gotID, err)
	}
	return bdReply{rows: [][]driver.Value{{enc, dek, kekID}}}
}

// bdOpenWrite afirma que la escritura lleva (intake_id, data_enc, data_dek, data_kek_id) y
// devuelve el checklist que hay DENTRO del sobre, abierto con la KEK que dice la fila.
func bdOpenWrite(t *testing.T, e bdEvent) BuyerData {
	t.Helper()
	if len(e.args) != 4 || e.args[0] != bdIntake {
		t.Fatalf("argumentos de la escritura = %#v, quería (intake_id, data_enc, data_dek, data_kek_id)", e.args)
	}
	enc, okEnc := e.args[1].([]byte)
	dek, okDEK := e.args[2].([]byte)
	kekID, okID := e.args[3].(string)
	if !okEnc || !okDEK || !okID {
		t.Fatalf("tipos de los argumentos = %T, %T, %T; quería []byte, []byte, string", e.args[1], e.args[2], e.args[3])
	}
	if kekID != bdKEKNewID {
		t.Errorf("data_kek_id = %q, quería la KEK current %q", kekID, bdKEKNewID)
	}
	for _, clear := range []string{bdRUT, bdAddress, "rut", "direccion"} {
		if bytes.Contains(enc, []byte(clear)) || bytes.Contains(dek, []byte(clear)) {
			t.Errorf("FUGA: %q viaja en claro en un argumento de la escritura", clear)
		}
	}
	plain, err := newBDCipher(t, bdKEKOldID).Decrypt(enc, dek, kekID)
	if err != nil {
		t.Fatalf("lo escrito no se abre con su data_kek_id: %v", err)
	}
	data := BuyerData{}
	if err := json.Unmarshal([]byte(plain), &data); err != nil {
		t.Fatalf("el contenido del sobre no es un objeto JSON: %v", err)
	}
	return data
}

// requireBDKinds afirma la secuencia exacta de lo que llegó al driver.
func requireBDKinds(t *testing.T, fake *bdFakeDB, want ...string) {
	t.Helper()
	if got := fake.kinds(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("lo que llegó al driver = %v, quería %v", got, want)
	}
}

// TestNewPostgresBuyerData_DoesNotTouchTheDatabase: construir no abre ni consulta nada.
func TestNewPostgresBuyerData_DoesNotTouchTheDatabase(t *testing.T) {
	t.Parallel()
	store, fake := newBDStore(t)
	if store == nil {
		t.Fatalf("NewPostgresBuyerData devolvió nil")
	}
	requireBDKinds(t, fake)
}

// TestPutBuyerField_EmptyKeyIsRejectedBeforeTheDatabase: sin clave, el centinela SIN envolver y
// ni una transacción. Un valor vacío, en cambio, se guarda.
func TestPutBuyerField_EmptyKeyIsRejectedBeforeTheDatabase(t *testing.T) {
	t.Parallel()
	store, fake := newBDStore(t)
	if err := store.PutBuyerField(context.Background(), bdIntake, "", bdRUT); err != ErrBuyerFieldEmpty { //nolint:errorlint // el contrato dice SIN envolver
		t.Fatalf("PutBuyerField sin clave = %v, quería ErrBuyerFieldEmpty tal cual", err)
	}
	requireBDKinds(t, fake)

	if err := store.PutBuyerField(context.Background(), bdIntake, "piso", ""); err != nil {
		t.Fatalf("PutBuyerField con valor vacío: %v", err)
	}
	if got := bdOpenWrite(t, fake.seen()[2]); !reflect.DeepEqual(got, BuyerData{"piso": ""}) {
		t.Errorf("checklist guardado = %+v, quería el campo con valor vacío", got)
	}
}

// TestPutBuyerField_FirstFieldInsertsASealedRow: sin fila, todo en UNA transacción —lectura con
// bloqueo, INSERT, commit— y a la base solo llega el sobre: se cifra ANTES de escribir.
func TestPutBuyerField_FirstFieldInsertsASealedRow(t *testing.T) {
	t.Parallel()
	store, fake := newBDStore(t)

	if err := store.PutBuyerField(context.Background(), bdIntake, "rut", bdRUT); err != nil {
		t.Fatalf("PutBuyerField: %v", err)
	}

	requireBDKinds(t, fake, bdEventBegin, bdEventQuery, bdEventExec, bdEventCommit)
	read, write := fake.seen()[1], fake.seen()[2]
	if read.query != bdSQLSelectForUpdate || !read.inTx || !reflect.DeepEqual(read.args, []driver.Value{bdIntake}) {
		t.Errorf("lectura = %+v, quería el SELECT … FOR UPDATE por la transacción con el intake_id", read)
	}
	if write.query != bdSQLInsert {
		t.Errorf("SQL de la escritura:\n%q\nquería, byte a byte:\n%q", write.query, bdSQLInsert)
	}
	if !write.inTx {
		t.Errorf("la escritura salió FUERA de la transacción que bloqueó la fila")
	}
	if got := bdOpenWrite(t, write); !reflect.DeepEqual(got, BuyerData{"rut": bdRUT}) {
		t.Errorf("checklist guardado = %+v, quería solo el rut", got)
	}
}

// TestPutBuyerField_MergesIntoTheExistingRow: con fila —sellada con una KEK ya RETIRADA— se
// descifra con la KEK de esa fila, se fusiona el campo nuevo sin perder los anteriores, y se
// re-cifra entera con la KEK current por UPDATE. Reescribir una clave corrige esa y solo esa.
func TestPutBuyerField_MergesIntoTheExistingRow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, key, value string
		want             BuyerData
	}{
		{"new field", "direccion", bdAddress, BuyerData{"rut": bdRUT, "piso": "3", "direccion": bdAddress}},
		{"overwritten field", "rut", "otro-valor", BuyerData{"rut": "otro-valor", "piso": "3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			store, fake := newBDStore(t)
			fake.script(bdSealedRow(t, bdKEKOldID, `{"rut":"`+bdRUT+`","piso":"3"}`))

			if err := store.PutBuyerField(context.Background(), bdIntake, c.key, c.value); err != nil {
				t.Fatalf("PutBuyerField: %v", err)
			}

			requireBDKinds(t, fake, bdEventBegin, bdEventQuery, bdEventExec, bdEventCommit)
			write := fake.seen()[2]
			if write.query != bdSQLUpdate {
				t.Errorf("SQL de la escritura:\n%q\nquería, byte a byte:\n%q", write.query, bdSQLUpdate)
			}
			if !write.inTx {
				t.Errorf("la escritura salió FUERA de la transacción que bloqueó la fila")
			}
			if got := bdOpenWrite(t, write); !reflect.DeepEqual(got, c.want) {
				t.Errorf("checklist guardado = %+v, quería %+v", got, c.want)
			}
		})
	}
}

// TestPutBuyerField_ResealsWithAFreshKeyEveryTime: la fila entera se re-cifra en cada campo, con
// llave de fila y nonce nuevos: dos escrituras del mismo dato no producen el mismo sobre.
func TestPutBuyerField_ResealsWithAFreshKeyEveryTime(t *testing.T) {
	t.Parallel()
	store, fake := newBDStore(t)
	for range 2 {
		if err := store.PutBuyerField(context.Background(), bdIntake, "rut", bdRUT); err != nil {
			t.Fatalf("PutBuyerField: %v", err)
		}
	}
	first, second := fake.seen()[2], fake.seen()[6]
	if reflect.DeepEqual(first.args[1], second.args[1]) || reflect.DeepEqual(first.args[2], second.args[2]) {
		t.Errorf("dos escrituras del mismo dato dieron el mismo data_enc o el mismo data_dek")
	}
}

// TestPutBuyerField_Failures: cada fallo con su texto, envolviendo su causa cuando el contrato lo
// dice; después del BEGIN se revierte y NO se escribe; y el valor del campo —el nuevo y los que
// ya estaban— no aparece en ningún error. Un blob ilegible nunca se resuelve sobrescribiendo.
func TestPutBuyerField_Failures(t *testing.T) {
	t.Parallel()
	cause := errors.New("causa-del-driver")
	const jsonErr = "intakes: los datos del comprador de la solicitud " + bdIntake + " no son un objeto JSON"
	cases := []struct {
		name      string
		arrange   func(t *testing.T, fake *bdFakeDB)
		broken    bool // el cifrador no sabe envolver
		wantText  string
		wantCause bool
		wantKinds []string
	}{
		{name: "begin fails", wantCause: true,
			arrange:   func(_ *testing.T, fake *bdFakeDB) { fake.beginErr = cause },
			wantText:  "intakes: abrir transacción de datos del comprador: causa-del-driver",
			wantKinds: nil},
		{name: "read fails", wantCause: true,
			arrange:   func(_ *testing.T, fake *bdFakeDB) { fake.script(bdReply{err: cause}) },
			wantText:  "intakes: leer los datos del comprador de la solicitud " + bdIntake + ": causa-del-driver",
			wantKinds: []string{bdEventBegin, bdEventQuery, bdEventRollback}},
		{name: "row sealed with an unknown KEK",
			arrange: func(t *testing.T, fake *bdFakeDB) {
				row := bdSealedRow(t, bdKEKOldID, `{"rut":"`+bdRUT+`"}`)
				row.rows[0][2] = "kek-que-no-existe"
				fake.script(row)
			},
			wantText:  "intakes: descifrando los datos del comprador de la solicitud " + bdIntake + ": ",
			wantKinds: []string{bdEventBegin, bdEventQuery, bdEventRollback}},
		{name: "tampered blob",
			arrange: func(t *testing.T, fake *bdFakeDB) {
				row := bdSealedRow(t, bdKEKNewID, `{"rut":"`+bdRUT+`"}`)
				row.rows[0][0] = []byte("no-es-un-sobre")
				fake.script(row)
			},
			wantText:  "intakes: descifrando los datos del comprador de la solicitud " + bdIntake + ": ",
			wantKinds: []string{bdEventBegin, bdEventQuery, bdEventRollback}},
		{name: "plaintext is a JSON array",
			arrange:   func(t *testing.T, fake *bdFakeDB) { fake.script(bdSealedRow(t, bdKEKNewID, `["`+bdRUT+`"]`)) },
			wantText:  jsonErr,
			wantKinds: []string{bdEventBegin, bdEventQuery, bdEventRollback}},
		{name: "plaintext is not JSON",
			arrange:   func(t *testing.T, fake *bdFakeDB) { fake.script(bdSealedRow(t, bdKEKNewID, bdRUT)) },
			wantText:  jsonErr,
			wantKinds: []string{bdEventBegin, bdEventQuery, bdEventRollback}},
		// El viejo entraba en pánico aquí, con la transacción abierta: `null` no da error
		// de unmarshal y deja el mapa en nil.
		{name: "plaintext is JSON null",
			arrange:   func(t *testing.T, fake *bdFakeDB) { fake.script(bdSealedRow(t, bdKEKNewID, `null`)) },
			wantText:  jsonErr,
			wantKinds: []string{bdEventBegin, bdEventQuery, bdEventRollback}},
		{name: "encrypt fails", broken: true, wantCause: true,
			arrange:   func(*testing.T, *bdFakeDB) {},
			wantText:  "intakes: cifrando los datos del comprador: WrapDEK de la DEK por-valor: causa-del-driver",
			wantKinds: []string{bdEventBegin, bdEventQuery, bdEventRollback}},
		{name: "write fails", wantCause: true,
			arrange:   func(_ *testing.T, fake *bdFakeDB) { fake.script(bdReply{}, bdReply{err: cause}) },
			wantText:  "intakes: guardando los datos del comprador de la solicitud " + bdIntake + ": causa-del-driver",
			wantKinds: []string{bdEventBegin, bdEventQuery, bdEventExec, bdEventRollback}},
		{name: "commit fails", wantCause: true,
			arrange:   func(_ *testing.T, fake *bdFakeDB) { fake.commitErr = cause },
			wantText:  "intakes: confirmando los datos del comprador: causa-del-driver",
			wantKinds: []string{bdEventBegin, bdEventQuery, bdEventExec}},
		{name: "rollback fails too", wantCause: true,
			arrange: func(_ *testing.T, fake *bdFakeDB) {
				fake.script(bdReply{err: cause})
				fake.rollbackErr = errors.New("conexión rota")
			},
			wantText: "intakes: leer los datos del comprador de la solicitud " + bdIntake + ": causa-del-driver\n" +
				"intakes: rollback de datos del comprador: conexión rota",
			wantKinds: []string{bdEventBegin, bdEventQuery, bdEventRollback}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fake := &bdFakeDB{}
			cipher := newBDCipher(t, bdKEKNewID)
			if c.broken {
				cipher = crypto.NewFieldCipher(bdBrokenKeyProvider{cause: cause})
			}
			store := NewPostgresBuyerData(fake.open(t), cipher)
			c.arrange(t, fake)

			err := store.PutBuyerField(context.Background(), bdIntake, "direccion", bdAddress)

			if err == nil {
				t.Fatalf("PutBuyerField = nil, quería un error")
			}
			// Los dos fallos de descifrado llevan detrás el texto del cifrador: se afirma el prefijo.
			if got := err.Error(); got != c.wantText && (!strings.HasSuffix(c.wantText, ": ") || !strings.HasPrefix(got, c.wantText)) {
				t.Errorf("error = %q, quería %q", got, c.wantText)
			}
			if errors.Is(err, cause) != c.wantCause {
				t.Errorf("errors.Is(err, causa) = %v, quería %v; err = %v", !c.wantCause, c.wantCause, err)
			}
			if errors.Is(err, sql.ErrTxDone) {
				t.Errorf("el error arrastra sql.ErrTxDone: %v", err)
			}
			for _, clear := range []string{bdRUT, bdAddress} {
				if strings.Contains(err.Error(), clear) {
					t.Errorf("FUGA: el error cita el valor %q: %v", clear, err)
				}
			}
			if got := fake.kinds(); strings.Join(got, ",") != strings.Join(c.wantKinds, ",") {
				t.Errorf("lo que llegó al driver = %v, quería %v", got, c.wantKinds)
			}
		})
	}
}

// TestGetBuyerData_ReadsOutsideAnyTransaction: UNA consulta por el pool, sin BEGIN y sin bloqueo;
// se descifra con la KEK de la fila (aquí, una retirada), no con la current.
func TestGetBuyerData_ReadsOutsideAnyTransaction(t *testing.T) {
	t.Parallel()
	store, fake := newBDStore(t)
	fake.script(bdSealedRow(t, bdKEKOldID, `{"rut":"`+bdRUT+`","direccion":"`+bdAddress+`"}`))

	got, found, err := store.GetBuyerData(context.Background(), bdIntake)

	if err != nil || !found {
		t.Fatalf("GetBuyerData = (_, %v, %v), quería found sin error", found, err)
	}
	if want := (BuyerData{"rut": bdRUT, "direccion": bdAddress}); !reflect.DeepEqual(got, want) {
		t.Errorf("checklist = %+v, quería %+v", got, want)
	}
	requireBDKinds(t, fake, bdEventQuery)
	if read := fake.seen()[0]; read.query != bdSQLGet || read.inTx || !reflect.DeepEqual(read.args, []driver.Value{bdIntake}) {
		t.Errorf("lectura = %+v, quería el SELECT sin FOR UPDATE, fuera de transacción, con el intake_id", read)
	}
}

// TestGetBuyerData_NoRowIsAnEmptyChecklist: sin fila no hay error: un checklist vacío y NO nil, y
// found=false. Un sobre cuyo contenido es `{}` SÍ es una fila: vacío y found=true.
func TestGetBuyerData_NoRowIsAnEmptyChecklist(t *testing.T) {
	t.Parallel()
	store, _ := newBDStore(t)
	got, found, err := store.GetBuyerData(context.Background(), bdIntake)
	if err != nil || found || got == nil || len(got) != 0 {
		t.Errorf("sin fila: GetBuyerData = (%#v, %v, %v), quería (vacío no nil, false, nil)", got, found, err)
	}

	for _, plain := range []string{"{}", " { } "} {
		store, fake := newBDStore(t)
		fake.script(bdSealedRow(t, bdKEKNewID, plain))
		got, found, err := store.GetBuyerData(context.Background(), bdIntake)
		if err != nil || !found || got == nil || len(got) != 0 {
			t.Errorf("contenido %s: GetBuyerData = (%#v, %v, %v), quería (vacío no nil, true, nil)", plain, got, found, err)
		}
	}
}

// TestGetBuyerData_Failures: (nil, false, err) con los mismos textos que la escritura, y sin
// citar el dato en claro.
func TestGetBuyerData_Failures(t *testing.T) {
	t.Parallel()
	cause := errors.New("causa-del-driver")
	cases := []struct {
		name      string
		reply     func(t *testing.T) bdReply
		wantText  string
		wantCause bool
	}{
		{name: "read fails", wantCause: true,
			reply:    func(*testing.T) bdReply { return bdReply{err: cause} },
			wantText: "intakes: leer los datos del comprador de la solicitud " + bdIntake + ": causa-del-driver"},
		{name: "row sealed with an unknown KEK",
			reply: func(t *testing.T) bdReply {
				row := bdSealedRow(t, bdKEKOldID, `{"rut":"`+bdRUT+`"}`)
				row.rows[0][2] = "kek-que-no-existe"
				return row
			},
			wantText: "intakes: descifrando los datos del comprador de la solicitud " + bdIntake + ": "},
		{name: "plaintext is not a JSON object",
			reply:    func(t *testing.T) bdReply { return bdSealedRow(t, bdKEKNewID, `"`+bdRUT+`"`) },
			wantText: "intakes: los datos del comprador de la solicitud " + bdIntake + " no son un objeto JSON"},
		{name: "plaintext is JSON null",
			reply:    func(t *testing.T) bdReply { return bdSealedRow(t, bdKEKNewID, `null`) },
			wantText: "intakes: los datos del comprador de la solicitud " + bdIntake + " no son un objeto JSON"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			store, fake := newBDStore(t)
			fake.script(c.reply(t))

			got, found, err := store.GetBuyerData(context.Background(), bdIntake)

			if err == nil || got != nil || found {
				t.Fatalf("GetBuyerData = (%#v, %v, %v), quería (nil, false, error)", got, found, err)
			}
			if text := err.Error(); text != c.wantText && (!strings.HasSuffix(c.wantText, ": ") || !strings.HasPrefix(text, c.wantText)) {
				t.Errorf("error = %q, quería %q", text, c.wantText)
			}
			if errors.Is(err, cause) != c.wantCause {
				t.Errorf("errors.Is(err, causa) = %v, quería %v; err = %v", !c.wantCause, c.wantCause, err)
			}
			if strings.Contains(err.Error(), bdRUT) {
				t.Errorf("FUGA: el error cita el valor en claro: %v", err)
			}
		})
	}
}
