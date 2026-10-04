package enroll_test

// Los tests de fichero de enroll.PostgresEdgeCertRepository, con el driver de database/sql de
// mentira (fakedb_test.go): el INSERT exacto, sus argumentos y el mapeo del error. Que ese SQL
// guarde de verdad lo prueba enrollhelpertest.ContratoEdgeCertRepository en los procesos de F9.

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

// Las dos implementaciones del puerto.
var (
	_ enroll.EdgeCertRepository = (*enroll.PostgresEdgeCertRepository)(nil)
	_ enroll.EdgeCertRepository = (*enrollhelpertest.MemoriaEdgeCertRepository)(nil)
)

// El INSERT, byte a byte, escrito aquí a mano (el literal de
// internal/gateway/enroll/edgecert.go @ 8896f13, con su sangría).
const sqlInsertEdgeCert = "\n" +
	"\t\tINSERT INTO public.edge_certs\n" +
	"\t\t\t(tenant_id, subject_cn, serial_number, fingerprint, not_before, not_after, cert_pem)\n" +
	"\t\tVALUES ($1, $2, $3, $4, $5, $6, $7)\n" +
	"\t"

func sampleCertRecord() enroll.EdgeCertRecord {
	return enroll.EdgeCertRecord{
		TenantID:     testTenant,
		SubjectCN:    testEdgeCN,
		SerialNumber: "a1b2c3",
		Fingerprint:  strings.Repeat("ab", 32),
		NotBefore:    time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC),
		NotAfter:     time.Date(2031, 4, 2, 3, 4, 5, 0, time.UTC),
		CertPEM:      []byte("-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----\n"),
	}
}

// TestNewPostgresEdgeCertRepository_DoesNotTouchTheDatabase: construir no consulta nada.
func TestNewPostgresEdgeCertRepository_DoesNotTouchTheDatabase(t *testing.T) {
	fake, db := openFakeDB(t)
	if enroll.NewPostgresEdgeCertRepository(db) == nil {
		t.Fatal("NewPostgresEdgeCertRepository devolvió nil")
	}
	if n := len(fake.statements()); n != 0 {
		t.Errorf("construir el adaptador emitió %d sentencias, quería 0", n)
	}
}

// TestPostgresEdgeCertCreate_InsertsTheRecord: un INSERT, el texto exacto, los siete campos en
// su orden y el PEM como texto (la columna cert_pem es TEXT).
func TestPostgresEdgeCertCreate_InsertsTheRecord(t *testing.T) {
	fake, db := openFakeDB(t)
	rec := sampleCertRecord()
	if err := enroll.NewPostgresEdgeCertRepository(db).Create(context.Background(), rec); err != nil {
		t.Fatalf("Create: error inesperado %v", err)
	}
	stmts := fake.statements()
	if len(stmts) != 1 {
		t.Fatalf("llegaron %d sentencias al driver, quería 1", len(stmts))
	}
	if stmts[0].query != sqlInsertEdgeCert {
		t.Errorf("SQL emitido:\n%q\nquería, byte a byte:\n%q", stmts[0].query, sqlInsertEdgeCert)
	}
	want := []driver.Value{
		rec.TenantID, rec.SubjectCN, rec.SerialNumber, rec.Fingerprint, rec.NotBefore, rec.NotAfter, string(rec.CertPEM),
	}
	if !reflect.DeepEqual(stmts[0].args, want) {
		t.Errorf("argumentos = %#v, quería %#v", stmts[0].args, want)
	}
}

// TestPostgresEdgeCertCreate_DriverFailure_IsWrapped: el fallo vuelve envuelto, con su texto.
func TestPostgresEdgeCertCreate_DriverFailure_IsWrapped(t *testing.T) {
	fake, db := openFakeDB(t)
	cause := errors.New("violación de unicidad")
	fake.fail(cause)
	err := enroll.NewPostgresEdgeCertRepository(db).Create(context.Background(), sampleCertRecord())
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, quería uno que envuelva la causa", err)
	}
	if !strings.HasPrefix(err.Error(), "enroll: persistiendo edge_cert: ") {
		t.Errorf("error = %q, quería el prefijo %q", err, "enroll: persistiendo edge_cert: ")
	}
}
