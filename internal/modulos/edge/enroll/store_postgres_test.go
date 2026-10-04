package enroll_test

// Los tests de fichero de enroll.PostgresCodeStore, con el driver de database/sql de mentira
// (fakedb_test.go): el texto EXACTO de las dos sentencias, sus argumentos y el mapeo de filas y
// errores. Que el UPDATE sea de verdad atómico y de un solo uso en Postgres lo prueba
// enrollhelpertest.ContratoCodeStore en los procesos de F9 (sesión F3-05).

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll/enrollhelpertest"
)

// Las dos sentencias, byte a byte, escritas aquí a mano (los literales de
// internal/gateway/enroll/store_postgres.go @ 8896f13, con su sangría).
const (
	sqlCreateCode = "\n" +
		"\t\tINSERT INTO public.enrollment_codes (code, tenant_id, expires_at)\n" +
		"\t\tVALUES ($1, $2, $3)\n" +
		"\t"
	// El consumo: UN solo UPDATE condicional con RETURNING. Las dos condiciones del WHERE son
	// la promesa: «sin usar» (un solo uso) y «sin vencer».
	sqlConsumeCode = "\n" +
		"\t\tUPDATE public.enrollment_codes\n" +
		"\t\tSET used_at = now()\n" +
		"\t\tWHERE code = $1 AND used_at IS NULL AND expires_at > now()\n" +
		"\t\tRETURNING tenant_id::text\n" +
		"\t"
)

// newCodeStore monta el adaptador sobre el driver de mentira.
func newCodeStore(t *testing.T) (*enroll.PostgresCodeStore, *fakeDB) {
	t.Helper()
	fake, db := openFakeDB(t)
	return enroll.NewPostgresCodeStore(db), fake
}

// requireOnlyStatement afirma que al driver llegó UNA sentencia, con ese texto y esos argumentos.
func requireOnlyStatement(t *testing.T, fake *fakeDB, wantQuery string, wantArgs ...driver.Value) {
	t.Helper()
	stmts := fake.statements()
	if len(stmts) != 1 {
		t.Fatalf("llegaron %d sentencias al driver, quería 1: %+v", len(stmts), stmts)
	}
	if stmts[0].query != wantQuery {
		t.Errorf("SQL emitido:\n%q\nquería, byte a byte:\n%q", stmts[0].query, wantQuery)
	}
	if !reflect.DeepEqual(stmts[0].args, wantArgs) {
		t.Errorf("argumentos = %#v, quería %#v", stmts[0].args, wantArgs)
	}
}

// TestNewPostgresCodeStore_DoesNotTouchTheDatabase: construir no consulta nada.
func TestNewPostgresCodeStore_DoesNotTouchTheDatabase(t *testing.T) {
	store, fake := newCodeStore(t)
	if store == nil {
		t.Fatal("NewPostgresCodeStore devolvió nil")
	}
	if n := len(fake.statements()); n != 0 {
		t.Errorf("construir el adaptador emitió %d sentencias, quería 0", n)
	}
}

// TestPostgresCodeStoreCreate: el INSERT exacto y sus tres argumentos; el fallo, envuelto.
func TestPostgresCodeStoreCreate(t *testing.T) {
	expiry := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
	store, fake := newCodeStore(t)
	if err := store.Create(context.Background(), testCode, testTenant, expiry); err != nil {
		t.Fatalf("Create: error inesperado %v", err)
	}
	requireOnlyStatement(t, fake, sqlCreateCode, testCode, testTenant, expiry)

	store, fake = newCodeStore(t)
	cause := errors.New("clave duplicada")
	fake.fail(cause)
	err := store.Create(context.Background(), testCode, testTenant, expiry)
	if !errors.Is(err, cause) || !strings.HasPrefix(err.Error(), "enroll: sembrando enrollment_code: ") {
		t.Errorf("Create con el driver caído = %v, quería \"enroll: sembrando enrollment_code: …\" envolviendo la causa", err)
	}
}

// TestPostgresCodeStoreConsume_AtomicSingleUseStatement: el consumo es UNA sentencia, el
// UPDATE … RETURNING literal, y devuelve el tenant de la fila.
func TestPostgresCodeStoreConsume_AtomicSingleUseStatement(t *testing.T) {
	store, fake := newCodeStore(t)
	fake.answer([]string{"tenant_id"}, []driver.Value{testTenant})
	tenant, err := store.Consume(context.Background(), testCode)
	if err != nil || tenant != testTenant {
		t.Fatalf("Consume = (%q, %v), quería (%q, nil)", tenant, err, testTenant)
	}
	requireOnlyStatement(t, fake, sqlConsumeCode, testCode)

	// Las dos guardas, dichas sobre el texto: sin ellas un código usado o vencido se consumiría.
	for _, guard := range []string{"used_at IS NULL", "expires_at > now()", "SET used_at = now()", "RETURNING tenant_id::text"} {
		if !strings.Contains(sqlConsumeCode, guard) {
			t.Errorf("la sentencia de consumo no contiene %q", guard)
		}
	}
}

// TestPostgresCodeStoreConsume_NoRow_IsErrCodeInvalid: si el UPDATE no devuelve fila (ausente,
// vencido o ya usado) el error es ErrCodeInvalid, el centinela tal cual, sin revelar la causa.
func TestPostgresCodeStoreConsume_NoRow_IsErrCodeInvalid(t *testing.T) {
	store, fake := newCodeStore(t)
	tenant, err := store.Consume(context.Background(), testCode)
	if !errors.Is(err, enroll.ErrCodeInvalid) || errors.Unwrap(err) != nil {
		t.Errorf("Consume sin fila: error %v, quería ErrCodeInvalid tal cual", err)
	}
	if tenant != "" {
		t.Errorf("Consume sin fila devolvió el tenant %q", tenant)
	}
	requireOnlyStatement(t, fake, sqlConsumeCode, testCode)
}

// TestPostgresCodeStoreConsume_DriverFailure_IsNotACodeError: un fallo de infraestructura no se
// disfraza de código inválido (el Server lo mapea a Internal, no a PermissionDenied).
func TestPostgresCodeStoreConsume_DriverFailure_IsNotACodeError(t *testing.T) {
	store, fake := newCodeStore(t)
	cause := errors.New("conexión rota")
	fake.fail(cause)
	tenant, err := store.Consume(context.Background(), testCode)
	if !errors.Is(err, cause) || !strings.HasPrefix(err.Error(), "enroll: consumiendo enrollment_code: ") {
		t.Fatalf("Consume con el driver caído = %v, quería \"enroll: consumiendo enrollment_code: …\" envolviendo la causa", err)
	}
	if errors.Is(err, enroll.ErrCodeInvalid) || tenant != "" {
		t.Errorf("un fallo del driver se confundió con un código inválido (tenant %q, err %v)", tenant, err)
	}
}

// TestPostgresCodeStoreConsume_CodeTravelsVerbatim: el código NO se normaliza. Cada variante
// adversaria llega al driver byte a byte como $1 —ni recortada, ni en otra caja, ni rechazada
// antes por vacía— y, como no hay fila para ella, es ErrCodeInvalid.
func TestPostgresCodeStoreConsume_CodeTravelsVerbatim(t *testing.T) {
	for name, code := range enrollhelpertest.AdversarialCodes(testCode) {
		t.Run(name, func(t *testing.T) {
			store, fake := newCodeStore(t)
			if _, err := store.Consume(context.Background(), code); !errors.Is(err, enroll.ErrCodeInvalid) {
				t.Errorf("Consume(%q): error %v, quería ErrCodeInvalid", code, err)
			}
			requireOnlyStatement(t, fake, sqlConsumeCode, code)
		})
	}
}
