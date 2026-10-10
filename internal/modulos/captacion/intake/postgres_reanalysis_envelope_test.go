//go:build pendiente

package intake

import (
	"context"
	"database/sql/driver"
	"reflect"
	"testing"
)

// ROJO de T8.40 (D-F7-9, D-F8-13) en el adaptador: (*Postgres).OpenReanalysis inserta el job YA
// con su sobre, sobre el driver de mentira de postgres_fakedb_test.go.
//
// 🔧 Este fichero es PROVISIONAL. En el verde sus tests pasan a postgres_reanalysis_test.go, que
// es el test de postgres_reanalysis.go, y wantOpenReanalysisEnvelopeSQL sustituye allí a
// wantOpenReanalysisSQL.

// wantOpenReanalysisEnvelopeSQL es la sentencia nueva, escrita APARTE y byte a byte. Es el MISMO
// INSERT de antes con las tres columnas del sobre al final (`$10, $11, $12`). 🔴 Sin Postgres,
// este texto es lo único que custodia que el sobre viaja en la sentencia que crea la fila —y no
// en una segunda—: una sola sentencia es lo que hace que no exista un job `pending` sin literal.
const wantOpenReanalysisEnvelopeSQL = `
INSERT INTO public.intake_jobs
       (tenant_id, session_id, contact_id, event_id, status, message_ts, source_refs,
        intake_id, requested_by, reanalysis_via, reanalysis_source, reanalyzed_from,
        source_text_enc, source_text_dek, source_text_kek_id)
VALUES ($1, $2, $3, $4::uuid, 'pending',
        COALESCE((SELECT j0.message_ts
                    FROM public.intake_jobs j0
                   WHERE j0.tenant_id = $1 AND j0.event_id = $4::uuid
                     AND j0.message_ts IS NOT NULL
                   ORDER BY j0.created_at
                   LIMIT 1), now()),
        '[]'::jsonb,
        $5::uuid, $6, $7, $8, $9,
        $10, $11, $12)
RETURNING id::text
`

// pgBornEnvelope es el sobre completo con el que nace el job.
var pgBornEnvelope = SourceText{Enc: []byte("enc-bytes"), DEK: []byte("dek-bytes"), KEKID: "k1"}

// TestPostgres_OpenReanalysis_FullEnvelope_TravelsInTheSameInsert: UNA sentencia, la literal, sin
// ninguna otra antes ni después, con los DOCE argumentos: los nueve de siempre y, al final, las
// tres piezas del sobre tal como llegan. Devuelve el id que da la base.
func TestPostgres_OpenReanalysis_FullEnvelope_TravelsInTheSameInsert(t *testing.T) {
	for from, wantFrom := range map[int]driver.Value{3: 3, 0: nil} {
		store, fake := newFakePostgres(t)
		fake.script(fakeReply{rows: [][]driver.Value{{pgJobID}}})
		req := pgRequest()
		req.Context.From = from
		req.SourceText = pgBornEnvelope
		id, err := store.OpenReanalysis(context.Background(), req)
		if id != pgJobID || err != nil {
			t.Errorf("OpenReanalysis = (%q, %v), quería (%q, nil)", id, err, pgJobID)
		}
		stmt := fake.requireOnly(t, fakeQuery)
		requireSQL(t, stmt, wantOpenReanalysisEnvelopeSQL)
		want := []driver.Value{
			pgKey.TenantID, pgKey.SessionID, pgKey.ContactID, pgKey.EventID,
			pgIntakeID, "owner", "api", "both", wantFrom,
			[]byte("enc-bytes"), []byte("dek-bytes"), "k1",
		}
		if !reflect.DeepEqual(stmt.args, want) {
			t.Errorf("argumentos con From=%d = %#v, quería los doce %#v", from, stmt.args, want)
		}
	}
}

// TestPostgres_OpenReanalysis_EmptyEnvelope_SendsThreeNulls: el sobre vacío entero —el hilo sin
// mensajes— abre el job igual, con la MISMA sentencia, y sus tres argumentos viajan como NULL: ni
// un bytea de longitud cero ni un kek_id "", que dejarían una fila que parece tener sobre.
func TestPostgres_OpenReanalysis_EmptyEnvelope_SendsThreeNulls(t *testing.T) {
	cases := map[string]SourceText{
		"zero value":           {},
		"empty non-nil slices": {Enc: []byte{}, DEK: []byte{}},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(fakeReply{rows: [][]driver.Value{{pgJobID}}})
			req := pgRequest()
			req.SourceText = env
			id, err := store.OpenReanalysis(context.Background(), req)
			if id != pgJobID || err != nil {
				t.Fatalf("OpenReanalysis con el sobre vacío = (%q, %v), quería (%q, nil)", id, err, pgJobID)
			}
			stmt := fake.requireOnly(t, fakeQuery)
			requireSQL(t, stmt, wantOpenReanalysisEnvelopeSQL)
			if len(stmt.args) != 12 {
				t.Fatalf("argumentos = %#v, quería 12 (los nueve de la petición y las tres piezas del sobre)", stmt.args)
			}
			for i, column := range []string{"source_text_enc", "source_text_dek", "source_text_kek_id"} {
				if stmt.args[9+i] != nil {
					t.Errorf("%s viaja como %#v, quería NULL", column, stmt.args[9+i])
				}
			}
		})
	}
}

// TestPostgres_OpenReanalysis_HalfEnvelope_SaysWhatIsMissing: el sobre a medias se rechaza antes
// de tocar la base, con el mismo texto que PutSourceText y CloseWithSourceText: dice QUÉ falta sin
// citar el contenido. No se abre ningún job.
func TestPostgres_OpenReanalysis_HalfEnvelope_SaysWhatIsMissing(t *testing.T) {
	cases := map[string]SourceText{
		"intake: sobre del literal incompleto (enc=0 dek=3 kek_id=true): son las tres o ninguna":  {DEK: []byte("dek"), KEKID: "k1"},
		"intake: sobre del literal incompleto (enc=7 dek=0 kek_id=true): son las tres o ninguna":  {Enc: []byte("secreto"), KEKID: "k1"},
		"intake: sobre del literal incompleto (enc=7 dek=3 kek_id=false): son las tres o ninguna": {Enc: []byte("secreto"), DEK: []byte("dek")},
		"intake: sobre del literal incompleto (enc=0 dek=0 kek_id=true): son las tres o ninguna":  {KEKID: "k1"},
	}
	// Un solo store para los cuatro: ninguno puede haber mandado nada a la base.
	store, fake := newFakePostgres(t)
	for want, env := range cases {
		req := pgRequest()
		req.SourceText = env
		id, err := store.OpenReanalysis(context.Background(), req)
		if id != "" || err == nil || err.Error() != want {
			t.Errorf("OpenReanalysis = (%q, %v), quería (\"\", %q)", id, err, want)
		}
	}
	fake.requireUntouched(t)
}
