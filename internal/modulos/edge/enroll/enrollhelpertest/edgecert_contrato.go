package enrollhelpertest

// La suite del puerto enroll.EdgeCertRepository: guarda el registro del certificado emitido.

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll"
	"github.com/google/uuid"
)

// MontajeEdgeCertRepository es lo que cada implementación entrega a ContratoEdgeCertRepository
// para UN caso. Tiene que venir limpio, sin registros.
type MontajeEdgeCertRepository struct {
	// Repository es la implementación bajo prueba.
	Repository enroll.EdgeCertRepository
	// SeedTenant crea un tenant que existe y devuelve su id, con forma de UUID y distinto en
	// cada llamada (con Postgres, una fila de public.tenants: public.edge_certs la referencia
	// por clave foránea). Falla el test t si no puede sembrar.
	SeedTenant func(t *testing.T) string
	// Records devuelve TODOS los registros guardados, en cualquier orden. El puerto solo tiene
	// Create, así que leer es cosa del montaje: con Postgres, un SELECT sobre public.edge_certs;
	// con el doble, MemoriaEdgeCertRepository.Records. Falla el test t si no puede leer.
	Records func(t *testing.T) []enroll.EdgeCertRecord
}

// EdgeCertRecord es el registro del puerto que cruza el montaje (MontajeEdgeCertRepository.Records),
// con nombre de la suite: un montaje que vive fuera del árbol de enroll —la pasada contra Postgres
// de test/procesos, que del paquete del puerto solo puede nombrar constructores (R9.4.d, candado
// ProcessImports, regla 3b)— lo nombra por aquí. Es un alias, no una copia: el tipo es el mismo
// (el precedente es outhelpertest.Invitation, D-F2-9; hallazgo 75 de F3).
type EdgeCertRecord = enroll.EdgeCertRecord

// ContratoEdgeCertRepository ejecuta las promesas de enroll.EdgeCertRepository contra la
// implementación que devuelve nuevo, con un montaje limpio por caso. No salta nada.
//
// Lo que la suite NO afirma, porque las dos implementaciones divergen: un Fingerprint repetido
// (en Postgres la columna es UNIQUE y el segundo Create falla; el doble lo acepta) y un tenant
// que no existe (en Postgres lo rechaza la clave foránea). La suite solo usa huellas distintas
// y tenants sembrados.
func ContratoEdgeCertRepository(t *testing.T, nuevo func(t *testing.T) MontajeEdgeCertRepository) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("enrollhelpertest.ContratoEdgeCertRepository: nuevo es nil; hace falta una función que devuelva un MontajeEdgeCertRepository")
	}
	cases := []struct {
		name string
		run  func(t *testing.T, m MontajeEdgeCertRepository)
	}{
		{"Empty_HasNoRecords", caseCertEmpty},                    // limpio de entrada
		{"Create_StoresEveryField", caseCertStoresFields},        // guarda y recupera, campo a campo
		{"Create_KeepsEachRecord", caseCertKeepsEach},            // varios, de varios tenants
		{"ConcurrentCreate_StoresAll", caseCertConcurrentCreate}, // seguro en paralelo
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			switch {
			case m.Repository == nil:
				t.Fatal("MontajeEdgeCertRepository.Repository es nil")
			case m.SeedTenant == nil:
				t.Fatal("MontajeEdgeCertRepository.SeedTenant es nil: la suite necesita sembrar tenants")
			case m.Records == nil:
				t.Fatal("MontajeEdgeCertRepository.Records es nil: la suite necesita leer lo guardado")
			}
			c.run(t, m)
		})
	}
}

// Los instantes van en segundos enteros y en UTC para que sobrevivan a un timestamptz.
var (
	certNotBefore = time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
	certNotAfter  = time.Date(2031, 4, 2, 3, 4, 5, 0, time.UTC)
)

// certRecord fabrica un registro con huella y serial únicos por n. El PEM no es un certificado
// de verdad: el repositorio guarda texto, no lo interpreta.
func certRecord(tenant string, n int) enroll.EdgeCertRecord {
	return enroll.EdgeCertRecord{
		TenantID:     tenant,
		SubjectCN:    fmt.Sprintf("contract-edge-%d", n),
		SerialNumber: fmt.Sprintf("c0ffee%02x", n),
		Fingerprint:  fmt.Sprintf("%064x", n+1),
		NotBefore:    certNotBefore.Add(time.Duration(n) * time.Hour),
		NotAfter:     certNotAfter.Add(time.Duration(n) * time.Hour),
		CertPEM:      []byte(fmt.Sprintf("-----BEGIN CERTIFICATE-----\ncontract-%d\n-----END CERTIFICATE-----\n", n)),
	}
}

// seedCertTenant siembra un tenant y comprueba que su id es un UUID.
func seedCertTenant(t *testing.T, m MontajeEdgeCertRepository) string {
	t.Helper()
	tenant := m.SeedTenant(t)
	if _, err := uuid.Parse(tenant); err != nil {
		t.Fatalf("MontajeEdgeCertRepository.SeedTenant devolvió %q, que no es un UUID bien formado: %v", tenant, err)
	}
	return tenant
}

// createCert llama a Create y falla el test si devuelve error.
func createCert(t *testing.T, m MontajeEdgeCertRepository, rec enroll.EdgeCertRecord) {
	t.Helper()
	if err := m.Repository.Create(context.Background(), rec); err != nil {
		t.Fatalf("Create(%s): error inesperado %v", rec.SubjectCN, err)
	}
}

// requireStored afirma que entre los registros guardados hay exactamente uno con la huella de
// want y que todos sus campos son los de want.
func requireStored(t *testing.T, records []enroll.EdgeCertRecord, want enroll.EdgeCertRecord) {
	t.Helper()
	var found []enroll.EdgeCertRecord
	for _, r := range records {
		if r.Fingerprint == want.Fingerprint {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("hay %d registros con la huella %s, quería 1", len(found), want.Fingerprint)
	}
	got := found[0]
	if got.TenantID != want.TenantID || got.SubjectCN != want.SubjectCN || got.SerialNumber != want.SerialNumber {
		t.Errorf("registro guardado = (tenant %q, cn %q, serial %q), quería (%q, %q, %q)",
			got.TenantID, got.SubjectCN, got.SerialNumber, want.TenantID, want.SubjectCN, want.SerialNumber)
	}
	if !got.NotBefore.Equal(want.NotBefore) || !got.NotAfter.Equal(want.NotAfter) {
		t.Errorf("validez guardada = [%v, %v], quería [%v, %v]", got.NotBefore, got.NotAfter, want.NotBefore, want.NotAfter)
	}
	if !bytes.Equal(got.CertPEM, want.CertPEM) {
		t.Errorf("cert_pem guardado = %q, quería %q", got.CertPEM, want.CertPEM)
	}
}

func caseCertEmpty(t *testing.T, m MontajeEdgeCertRepository) {
	if records := m.Records(t); len(records) != 0 {
		t.Errorf("un montaje limpio trae %d registros, quería 0", len(records))
	}
}

func caseCertStoresFields(t *testing.T, m MontajeEdgeCertRepository) {
	rec := certRecord(seedCertTenant(t, m), 1)
	createCert(t, m, rec)
	records := m.Records(t)
	if len(records) != 1 {
		t.Fatalf("tras un Create hay %d registros, quería 1", len(records))
	}
	requireStored(t, records, rec)
}

// caseCertKeepsEach: varios certificados del mismo tenant (un Edge re-enrolado, o dos Edge) y de
// otro tenant; todos quedan, cada uno con lo suyo.
func caseCertKeepsEach(t *testing.T, m MontajeEdgeCertRepository) {
	tenantA, tenantB := seedCertTenant(t, m), seedCertTenant(t, m)
	recs := []enroll.EdgeCertRecord{certRecord(tenantA, 1), certRecord(tenantA, 2), certRecord(tenantB, 3)}
	// El mismo CommonName en dos tenants no choca: la identidad del registro no es el CN.
	recs[2].SubjectCN = recs[0].SubjectCN
	for _, rec := range recs {
		createCert(t, m, rec)
	}
	records := m.Records(t)
	if len(records) != len(recs) {
		t.Fatalf("tras %d Create hay %d registros", len(recs), len(records))
	}
	for _, rec := range recs {
		requireStored(t, records, rec)
	}
}

func caseCertConcurrentCreate(t *testing.T, m MontajeEdgeCertRepository) {
	tenant := seedCertTenant(t, m)
	const creates = 16
	start := make(chan struct{})
	errs := make(chan error, creates)
	var wg sync.WaitGroup
	for i := range creates {
		wg.Go(func() {
			<-start
			errs <- m.Repository.Create(context.Background(), certRecord(tenant, i))
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Create concurrente: error inesperado %v", err)
		}
	}
	records := m.Records(t)
	if len(records) != creates {
		t.Fatalf("tras %d Create a la vez hay %d registros", creates, len(records))
	}
	for i := range creates {
		requireStored(t, records, certRecord(tenant, i))
	}
}
