//go:build pendiente

package intake

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// ROJO de (*Postgres).CloseWithSourceText, la quinta operación de la cola (D-F7-9, D-F8-13),
// sobre el driver de mentira de postgres_fakedb_test.go. En el verde pierde la etiqueta.

// wantCloseWithSourceTextSQL es la sentencia, escrita APARTE y byte a byte como las cuatro de
// postgres_test.go. 🔴 Sus tres guardas viven en SQL y este texto es lo único que las custodia sin
// Postgres: el `id` (una fila y solo una, no la tupla), el `status = 'aggregating'` (idempotente)
// y el `updated_at = $2` (solo se cierra lo que se leyó). Su conducta la prueba la suite de la
// cola contra Postgres real, en los procesos de F9.
const wantCloseWithSourceTextSQL = `
UPDATE public.intake_jobs
   SET status             = 'pending',
       source_text_enc    = $3,
       source_text_dek    = $4,
       source_text_kek_id = $5,
       updated_at         = now()
 WHERE id = $1::uuid
   AND status = 'aggregating'
   AND updated_at = $2
`

// incompleteWindowText es el rechazo de la clave a medias o del id vacío.
const incompleteWindowText = "intake: ventana incompleta al cerrar con el literal"

// pgSeen es la ventana viva tal como la dio ListAggregating. Su LastActivity lleva microsegundos
// a propósito: es la precisión de la columna, y la guarda compara por igualdad exacta.
var pgSeen = OpenJob{
	ID:           "22222222-2222-4222-8222-222222222222",
	Key:          pgKey,
	LastActivity: time.Date(2026, 10, 8, 12, 0, 40, 123456000, time.UTC),
	CreatedAt:    time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC),
}

// pgEnvelope es un sobre completo.
var pgEnvelope = SourceText{Enc: []byte("enc-bytes"), DEK: []byte("dek-bytes"), KEKID: "k1"}

// TestPostgres_CloseWithSourceText_NilReceiverOrNilDB_IsANoOp: un *Postgres nil, o uno sin base,
// no tiene dónde escribir: (false, nil), sin panic, como sus hermanas.
func TestPostgres_CloseWithSourceText_NilReceiverOrNilDB_IsANoOp(t *testing.T) {
	for name, p := range map[string]*Postgres{"nil receiver": nil, "nil db": NewPostgres(nil)} {
		if ok, err := p.CloseWithSourceText(context.Background(), pgSeen, pgEnvelope); ok || err != nil {
			t.Errorf("CloseWithSourceText (%s) = (%v, %v), quería (false, nil)", name, ok, err)
		}
	}
}

// TestPostgres_CloseWithSourceText_OneStatement_TrueOnlyIfItTouchedARow: UNA sentencia, la
// literal, sin lectura previa; con el id, la marca que se leyó TAL CUAL (sin truncar) y las tres
// piezas del sobre como llegan. true si afectó una fila y (false, nil) si ninguna: la ventana
// cambió, ya estaba cerrada o no existe.
func TestPostgres_CloseWithSourceText_OneStatement_TrueOnlyIfItTouchedARow(t *testing.T) {
	for affected, want := range map[int64]bool{0: false, 1: true} {
		store, fake := newFakePostgres(t)
		fake.script(fakeReply{affected: affected})
		ok, err := store.CloseWithSourceText(context.Background(), pgSeen, pgEnvelope)
		if err != nil || ok != want {
			t.Errorf("CloseWithSourceText con %d filas afectadas = (%v, %v), quería (%v, nil)", affected, ok, err, want)
		}
		stmt := fake.requireOnly(t, fakeExec)
		requireSQL(t, stmt, wantCloseWithSourceTextSQL)
		if len(stmt.args) != 5 {
			t.Fatalf("argumentos = %#v, quería 5 (id, marca leída y las tres piezas del sobre)", stmt.args)
		}
		if stmt.args[0] != pgSeen.ID {
			t.Errorf("$1 = %#v, quería el id %q", stmt.args[0], pgSeen.ID)
		}
		if sent, isTime := stmt.args[1].(time.Time); !isTime || !sent.Equal(pgSeen.LastActivity) {
			t.Errorf("$2 = %#v, quería la marca leída %v, sin truncar", stmt.args[1], pgSeen.LastActivity)
		}
		enc, encOK := stmt.args[2].([]byte)
		dek, dekOK := stmt.args[3].([]byte)
		if !encOK || !dekOK || !bytes.Equal(enc, pgEnvelope.Enc) || !bytes.Equal(dek, pgEnvelope.DEK) || stmt.args[4] != pgEnvelope.KEKID {
			t.Errorf("sobre enviado = (%#v, %#v, %#v), quería (%q, %q, %q)",
				stmt.args[2], stmt.args[3], stmt.args[4], pgEnvelope.Enc, pgEnvelope.DEK, pgEnvelope.KEKID)
		}
	}
}

// TestPostgres_CloseWithSourceText_EmptyEnvelope_SendsThreeNulls: el sobre vacío entero —el hilo
// sin mensajes— cierra igual, y sus tres argumentos viajan como NULL: ni un bytea de longitud
// cero ni un kek_id "", que dejarían una fila que parece tener sobre.
func TestPostgres_CloseWithSourceText_EmptyEnvelope_SendsThreeNulls(t *testing.T) {
	cases := map[string]SourceText{
		"zero value":           {},
		"empty non-nil slices": {Enc: []byte{}, DEK: []byte{}},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(fakeReply{affected: 1})
			ok, err := store.CloseWithSourceText(context.Background(), pgSeen, env)
			if err != nil || !ok {
				t.Fatalf("CloseWithSourceText con el sobre vacío = (%v, %v), quería (true, nil)", ok, err)
			}
			stmt := fake.requireOnly(t, fakeExec)
			requireSQL(t, stmt, wantCloseWithSourceTextSQL)
			if len(stmt.args) != 5 {
				t.Fatalf("argumentos = %#v, quería 5", stmt.args)
			}
			for i, column := range []string{"source_text_enc", "source_text_dek", "source_text_kek_id"} {
				if stmt.args[2+i] != nil {
					t.Errorf("%s viaja como %#v, quería NULL", column, stmt.args[2+i])
				}
			}
		})
	}
}

// TestPostgres_CloseWithSourceText_HalfEnvelope_SaysWhatIsMissing: el sobre a medias se rechaza
// antes de tocar la base, con el mismo texto que PutSourceText: dice QUÉ falta sin citar el
// contenido.
func TestPostgres_CloseWithSourceText_HalfEnvelope_SaysWhatIsMissing(t *testing.T) {
	cases := map[string]SourceText{
		"intake: sobre del literal incompleto (enc=0 dek=3 kek_id=true): son las tres o ninguna":  {DEK: []byte("dek"), KEKID: "k1"},
		"intake: sobre del literal incompleto (enc=7 dek=0 kek_id=true): son las tres o ninguna":  {Enc: []byte("secreto"), KEKID: "k1"},
		"intake: sobre del literal incompleto (enc=7 dek=3 kek_id=false): son las tres o ninguna": {Enc: []byte("secreto"), DEK: []byte("dek")},
		"intake: sobre del literal incompleto (enc=0 dek=0 kek_id=true): son las tres o ninguna":  {KEKID: "k1"},
	}
	// Un solo store para los cuatro: ninguno puede haber mandado nada a la base.
	store, fake := newFakePostgres(t)
	for want, env := range cases {
		ok, err := store.CloseWithSourceText(context.Background(), pgSeen, env)
		if ok || err == nil || err.Error() != want {
			t.Errorf("CloseWithSourceText = (%v, %v), quería (false, %q)", ok, err, want)
		}
	}
	fake.requireUntouched(t)
}

// TestPostgres_CloseWithSourceText_IncompleteWindow_RejectedBeforeTheEnvelope: con la clave a
// medias o sin id se rechaza en Go, con su texto y sin mandar nada a la base. La ventana se mira
// ANTES que el sobre: con las dos cosas mal, el error es el de la ventana.
func TestPostgres_CloseWithSourceText_IncompleteWindow_RejectedBeforeTheEnvelope(t *testing.T) {
	broken := map[string]OpenJob{}
	for name, k := range incompleteKeys() {
		job := pgSeen
		job.Key = k
		broken[name] = job
	}
	noID := pgSeen
	noID.ID = ""
	broken["no id"] = noID

	envelopes := map[string]SourceText{"complete": pgEnvelope, "empty": {}, "half": {Enc: []byte("secreto")}}
	for name, job := range broken {
		t.Run(name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			for envName, env := range envelopes {
				ok, err := store.CloseWithSourceText(context.Background(), job, env)
				if ok || err == nil || err.Error() != incompleteWindowText {
					t.Errorf("CloseWithSourceText (sobre %s) = (%v, %v), quería (false, %q)", envName, ok, err, incompleteWindowText)
				}
			}
			fake.requireUntouched(t)
		})
	}
}

// TestPostgres_CloseWithSourceText_Failures_AreWrapped: el fallo de la sentencia y el de contar
// las filas salen envueltos, cada uno con su prefijo, y nunca como un cierre hecho.
func TestPostgres_CloseWithSourceText_Failures_AreWrapped(t *testing.T) {
	cases := map[string]fakeReply{
		"intake: cerrar la ventana con su literal: ":     {err: errFakeBoom},
		"intake: contar filas cerradas con su literal: ": {affected: 1, affectedErr: errFakeBoom},
	}
	for prefix, reply := range cases {
		store, fake := newFakePostgres(t)
		fake.script(reply)
		ok, err := store.CloseWithSourceText(context.Background(), pgSeen, pgEnvelope)
		requireWrapped(t, err, errFakeBoom, prefix)
		if ok {
			t.Errorf("CloseWithSourceText con error devolvió true (%q)", prefix)
		}
	}
}
