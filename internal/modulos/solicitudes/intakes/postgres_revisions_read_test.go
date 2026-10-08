package intakes

import (
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Este fichero prueba la LECTURA de revisiones del adaptador —no es un método exportado: se
// observa por Get— y la retención del literal de nivel 2: descifrado, poda perezosa y sello.

// Edad y TTL de ejemplo, en segundos: una revisión joven y una que ya venció el TTL de plataforma.
const (
	pgYoungAge = int64(60)
	pgOldAge   = int64(400 * 24 * 3600)
)

// pgPlatformTTL es el TTL de plataforma en segundos, que la lectura manda como segundo argumento.
var pgPlatformTTL = int64(DefaultLiteralTTL.Seconds())

// pgRevisionRow es una fila de revisión con sus doce columnas: las seis de la revisión, el sobre
// (vacío), el sello de poda (NULL), la edad y el TTL.
func pgRevisionRow(no int64, payload string, age int64) []driver.Value {
	return []driver.Value{no, RevisionKindInterpreted, []byte(payload), nil, nil, pgCreated,
		nil, nil, nil, nil, age, pgPlatformTTL}
}

// pgSealedRow es pgRevisionRow con el literal `plain` sellado en su sobre por cipher.
func pgSealedRow(t *testing.T, no int64, payload, plain string, age int64) []driver.Value {
	t.Helper()
	enc, dek, kekID, err := newPgCipher(t).Encrypt(plain)
	if err != nil {
		t.Fatalf("sellar el literal de prueba: %v", err)
	}
	row := pgRevisionRow(no, payload, age)
	row[6], row[7], row[8] = enc, dek, kekID
	return row
}

// scriptGetWithRevisions guioniza un Get hasta sus revisiones: cabecera, líneas (ninguna) y las
// filas de revisión dadas. Lo que venga después (podas y la consulta de datos del comprador) lo
// encola cada test.
func scriptGetWithRevisions(fake *pgFake, revisions ...[]driver.Value) {
	fake.script(pgOne(pgIntakeRow(StatusOpen, 10)...), pgReply{}, pgReply{rows: revisions})
}

// scriptExpiredRevisionRead guioniza un Get entero cuya única revisión trae un literal vencido que
// la poda sella en sealedAt. El sobre va con bytes cualesquiera: lo vencido no se abre.
func scriptExpiredRevisionRead(fake *pgFake, sealedAt time.Time) {
	row := pgRevisionRow(1, `{"v":1}`, pgOldAge)
	row[6], row[7], row[8] = []byte("enc"), []byte("dek"), pgKEKID
	scriptGetWithRevisions(fake, row)
	fake.script(pgOne(sealedAt), pgOne(false))
}

// TestPostgres_Revisions_PlainRowsComeOutAsStored: sin sobre, el payload sale tal cual, los textos
// NULL como cadena vacía, y la consulta lleva la solicitud y el TTL de plataforma en segundos.
func TestPostgres_Revisions_PlainRowsComeOutAsStored(t *testing.T) {
	store, fake := newFakePostgres(t)
	second := pgRevisionRow(2, `{"v":1,"total":20}`, pgYoungAge)
	second[3], second[4] = "Tu pedido", RevisionByOwner
	scriptGetWithRevisions(fake, pgRevisionRow(1, `{"v":1,"total":10}`, pgOldAge), second)
	fake.script(pgOne(false))
	d, err := store.Get(t.Context(), pgTenant, pgIntakeID)
	if err != nil {
		t.Fatalf("Get: error inesperado %v", err)
	}
	want := []Revision{
		{IntakeID: pgIntakeID, RevisionNo: 1, Kind: RevisionKindInterpreted, Payload: []byte(`{"v":1,"total":10}`), CreatedAt: pgCreated},
		{IntakeID: pgIntakeID, RevisionNo: 2, Kind: RevisionKindInterpreted, Payload: []byte(`{"v":1,"total":20}`), RenderedText: "Tu pedido", CreatedBy: RevisionByOwner, CreatedAt: pgCreated},
	}
	if !reflect.DeepEqual(d.Revisions, want) {
		t.Errorf("Revisions = %+v, quería %+v", d.Revisions, want)
	}
	if got, want := fake.statements()[2].args, []driver.Value{pgIntakeID, pgPlatformTTL}; !reflect.DeepEqual(got, want) {
		t.Errorf("argumentos de la lectura de revisiones = %v, quería %v", got, want)
	}
	requirePgKinds(t, fake, pgQuery, pgQuery, pgQuery, pgQuery) // una revisión vieja SIN sobre no se poda
}

// TestPostgres_Revisions_LiveLiteralIsDecryptedAndMerged: un sobre vigente se abre y el literal
// vuelve fundido en el payload; no hay poda ni evento.
func TestPostgres_Revisions_LiveLiteralIsDecryptedAndMerged(t *testing.T) {
	sink := &pgLogSink{}
	store, fake := newFakePostgres(t, WithLiteralCipher(newPgCipher(t)), WithRetentionLog(sink))
	scriptGetWithRevisions(fake, pgSealedRow(t, 1, `{"v":1}`, `{"source_text":"dos empanadas"}`, pgYoungAge))
	fake.script(pgOne(false))
	d, err := store.Get(t.Context(), pgTenant, pgIntakeID)
	if err != nil {
		t.Fatalf("Get: error inesperado %v", err)
	}
	if got := string(d.Revisions[0].Payload); !strings.Contains(got, `"source_text":"dos empanadas"`) {
		t.Errorf("payload = %s, quería el literal fundido", got)
	}
	if !d.Revisions[0].LiteralPrunedAt.IsZero() || len(sink.logged()) != 0 {
		t.Errorf("un literal vigente salió podado (%v) o anunciado (%+v)", d.Revisions[0].LiteralPrunedAt, sink.logged())
	}
	requirePgKinds(t, fake, pgQuery, pgQuery, pgQuery, pgQuery)
}

// TestPostgres_Revisions_ExpiredLiteralIsPrunedSealedAndLogged: un sobre vencido se poda DESPUÉS de
// leer (una sentencia más, con la solicitud y el número), la revisión sale sin literal y con el
// sello que devolvió la base en la MISMA lectura, y la poda queda registrada. No hace falta
// cifrador: lo vencido no se abre.
func TestPostgres_Revisions_ExpiredLiteralIsPrunedSealedAndLogged(t *testing.T) {
	sink := &pgLogSink{}
	store, fake := newFakePostgres(t, WithRetentionLog(sink))
	scriptExpiredRevisionRead(fake, pgAt)
	d, err := store.Get(t.Context(), pgTenant, pgIntakeID)
	if err != nil {
		t.Fatalf("Get: error inesperado %v", err)
	}
	rev := d.Revisions[0]
	if !rev.LiteralPrunedAt.Equal(pgAt) || string(rev.Payload) != `{"v":1}` {
		t.Errorf("revisión podada = %+v, quería el payload intacto y LiteralPrunedAt = %v", rev, pgAt)
	}
	requirePgKinds(t, fake, pgQuery, pgQuery, pgQuery, pgQuery, pgQuery)
	if got, want := fake.statements()[3].args, []driver.Value{pgIntakeID, 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("argumentos de la poda = %v, quería %v", got, want)
	}
	logged := sink.logged()
	if len(logged) != 1 || logged[0].level != "info" || logged[0].msg != "retención: literal de la revisión podado por TTL vencido" {
		t.Fatalf("evento de poda = %+v", logged)
	}
	wantArgs := []any{"intake_id", pgIntakeID, "revision_no", 1, "edad_segundos", pgOldAge, "ttl_segundos", pgPlatformTTL}
	if !reflect.DeepEqual(logged[0].args, wantArgs) {
		t.Errorf("campos del evento de poda = %v, quería %v", logged[0].args, wantArgs)
	}
}

// TestPostgres_Revisions_TenantTTLZeroNeverPrunes: un TTL 0 en la fila es «no podar», por vieja
// que sea la revisión: se abre como vigente.
func TestPostgres_Revisions_TenantTTLZeroNeverPrunes(t *testing.T) {
	store, fake := newFakePostgres(t, WithLiteralCipher(newPgCipher(t)))
	row := pgSealedRow(t, 1, `{"v":1}`, `{"source_text":"dos empanadas"}`, pgOldAge)
	row[11] = int64(0)
	scriptGetWithRevisions(fake, row)
	fake.script(pgOne(false))
	d, err := store.Get(t.Context(), pgTenant, pgIntakeID)
	if err != nil {
		t.Fatalf("Get: error inesperado %v", err)
	}
	if !strings.Contains(string(d.Revisions[0].Payload), "dos empanadas") {
		t.Errorf("payload = %s, quería el literal fundido", d.Revisions[0].Payload)
	}
	requirePgKinds(t, fake, pgQuery, pgQuery, pgQuery, pgQuery)
}

// TestPostgres_Revisions_PruneThatSealsNothing: si otra lectura se adelantó (la poda no devuelve
// fila) no se anuncia nada; si la poda falla, se registra como error. En los dos casos la lectura
// NO falla y la revisión sale sin fecha inventada.
func TestPostgres_Revisions_PruneThatSealsNothing(t *testing.T) {
	cases := []struct {
		name      string
		prune     pgReply
		wantLevel string
		wantMsg   string
	}{
		{"another reader pruned first", pgReply{}, "", ""},
		{"prune fails", pgReply{err: errPgBoom}, "error", "retención: no se pudo podar el literal de una revisión vencida"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &pgLogSink{}
			store, fake := newFakePostgres(t, WithRetentionLog(sink))
			row := pgRevisionRow(1, `{"v":1}`, pgOldAge)
			row[6], row[7], row[8] = []byte("enc"), []byte("dek"), pgKEKID
			scriptGetWithRevisions(fake, row)
			fake.script(tc.prune, pgOne(false))
			d, err := store.Get(t.Context(), pgTenant, pgIntakeID)
			if err != nil {
				t.Fatalf("Get: la poda sin sello hizo fallar la lectura: %v", err)
			}
			if !d.Revisions[0].LiteralPrunedAt.IsZero() {
				t.Errorf("LiteralPrunedAt = %v, quería el cero", d.Revisions[0].LiteralPrunedAt)
			}
			logged := sink.logged()
			if tc.wantLevel == "" {
				if len(logged) != 0 {
					t.Errorf("se anunció una poda que no ocurrió: %+v", logged)
				}
				return
			}
			if len(logged) != 1 || logged[0].level != tc.wantLevel || logged[0].msg != tc.wantMsg {
				t.Fatalf("registro = %+v, quería un %s %q", logged, tc.wantLevel, tc.wantMsg)
			}
			wantArgs := []any{"intake_id", pgIntakeID, "revision_no", 1, "error", errPgBoom}
			if !reflect.DeepEqual(logged[0].args, wantArgs) {
				t.Errorf("campos del registro = %v, quería %v", logged[0].args, wantArgs)
			}
		})
	}
}

// TestPostgres_Revisions_ReadErrors: los fallos de la consulta, con su prefijo byte a byte.
func TestPostgres_Revisions_ReadErrors(t *testing.T) {
	closeBoom := errors.New("cierre roto")
	cases := []struct {
		name   string
		reply  pgReply
		prefix string
		cause  error
	}{
		{"query fails", pgReply{err: errPgBoom}, "intakes: listar revisiones: ", errPgBoom},
		{"iteration fails", pgReply{endErr: errPgBoom}, "intakes: recorrer revisiones: ", errPgBoom},
		{"close fails", pgReply{closeErr: closeBoom}, "intakes: cerrar filas de revisiones: ", closeBoom},
		{"close does not mask iteration", pgReply{endErr: errPgBoom, closeErr: closeBoom}, "intakes: recorrer revisiones: ", errPgBoom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(pgOne(pgIntakeRow(StatusOpen, 10)...), pgReply{}, tc.reply)
			_, err := store.Get(t.Context(), pgTenant, pgIntakeID)
			requirePgWrapped(t, err, tc.prefix, tc.cause)
		})
	}
	t.Run("row cannot be scanned", func(t *testing.T) {
		store, fake := newFakePostgres(t)
		bad := pgRevisionRow(1, `{"v":1}`, pgYoungAge)
		bad[5] = "no es una fecha"
		scriptGetWithRevisions(fake, bad)
		_, err := store.Get(t.Context(), pgTenant, pgIntakeID)
		if err == nil || !strings.HasPrefix(err.Error(), "intakes: leer revisión: ") {
			t.Errorf("error = %v, quería el prefijo %q", err, "intakes: leer revisión: ")
		}
	})
}

// TestPostgres_Revisions_EnvelopeErrors: un sobre vigente que no se puede abrir hace fallar la
// lectura nombrando la revisión y la solicitud, con el texto exacto de cada causa.
func TestPostgres_Revisions_EnvelopeErrors(t *testing.T) {
	const prefix = "intakes: revisión 2 de la solicitud " + pgIntakeID + ": "
	incomplete := pgRevisionRow(2, `{"v":1}`, pgYoungAge)
	incomplete[6] = []byte("enc")
	garbage := pgRevisionRow(2, `{"v":1}`, pgYoungAge)
	garbage[6], garbage[7], garbage[8] = []byte("enc"), []byte("dek"), pgKEKID
	cases := []struct {
		name       string
		withCipher bool
		row        []driver.Value
		want       string
		exact      bool
	}{
		{"incomplete envelope", true, incomplete,
			prefix + "sobre del literal incompleto en BD (enc=3 dek=0 kek_id=false): son las tres o ninguna", true},
		{"store without cipher", false, pgSealedRow(t, 2, `{"v":1}`, `{"source_text":"x"}`, pgYoungAge),
			prefix + "la revisión trae literal cifrado y el store no tiene FieldCipher (fallo de cableado, no de dato)", true},
		{"envelope does not decrypt", true, garbage, prefix + "descifrar el literal: ", false},
		{"decrypted text is not the literal", true, pgSealedRow(t, 2, `{"v":1}`, `esto no es JSON`, pgYoungAge),
			prefix + "interpretar el literal descifrado: ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var opts []PostgresOption
			if tc.withCipher {
				opts = append(opts, WithLiteralCipher(newPgCipher(t)))
			}
			store, fake := newFakePostgres(t, opts...)
			scriptGetWithRevisions(fake, tc.row)
			d, err := store.Get(t.Context(), pgTenant, pgIntakeID)
			if err == nil || !reflect.DeepEqual(d, Detail{}) {
				t.Fatalf("Get = (%+v, %v), quería (Detail{}, error)", d, err)
			}
			if tc.exact && err.Error() != tc.want {
				t.Errorf("error = %q, quería %q", err.Error(), tc.want)
			}
			if !tc.exact && !strings.HasPrefix(err.Error(), tc.want) {
				t.Errorf("error = %q, quería el prefijo %q", err.Error(), tc.want)
			}
		})
	}
}

// TestPostgres_Revisions_StoredSealWinsOverTheNewOne: LA COLUMNA MANDA. Si la fila ya traía sello
// y aun así conserva un sobre vencido, la poda lo destruye pero el sello que sale es el que había:
// el instante en que el texto se destruyó de verdad no se mueve con una lectura posterior.
func TestPostgres_Revisions_StoredSealWinsOverTheNewOne(t *testing.T) {
	store, fake := newFakePostgres(t, WithRetentionLog(&pgLogSink{}))
	row := pgRevisionRow(1, `{"v":1}`, pgOldAge)
	row[6], row[7], row[8], row[9] = []byte("enc"), []byte("dek"), pgKEKID, pgUpdated
	scriptGetWithRevisions(fake, row)
	fake.script(pgOne(pgAt), pgOne(false))
	d, err := store.Get(t.Context(), pgTenant, pgIntakeID)
	if err != nil {
		t.Fatalf("Get: error inesperado %v", err)
	}
	if got := d.Revisions[0].LiteralPrunedAt; !got.Equal(pgUpdated) {
		t.Errorf("LiteralPrunedAt = %v, quería el sello que ya tenía la fila (%v)", got, pgUpdated)
	}
}
