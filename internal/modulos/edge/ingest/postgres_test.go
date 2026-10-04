package ingest

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Las dos sentencias del adaptador, byte a byte (con su sangría): son las del fichero viejo.
const (
	insertSQL = `
		INSERT INTO public.ingest_dedupe (session_id, wa_message_id)
		VALUES ($1, $2)
		ON CONFLICT (session_id, wa_message_id) DO NOTHING
	`
	sweepSQL = `
		DELETE FROM public.ingest_dedupe
		WHERE ctid IN (
			SELECT ctid FROM public.ingest_dedupe
			WHERE first_seen_at < $1
			LIMIT $2
		)
	`
)

// Los valores por defecto que promete el contrato de PostgresDeduper.
const (
	wantDefaultRetention  = 7 * 24 * time.Hour
	wantDefaultSweepEvery = 512
	wantDefaultSweepBatch = 1000
)

// newDeduper monta el adaptador sobre el driver de mentira.
func newDeduper(t *testing.T, opts ...Option) (*PostgresDeduper, *fakeDB) {
	t.Helper()
	fake := newFakeDB()
	return NewPostgresDeduper(fake.open(t), opts...), fake
}

// seeNew pregunta por n claves NUEVAS de la sesión (wamid.<prefix>-1 … -n) y exige (false, nil).
func seeNew(t *testing.T, d *PostgresDeduper, prefix string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("wamid.%s-%d", prefix, i)
		seen, err := d.Seen(context.Background(), "session-1", id)
		if err != nil || seen {
			t.Fatalf("Seen(%q) = (%v, %v), quería (false, nil): la clave es nueva", id, seen, err)
		}
	}
}

// newKeysUntilSweep pregunta por claves nuevas hasta que llega la primera poda, y devuelve
// cuántas hicieron falta y la poda. Se rinde a las 2000.
func newKeysUntilSweep(t *testing.T, d *PostgresDeduper, fake *fakeDB) (int, statement) {
	t.Helper()
	for n := 1; n <= 2000; n++ {
		seeNew(t, d, fmt.Sprintf("until-%d", n), 1)
		if sweeps := fake.sweeps(); len(sweeps) > 0 {
			return n, sweeps[0]
		}
	}
	t.Fatal("2000 claves nuevas y ninguna poda")
	return 0, statement{}
}

// requireCutoff afirma que el corte de la poda es (ahora − retention) con «ahora» entre before y
// after —los dos instantes que el test tomó alrededor de la llamada—, y que viaja en UTC.
func requireCutoff(t *testing.T, sweep statement, retention time.Duration, before, after time.Time) {
	t.Helper()
	if len(sweep.args) != 2 {
		t.Fatalf("la poda mandó %d argumentos, quería 2: %v", len(sweep.args), sweep.args)
	}
	cutoff, ok := sweep.args[0].(time.Time)
	if !ok {
		t.Fatalf("argumento $1 de la poda = %v (%T), quería un time.Time", sweep.args[0], sweep.args[0])
	}
	if cutoff.Before(before.Add(-retention)) || cutoff.After(after.Add(-retention)) {
		t.Errorf("corte de la poda = %v, quería ahora − %v (entre %v y %v)",
			cutoff, retention, before.Add(-retention), after.Add(-retention))
	}
	if cutoff.Location() != time.UTC {
		t.Errorf("el corte de la poda viaja en %v, quería UTC", cutoff.Location())
	}
}

// requireBatch afirma el lote (LIMIT) de la poda.
func requireBatch(t *testing.T, sweep statement, want int64) {
	t.Helper()
	if len(sweep.args) != 2 {
		t.Fatalf("la poda mandó %d argumentos, quería 2: %v", len(sweep.args), sweep.args)
	}
	if sweep.args[1] != driver.Value(want) {
		t.Errorf("argumento $2 de la poda = %v (%T), quería %d", sweep.args[1], sweep.args[1], want)
	}
}

// TestNewPostgresDeduper_DoesNotQuery: construir el deduper no toca la base.
func TestNewPostgresDeduper_DoesNotQuery(t *testing.T) {
	d, fake := newDeduper(t)
	if d == nil {
		t.Fatal("NewPostgresDeduper devolvió nil")
	}
	if seen := fake.seen(); len(seen) != 0 {
		t.Errorf("construir el deduper emitió %d sentencias, quería 0", len(seen))
	}
}

// TestPostgresDeduper_Seen_FirstSightingInserts: una clave nueva es UN INSERT, el literal, con la
// sesión y el mensaje, y devuelve false.
func TestPostgresDeduper_Seen_FirstSightingInserts(t *testing.T) {
	d, fake := newDeduper(t)
	seen, err := d.Seen(context.Background(), "session-1", "wamid.A")
	if err != nil || seen {
		t.Fatalf("Seen de una clave nueva = (%v, %v), quería (false, nil)", seen, err)
	}
	got := fake.seen()
	if len(got) != 1 {
		t.Fatalf("llegaron %d sentencias al driver, quería 1 (un solo write en el camino caliente): %v", len(got), fake.kinds())
	}
	if got[0].query != insertSQL {
		t.Errorf("SQL emitido:\n%q\nquería:\n%q", got[0].query, insertSQL)
	}
	if !slices.Equal(got[0].args, []driver.Value{"session-1", "wamid.A"}) {
		t.Errorf("argumentos = %v, quería [session-1 wamid.A]", got[0].args)
	}
}

// TestPostgresDeduper_Seen_ZeroRowsAffectedIsDuplicate: si el INSERT no toca ninguna fila, la
// clave ya estaba: true.
func TestPostgresDeduper_Seen_ZeroRowsAffectedIsDuplicate(t *testing.T) {
	d, fake := newDeduper(t)
	seeNew(t, d, "dup", 1)
	for range 2 {
		seen, err := d.Seen(context.Background(), "session-1", "wamid.dup-1")
		if err != nil || !seen {
			t.Fatalf("Seen de una clave repetida = (%v, %v), quería (true, nil)", seen, err)
		}
	}
	// La clave son los dos: el mismo mensaje en otra sesión es nuevo.
	seen, err := d.Seen(context.Background(), "session-2", "wamid.dup-1")
	if err != nil || seen {
		t.Errorf("Seen del mismo mensaje en otra sesión = (%v, %v), quería (false, nil)", seen, err)
	}
	if kinds := fake.kinds(); !slices.Equal(kinds, []string{kindInsert, kindInsert, kindInsert, kindInsert}) {
		t.Errorf("sentencias = %v, quería cuatro INSERT y nada más", kinds)
	}
}

// TestPostgresDeduper_Seen_InsertErrorIsFalseAndWrapped: el fallo del INSERT sale envuelto y con
// false (el consumidor es fail-open).
func TestPostgresDeduper_Seen_InsertErrorIsFalseAndWrapped(t *testing.T) {
	d, fake := newDeduper(t)
	boom := errors.New("base caída")
	fake.set(func(f *fakeDB) { f.insertErr = boom })
	seen, err := d.Seen(context.Background(), "session-1", "wamid.A")
	requireFalseAndWrapped(t, seen, err, boom, "ingest: registrar dedupe: ")
}

// TestPostgresDeduper_Seen_RowsAffectedErrorIsFalseAndWrapped: si no se puede saber cuántas filas
// tocó el INSERT, tampoco se sabe si era un duplicado: false y el error envuelto.
func TestPostgresDeduper_Seen_RowsAffectedErrorIsFalseAndWrapped(t *testing.T) {
	d, fake := newDeduper(t, WithSweep(1, 10))
	boom := errors.New("sin filas afectadas")
	fake.set(func(f *fakeDB) { f.affectedErr = boom })
	seen, err := d.Seen(context.Background(), "session-1", "wamid.A")
	requireFalseAndWrapped(t, seen, err, boom, "ingest: filas afectadas del dedupe: ")
	if sweeps := fake.sweeps(); len(sweeps) != 0 {
		t.Errorf("un Seen fallido disparó %d podas, quería 0", len(sweeps))
	}
}

// TestPostgresDeduper_Sweep_OneEveryNNewKeys: la poda sale una vez cada «cadencia» claves nuevas,
// SÍNCRONA: es la sentencia que sigue al INSERT de la clave que la dispara.
func TestPostgresDeduper_Sweep_OneEveryNNewKeys(t *testing.T) {
	d, fake := newDeduper(t, WithSweep(3, 50))
	seeNew(t, d, "k", 2)
	if kinds := fake.kinds(); !slices.Equal(kinds, []string{kindInsert, kindInsert}) {
		t.Fatalf("tras 2 claves nuevas: %v, quería dos INSERT y ninguna poda", kinds)
	}
	seeNew(t, d, "l", 1)
	round := []string{kindInsert, kindInsert, kindInsert, kindSweep}
	if kinds := fake.kinds(); !slices.Equal(kinds, round) {
		t.Fatalf("tras la 3.ª clave nueva: %v, quería %v (la poda ya salió al volver Seen)", kinds, round)
	}
	seeNew(t, d, "m", 6)
	want := slices.Concat(round, round, round)
	if kinds := fake.kinds(); !slices.Equal(kinds, want) {
		t.Errorf("tras 9 claves nuevas: %v, quería %v", kinds, want)
	}
}

// TestPostgresDeduper_Sweep_DuplicatesDoNotAdvance: solo las claves NUEVAS cuentan para la
// cadencia; los duplicados, por muchos que sean, ni podan ni acercan la poda.
func TestPostgresDeduper_Sweep_DuplicatesDoNotAdvance(t *testing.T) {
	d, fake := newDeduper(t, WithSweep(3, 50))
	seeNew(t, d, "k", 2)
	for range 10 {
		seen, err := d.Seen(context.Background(), "session-1", "wamid.k-1")
		if err != nil || !seen {
			t.Fatalf("Seen de una clave repetida = (%v, %v), quería (true, nil)", seen, err)
		}
	}
	if sweeps := fake.sweeps(); len(sweeps) != 0 {
		t.Fatalf("2 claves nuevas y 10 duplicados dispararon %d podas, quería 0", len(sweeps))
	}
	seeNew(t, d, "l", 1)
	if sweeps := fake.sweeps(); len(sweeps) != 1 {
		t.Errorf("la 3.ª clave nueva disparó %d podas, quería 1", len(sweeps))
	}
}

// TestPostgresDeduper_Sweep_FailedInsertDoesNotAdvance: un INSERT fallido no es una clave nueva.
func TestPostgresDeduper_Sweep_FailedInsertDoesNotAdvance(t *testing.T) {
	d, fake := newDeduper(t, WithSweep(2, 50))
	seeNew(t, d, "k", 1)
	fake.set(func(f *fakeDB) { f.insertErr = errors.New("base caída") })
	for range 3 {
		if _, err := d.Seen(context.Background(), "session-1", "wamid.failed"); err == nil {
			t.Fatal("Seen con la base caída devolvió err = nil")
		}
	}
	if sweeps := fake.sweeps(); len(sweeps) != 0 {
		t.Fatalf("1 clave nueva y 3 INSERT fallidos dispararon %d podas, quería 0", len(sweeps))
	}
	fake.set(func(f *fakeDB) { f.insertErr = nil })
	seeNew(t, d, "l", 1)
	if sweeps := fake.sweeps(); len(sweeps) != 1 {
		t.Errorf("la 2.ª clave nueva disparó %d podas, quería 1", len(sweeps))
	}
}

// TestPostgresDeduper_Sweep_EmitsTheBoundedDelete: la poda es el DELETE literal, con el corte
// (ahora − retención, en UTC) y el lote.
func TestPostgresDeduper_Sweep_EmitsTheBoundedDelete(t *testing.T) {
	d, fake := newDeduper(t, WithRetention(36*time.Hour), WithSweep(1, 50))
	before := time.Now()
	seeNew(t, d, "k", 1)
	after := time.Now()
	sweeps := fake.sweeps()
	if len(sweeps) != 1 {
		t.Fatalf("con cadencia 1, una clave nueva disparó %d podas, quería 1", len(sweeps))
	}
	if sweeps[0].query != sweepSQL {
		t.Errorf("SQL emitido:\n%q\nquería:\n%q", sweeps[0].query, sweepSQL)
	}
	requireCutoff(t, sweeps[0], 36*time.Hour, before, after)
	requireBatch(t, sweeps[0], 50)
}

// TestPostgresDeduper_Sweep_ErrorIsDiscarded: la poda es best-effort: si falla, Seen contesta lo
// mismo, y la siguiente vuelve a intentarlo a su cadencia.
func TestPostgresDeduper_Sweep_ErrorIsDiscarded(t *testing.T) {
	d, fake := newDeduper(t, WithSweep(2, 50))
	fake.set(func(f *fakeDB) { f.sweepErr = errors.New("poda caída") })
	seeNew(t, d, "k", 2) // seeNew exige (false, nil) también en la clave que poda
	if sweeps := fake.sweeps(); len(sweeps) != 1 {
		t.Fatalf("tras 2 claves nuevas llegaron %d podas, quería 1 (la que falla)", len(sweeps))
	}
	// La clave que disparó la poda fallida quedó registrada: ahora es un duplicado.
	seen, err := d.Seen(context.Background(), "session-1", "wamid.k-2")
	if err != nil || !seen {
		t.Errorf("Seen de la clave que podó = (%v, %v), quería (true, nil)", seen, err)
	}
	seeNew(t, d, "l", 2)
	if sweeps := fake.sweeps(); len(sweeps) != 2 {
		t.Errorf("tras 4 claves nuevas llegaron %d podas, quería 2 (la siguiente reintenta)", len(sweeps))
	}
}

// TestPostgresDeduper_Defaults: sin opciones, una poda cada 512 claves nuevas, de hasta 1000
// filas, con 7 días de retención.
func TestPostgresDeduper_Defaults(t *testing.T) {
	d, fake := newDeduper(t)
	before := time.Now()
	n, sweep := newKeysUntilSweep(t, d, fake)
	after := time.Now()
	if n != wantDefaultSweepEvery {
		t.Errorf("la primera poda llegó a las %d claves nuevas, quería %d", n, wantDefaultSweepEvery)
	}
	requireBatch(t, sweep, wantDefaultSweepBatch)
	requireCutoff(t, sweep, wantDefaultRetention, before, after)
}

// TestOptions_NonPositiveAreIgnored: una opción con un valor <= 0 deja lo que hubiera —cada valor
// de WithSweep por separado—, y entre dos válidas gana la última.
func TestOptions_NonPositiveAreIgnored(t *testing.T) {
	cases := []struct {
		name          string
		opts          []Option
		wantEvery     int
		wantBatch     int64
		wantRetention time.Duration
	}{
		{
			name:      "valid options apply",
			opts:      []Option{WithRetention(24 * time.Hour), WithSweep(4, 50)},
			wantEvery: 4, wantBatch: 50, wantRetention: 24 * time.Hour,
		},
		{
			name:      "zero sweep keeps both",
			opts:      []Option{WithSweep(4, 50), WithSweep(0, 0)},
			wantEvery: 4, wantBatch: 50, wantRetention: wantDefaultRetention,
		},
		{
			name:      "zero cadence keeps it and takes the batch",
			opts:      []Option{WithSweep(4, 50), WithSweep(0, 9)},
			wantEvery: 4, wantBatch: 9, wantRetention: wantDefaultRetention,
		},
		{
			name:      "negative batch keeps it and takes the cadence",
			opts:      []Option{WithSweep(4, 50), WithSweep(6, -1)},
			wantEvery: 6, wantBatch: 50, wantRetention: wantDefaultRetention,
		},
		{
			name:      "zero batch keeps the default batch",
			opts:      []Option{WithSweep(3, 0)},
			wantEvery: 3, wantBatch: wantDefaultSweepBatch, wantRetention: wantDefaultRetention,
		},
		{
			name:      "smallest valid sweep applies",
			opts:      []Option{WithSweep(4, 50), WithSweep(1, 1)},
			wantEvery: 1, wantBatch: 1, wantRetention: wantDefaultRetention,
		},
		{
			name:      "zero retention keeps it",
			opts:      []Option{WithRetention(time.Hour), WithRetention(0), WithSweep(2, 50)},
			wantEvery: 2, wantBatch: 50, wantRetention: time.Hour,
		},
		{
			name:      "negative retention keeps it",
			opts:      []Option{WithRetention(time.Hour), WithRetention(-time.Minute), WithSweep(2, 50)},
			wantEvery: 2, wantBatch: 50, wantRetention: time.Hour,
		},
		{
			name:      "zero retention keeps the default",
			opts:      []Option{WithRetention(0), WithSweep(2, 50)},
			wantEvery: 2, wantBatch: 50, wantRetention: wantDefaultRetention,
		},
		{
			name:      "last valid retention wins",
			opts:      []Option{WithRetention(time.Hour), WithRetention(2 * time.Hour), WithSweep(2, 50)},
			wantEvery: 2, wantBatch: 50, wantRetention: 2 * time.Hour,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, fake := newDeduper(t, c.opts...)
			before := time.Now()
			n, sweep := newKeysUntilSweep(t, d, fake)
			after := time.Now()
			if n != c.wantEvery {
				t.Errorf("la primera poda llegó a las %d claves nuevas, quería %d", n, c.wantEvery)
			}
			requireBatch(t, sweep, c.wantBatch)
			requireCutoff(t, sweep, c.wantRetention, before, after)
		})
	}
}

// TestPostgresDeduper_Seen_ConcurrentNewKeys: la cuenta de la cadencia aguanta llamadas en
// paralelo: 64 claves nuevas con cadencia 8 son exactamente 8 podas.
func TestPostgresDeduper_Seen_ConcurrentNewKeys(t *testing.T) {
	d, fake := newDeduper(t, WithSweep(8, 50))
	const keys = 64
	var wg sync.WaitGroup
	errs := make(chan error, keys)
	for i := range keys {
		wg.Go(func() {
			seen, err := d.Seen(context.Background(), "session-1", fmt.Sprintf("wamid.parallel-%d", i))
			if err == nil && seen {
				err = fmt.Errorf("la clave nueva %d salió como duplicado", i)
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Seen en paralelo: %v", err)
		}
	}
	if sweeps := fake.sweeps(); len(sweeps) != keys/8 {
		t.Errorf("%d claves nuevas en paralelo con cadencia 8 dispararon %d podas, quería %d", keys, len(sweeps), keys/8)
	}
}

// requireFalseAndWrapped afirma el contrato del error de Seen: false, el prefijo y la causa.
func requireFalseAndWrapped(t *testing.T, seen bool, err, cause error, prefix string) {
	t.Helper()
	if seen {
		t.Error("Seen con error devolvió true: el error viaja siempre con false")
	}
	if err == nil {
		t.Fatalf("err = nil, quería un error con el prefijo %q", prefix)
	}
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("err = %q, quería el prefijo %q", err, prefix)
	}
	if !errors.Is(err, cause) {
		t.Errorf("err = %q no envuelve la causa %q", err, cause)
	}
}
