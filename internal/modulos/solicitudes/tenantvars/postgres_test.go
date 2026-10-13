package tenantvars_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/tenantvars"
)

// Aserciones de compilación de lo que Postgres promete: las firmas del puerto y un constructor
// que recibe el *sql.DB y no devuelve error.
var (
	_ func(*sql.DB) *tenantvars.Postgres                                                 = tenantvars.NewPostgres
	_ func(*tenantvars.Postgres, context.Context, string) ([]tenantvars.Variable, error) = (*tenantvars.Postgres).List
	_ func(*tenantvars.Postgres, context.Context, string, map[string]string) error       = (*tenantvars.Postgres).Replace
)

// Las tres sentencias del adaptador, byte a byte (con su sangría): son las del fichero viejo.
const (
	listSQL = `
		SELECT key, value, updated_at
		FROM public.tenant_variables
		WHERE tenant_id = $1
		ORDER BY key
	`
	deleteSQL = `
			DELETE FROM public.tenant_variables
			WHERE tenant_id = $1 AND key <> ALL($2::text[])
		`
	upsertSQL = `
			INSERT INTO public.tenant_variables (tenant_id, key, value)
			SELECT $1, k, v FROM unnest($2::text[], $3::text[]) AS t(k, v)
			ON CONFLICT (tenant_id, key) DO UPDATE
			   SET value = EXCLUDED.value, updated_at = now()
			 WHERE public.tenant_variables.value IS DISTINCT FROM EXCLUDED.value
		`
)

// newPostgres monta el adaptador sobre el driver de mentira.
func newPostgres(t *testing.T) (*tenantvars.Postgres, *fakeDB) {
	t.Helper()
	fake := &fakeDB{}
	return tenantvars.NewPostgres(fake.open(t)), fake
}

// TestNewPostgres_DoesNotQuery: construir el store no toca la base.
func TestNewPostgres_DoesNotQuery(t *testing.T) {
	store, fake := newPostgres(t)
	if store == nil {
		t.Fatal("NewPostgres devolvió nil")
	}
	if seen := fake.seen(); len(seen) != 0 {
		t.Errorf("construir el store emitió %d eventos, quería 0: %+v", len(seen), seen)
	}
}

// TestPostgres_List_EmitsTheQuery: un List es UNA consulta, la literal, fuera de transacción y
// con el tenant como único argumento.
func TestPostgres_List_EmitsTheQuery(t *testing.T) {
	store, fake := newPostgres(t)
	if _, err := store.List(context.Background(), "tenant-1"); err != nil {
		t.Fatalf("List: error inesperado %v", err)
	}
	seen := fake.seen()
	if len(seen) != 1 {
		t.Fatalf("List emitió %d eventos, quería 1: %+v", len(seen), seen)
	}
	got := seen[0]
	if got.kind != eventQuery || got.inTx {
		t.Errorf("List emitió un %q (en transacción: %v), quería una consulta suelta", got.kind, got.inTx)
	}
	if got.query != listSQL {
		t.Errorf("SQL emitido:\n%q\nquería:\n%q", got.query, listSQL)
	}
	if len(got.args) != 1 || got.args[0] != "tenant-1" {
		t.Errorf("argumentos de List = %v, quería [tenant-1]", got.args)
	}
}

// TestPostgres_List_NoRows_EmptyNotNil: sin filas, un slice vacío que no es nil, y sin error.
func TestPostgres_List_NoRows_EmptyNotNil(t *testing.T) {
	store, _ := newPostgres(t)
	got, err := store.List(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("List sin filas: error inesperado %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("List sin filas = %#v, quería un slice vacío no nil", got)
	}
}

// TestPostgres_List_MapsTheRows: cada fila sale con sus tres columnas en su sitio y en el orden
// en que las da la base (el adaptador no reordena).
func TestPostgres_List_MapsTheRows(t *testing.T) {
	store, fake := newPostgres(t)
	first := time.Unix(1_700_000_000, 0).UTC()
	second := first.Add(time.Minute)
	fake.script(reply{rows: [][]driver.Value{
		{"shipping", "", second},
		{"currency", "  Bs  ", first},
	}})
	got, err := store.List(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("List: error inesperado %v", err)
	}
	want := []tenantvars.Variable{
		{Key: "shipping", Value: "", UpdatedAt: second},
		{Key: "currency", Value: "  Bs  ", UpdatedAt: first},
	}
	if len(got) != len(want) {
		t.Fatalf("List devolvió %d filas, quería %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("fila %d = %+v, quería %+v", i, got[i], want[i])
		}
	}
}

// TestPostgres_List_Errors: los cuatro fallos posibles salen envueltos, cada uno con su prefijo
// byte a byte, y sin filas a medias. El del cierre no pisa a uno anterior.
func TestPostgres_List_Errors(t *testing.T) {
	boom := errors.New("base caída")
	closeBoom := errors.New("cierre roto")
	at := time.Unix(1_700_000_000, 0).UTC()
	good := []driver.Value{"currency", "Bs", at}
	// Una clave NULL no se puede escanear a string: el error es de database/sql.
	bad := []driver.Value{nil, "Bs", at}
	cases := []struct {
		name   string
		reply  reply
		prefix string
		cause  error
	}{
		{"query fails", reply{err: boom}, "tenantvars: listar variables: ", boom},
		{"row does not scan", reply{rows: [][]driver.Value{good, bad}}, "tenantvars: leer variable: ", nil},
		{"iteration fails", reply{rows: [][]driver.Value{good}, endErr: boom}, "tenantvars: recorrer variables: ", boom},
		{"close fails", reply{rows: [][]driver.Value{good}, closeErr: closeBoom}, "tenantvars: cerrar filas de variables: ", closeBoom},
		{
			"close fails after a scan failure: the first error wins",
			reply{rows: [][]driver.Value{bad}, closeErr: closeBoom}, "tenantvars: leer variable: ", nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			fake.script(c.reply)
			got, err := store.List(context.Background(), "tenant-1")
			requireWrapped(t, err, c.cause, c.prefix)
			if got != nil {
				t.Errorf("List con error devolvió %+v, quería nil", got)
			}
			if c.cause == nil && errors.Is(err, closeBoom) {
				t.Errorf("err = %q arrastra el fallo del cierre; quería solo el primero", err)
			}
		})
	}
}

// TestPostgres_Replace_DeletesAndUpsertsInOneTransaction: abrir, el DELETE de las retiradas, el
// upsert y confirmar, en ese orden y las dos sentencias DENTRO de la transacción; con el SQL
// literal y con las claves y los valores como []string emparejados por posición.
func TestPostgres_Replace_DeletesAndUpsertsInOneTransaction(t *testing.T) {
	store, fake := newPostgres(t)
	vars := map[string]string{"currency": "Bs", "greeting": "  ¡Hola!  ", "empty": ""}
	if err := store.Replace(context.Background(), "tenant-1", vars); err != nil {
		t.Fatalf("Replace: error inesperado %v", err)
	}
	seen := fake.seen()
	requireKinds(t, seen, eventBegin, eventExec, eventExec, eventCommit)
	del, ups := seen[1], seen[2]
	if !del.inTx || !ups.inTx {
		t.Errorf("sentencias en transacción: DELETE=%v, upsert=%v; quería las dos dentro", del.inTx, ups.inTx)
	}
	if del.query != deleteSQL {
		t.Errorf("SQL del borrado:\n%q\nquería:\n%q", del.query, deleteSQL)
	}
	if ups.query != upsertSQL {
		t.Errorf("SQL del upsert:\n%q\nquería:\n%q", ups.query, upsertSQL)
	}
	delKeys := requireArgs(t, "DELETE", del, "tenant-1", 1)[0]
	sent := requireArgs(t, "upsert", ups, "tenant-1", 2)
	keys, values := sent[0], sent[1]
	if len(keys) != len(vars) || len(values) != len(vars) || len(delKeys) != len(vars) {
		t.Fatalf("claves del DELETE %v, claves %v y valores %v del upsert: quería %d de cada", delKeys, keys, values, len(vars))
	}
	// El orden sale de un mapa: lo que importa es que clave y valor viajen en la MISMA posición
	// y que el DELETE conserve las mismas claves que el upsert escribe.
	for i, k := range keys {
		want, ok := vars[k]
		if !ok {
			t.Errorf("el upsert manda la clave %q, que no estaba en vars", k)
		}
		if values[i] != want {
			t.Errorf("valor en la posición %d (clave %q) = %q, quería %q", i, k, values[i], want)
		}
		if delKeys[i] != k {
			t.Errorf("clave %d del DELETE = %q y del upsert = %q; quería la misma lista", i, delKeys[i], k)
		}
	}
}

// TestPostgres_Replace_NoVariables_OnlyDeletes: con un mapa vacío o nil se emite SOLO el DELETE,
// en su transacción, y sus claves viajan como un []string vacío que NO es nil (con nil viajaría
// NULL y `key <> ALL(NULL)` no borraría nada). El upsert no se ejecuta.
func TestPostgres_Replace_NoVariables_OnlyDeletes(t *testing.T) {
	cases := []struct {
		name string
		vars map[string]string
	}{
		{"empty map", map[string]string{}},
		{"nil map", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			if err := store.Replace(context.Background(), "tenant-1", c.vars); err != nil {
				t.Fatalf("Replace: error inesperado %v", err)
			}
			seen := fake.seen()
			requireKinds(t, seen, eventBegin, eventExec, eventCommit)
			del := seen[1]
			if del.query != deleteSQL || !del.inTx {
				t.Errorf("la única sentencia (en transacción: %v) es:\n%q\nquería el DELETE en transacción", del.inTx, del.query)
			}
			if keys := requireArgs(t, "DELETE", del, "tenant-1", 1)[0]; len(keys) != 0 {
				t.Errorf("claves del DELETE = %v, quería ninguna", keys)
			}
		})
	}
}

// TestPostgres_Replace_Errors: el fallo de cada sentencia sale envuelto con su prefijo byte a
// byte, la transacción se REVIERTE (nunca se confirma) y tras el fallo del DELETE no se llega a
// emitir el upsert.
func TestPostgres_Replace_Errors(t *testing.T) {
	boom := errors.New("base caída")
	cases := []struct {
		name    string
		replies []reply
		prefix  string
		kinds   []string
	}{
		{
			"delete fails", []reply{{err: boom}},
			"tenantvars: borrar variables retiradas: ", []string{eventBegin, eventExec, eventRollback},
		},
		{
			"upsert fails", []reply{{}, {err: boom}},
			"tenantvars: guardar variables: ", []string{eventBegin, eventExec, eventExec, eventRollback},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			fake.script(c.replies...)
			err := store.Replace(context.Background(), "tenant-1", map[string]string{"currency": "Bs"})
			requireWrapped(t, err, boom, c.prefix)
			requireKinds(t, fake.seen(), c.kinds...)
		})
	}
}

// TestPostgres_Replace_TransactionErrors: si la transacción no se abre no se emite ninguna
// sentencia, y si no se confirma el error llega al llamante; los dos envuelven la causa.
func TestPostgres_Replace_TransactionErrors(t *testing.T) {
	boom := errors.New("base caída")
	t.Run("begin fails", func(t *testing.T) {
		store, fake := newPostgres(t)
		fake.failTx(boom, nil)
		err := store.Replace(context.Background(), "tenant-1", map[string]string{"currency": "Bs"})
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, quería uno que envuelva el fallo de abrir la transacción", err)
		}
		requireKinds(t, fake.seen())
	})
	t.Run("commit fails", func(t *testing.T) {
		store, fake := newPostgres(t)
		fake.failTx(nil, boom)
		err := store.Replace(context.Background(), "tenant-1", map[string]string{"currency": "Bs"})
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, quería uno que envuelva el fallo de confirmar", err)
		}
		requireKinds(t, fake.seen(), eventBegin, eventExec, eventExec)
	})
}

// requireKinds afirma que llegaron al driver exactamente esos eventos, en ese orden.
func requireKinds(t *testing.T, seen []event, want ...string) {
	t.Helper()
	got := make([]string, 0, len(seen))
	for _, e := range seen {
		got = append(got, e.kind)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("eventos en el driver = %v, quería %v", got, want)
	}
}

// requireArgs afirma que la sentencia lleva el tenant como $1 y, detrás, exactamente lists
// argumentos []string no nil, y los devuelve en orden.
func requireArgs(t *testing.T, what string, e event, tenant string, lists int) [][]string {
	t.Helper()
	if len(e.args) != 1+lists || e.args[0] != tenant {
		t.Fatalf("argumentos del %s = %v, quería [%s] y %d listas", what, e.args, tenant, lists)
	}
	out := make([][]string, 0, lists)
	for i, arg := range e.args[1:] {
		out = append(out, requireStrings(t, fmt.Sprintf("$%d del %s", i+2, what), arg))
	}
	return out
}

// requireStrings afirma que el argumento es un []string NO nil y lo devuelve.
func requireStrings(t *testing.T, what string, arg driver.Value) []string {
	t.Helper()
	got, ok := arg.([]string)
	if !ok {
		t.Fatalf("%s = %v (%T), quería un []string", what, arg, arg)
	}
	if got == nil {
		t.Fatalf("%s es un []string nil: viajaría como NULL; quería uno no nil", what)
	}
	return got
}

// requireWrapped afirma que err empieza por prefix y, si cause no es nil, que lo envuelve.
func requireWrapped(t *testing.T, err, cause error, prefix string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, quería un error con el prefijo %q", prefix)
	}
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("err = %q, quería el prefijo %q", err, prefix)
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Errorf("err = %q no envuelve la causa %q", err, cause)
	}
}
