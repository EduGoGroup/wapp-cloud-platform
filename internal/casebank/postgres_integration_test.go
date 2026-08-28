// postgres_integration_test.go verifica contra Postgres real lo que NINGÚN doble
// en memoria puede demostrar: que el CONSENTIMIENTO LO VIGILA TAMBIÉN LA BASE.
//
// 🔴 EL REPARTO CON casebank_test.go, que es la razón de que sean dos ficheros:
// el guard de Go y el CHECK de la 0082 defienden lo mismo, y una defensa
// duplicada solo vale si cada mitad tiene un test que la otra no puede salvar.
// Aquí se hace el INSERT CRUDO, saltándose el servicio, así que el guard de Go no
// puede rescatar a nadie; allí el store es un doble que cuenta llamadas, así que
// el CHECK tampoco.
//
//	💥 borrar el guard de Go          ⇒ rojo ALLÍ, verde aquí.
//	💥 borrar el ADD CONSTRAINT de la 0082 ⇒ rojo AQUÍ, verde allí.
//
// Mismo contrato que el resto de los *_integration_test.go del repo (sin
// WAPP_TEST_DB_DSN se salta).
package casebank_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/casebank"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

const dsnEnv = "WAPP_TEST_DB_DSN"

// tenantIntegracion y el prefijo de limpieza. El DELETE va SIEMPRE con `WHERE
// tenant_id LIKE 't-casebank-integracion%'` y nunca sin WHERE: si alguien corre
// esto contra una base con casos de verdad, un DELETE global borraría material
// que no se puede volver a recoger sin pedirle otra vez el consentimiento al
// tenant (mismo criterio que `degradation.limpiar`).
const (
	tenantIntegracion = "t-casebank-integracion"
	prefijoLimpieza   = "t-casebank-integracion%"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en %s (%v) pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv, err)
		}
		t.Skipf("BD no disponible en %s (%v): se omiten los tests de integración", dsnEnv, err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migrando BD de test: %v", err)
	}
	return db
}

// nuevoStorePG arma el store real y deja la tabla limpia PARA ESTOS TENANTS.
func nuevoStorePG(t *testing.T) (*casebank.Postgres, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	limpiar(t, db)
	t.Cleanup(func() { limpiar(t, db) })
	return casebank.NewPostgres(db), db
}

func limpiar(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`DELETE FROM public.intake_case_bank WHERE tenant_id LIKE $1`, prefijoLimpieza)
	if err != nil {
		t.Fatalf("limpiando public.intake_case_bank: %v", err)
	}
}

// ---------------------------------------------------------------------------
// EL CHECK
// ---------------------------------------------------------------------------

// TestElCheckDelConsentimientoEsLaRedDeAbajo comprueba que el consentimiento está
// TAMBIÉN en la base, no solo en Go. El servicio rechaza el caso sin
// consentimiento antes de llegar aquí (test de unidad); esto verifica que si
// alguien se salta el servicio —un INSERT a mano, un script de carga, un futuro
// store distinto— la base sigue diciendo que no.
//
// 💥 Mutación: mover el CHECK de la 0082 DENTRO del `CREATE TABLE` ⇒ sobre una
// base ya migrada el CHECK desaparece (el CREATE no vuelve a correr) y este test
// se pone ROJO. Es exactamente el NO-OP del segundo arranque que la 0071 pagó con
// tiempo real.
func TestElCheckDelConsentimientoEsLaRedDeAbajo(t *testing.T) {
	_, db := nuevoStorePG(t)
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `
		INSERT INTO public.intake_case_bank (tenant_id, consented, source_text)
		VALUES ($1, false, 'texto sin consentimiento')`, tenantIntegracion)
	if err == nil {
		t.Fatal("la base aceptó una fila SIN consentimiento: el CHECK no está vigilando")
	}
	if !strings.Contains(err.Error(), "intake_case_bank_consented_check") {
		t.Errorf("la base rechazó por %v, se esperaba intake_case_bank_consented_check", err)
	}
}

// TestElDefaultDeConsentedRECHAZAALDescuidado es la mitad que hace que el DEFAULT
// signifique algo: una fila que OMITE la columna nace `false` y la base la
// rechaza.
//
// 💥 Mutación: cambiar el DEFAULT de la 0082 a `true` ⇒ el INSERT descuidado pasa
// y este test se pone rojo. Es la mutación que el test de arriba NO caza, porque
// aquel pasa `false` explícito.
func TestElDefaultDeConsentedRECHAZAALDescuidado(t *testing.T) {
	_, db := nuevoStorePG(t)
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `
		INSERT INTO public.intake_case_bank (tenant_id, source_text)
		VALUES ($1, 'la columna consented ni se menciona')`, tenantIntegracion)
	if err == nil {
		t.Fatal("la base aceptó una fila que ni menciona `consented`: el DEFAULT no es false")
	}
	if !strings.Contains(err.Error(), "intake_case_bank_consented_check") {
		t.Errorf("la base rechazó por %v, se esperaba intake_case_bank_consented_check", err)
	}
}

// TestElCheckAceptaLaFilaConsentida es el hermano positivo, sin el cual los dos de
// arriba los pasaría también una tabla que no acepta NADA.
func TestElCheckAceptaLaFilaConsentida(t *testing.T) {
	store, db := nuevoStorePG(t)
	ctx := context.Background()

	id, err := store.Insertar(ctx, casebank.Caso{
		TenantID:   tenantIntegracion,
		Consented:  true,
		SourceText: "quiero una torta de 10 porciones",
	})
	if err != nil {
		t.Fatalf("la base RECHAZÓ una fila CONSENTIDA: %v", err)
	}
	if id == 0 {
		t.Error("el INSERT no devolvió id: el RETURNING no está llegando")
	}

	var consented bool
	var expected sql.NullString
	err = db.QueryRowContext(ctx,
		`SELECT consented, expected FROM public.intake_case_bank WHERE id = $1`, id).
		Scan(&consented, &expected)
	if err != nil {
		t.Fatalf("releyendo la fila: %v", err)
	}
	if !consented {
		t.Error("la fila quedó con consented=false")
	}
	// 🔴 `expected` vacío tiene que quedar NULL de SQL y no el literal JSON
	// `null`: son valores distintos y dicen cosas distintas («aún no se curó»
	// contra «la interpretación correcta es nula»).
	if expected.Valid {
		t.Errorf("expected = %q; un caso sin curar tiene que quedar NULL de SQL", expected.String)
	}
}

// ---------------------------------------------------------------------------
// EL STORE, CONTRA LA BASE DE VERDAD
// ---------------------------------------------------------------------------

// TestExiste_DistingueTenantYTexto: el guard de idempotencia de la siembra no
// puede confundir dos tenants ni dos textos.
func TestExiste_DistingueTenantYTexto(t *testing.T) {
	store, _ := nuevoStorePG(t)
	ctx := context.Background()
	const texto = "quiero dos tortas y un paquete de 30"

	if _, err := store.Insertar(ctx, casebank.Caso{
		TenantID: tenantIntegracion, Consented: true, SourceText: texto,
	}); err != nil {
		t.Fatalf("Insertar: %v", err)
	}

	casos := []struct {
		nombre, tenant, texto string
		quiero                bool
	}{
		{"mismo tenant y mismo texto", tenantIntegracion, texto, true},
		{"mismo tenant, otro texto", tenantIntegracion, texto + " y flan", false},
		{"otro tenant, mismo texto", tenantIntegracion + "-bis", texto, false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			ya, err := store.Existe(ctx, c.tenant, c.texto)
			if err != nil {
				t.Fatalf("Existe: %v", err)
			}
			if ya != c.quiero {
				t.Errorf("Existe(%q, …) = %t; se esperaba %t", c.tenant, ya, c.quiero)
			}
		})
	}
}

// TestSembrarElCasoAmbar_EsIdempotenteContraPostgres corre la siembra REAL dos
// veces y comprueba las dos cosas que solo la base puede responder: que la
// segunda no escribe, y que lo que quedó en `source_text` no lleva PII.
//
// 🔴 La segunda comprobación es un ECO del anonimizador en SQL, no su juez: si el
// anonimizador tiene un agujero, esta consulta lo tiene igual. Lo que caza de
// verdad es la fila que entró SIN pasar por él.
func TestSembrarElCasoAmbar_EsIdempotenteContraPostgres(t *testing.T) {
	store, db := nuevoStorePG(t)
	ctx := context.Background()

	svc, err := casebank.NewServicio(store, casebank.NuevoAnonimizador(casebank.NombresDelCaso()...))
	if err != nil {
		t.Fatalf("NewServicio: %v", err)
	}

	id, sembro, err := svc.Sembrar(ctx, casebank.CasoAmbar(tenantIntegracion))
	if err != nil || !sembro || id == 0 {
		t.Fatalf("la 1.ª siembra devolvió (%d, %t, %v); se esperaba haber escrito", id, sembro, err)
	}
	id2, sembro2, err := svc.Sembrar(ctx, casebank.CasoAmbar(tenantIntegracion))
	if err != nil {
		t.Fatalf("la 2.ª siembra falló: %v", err)
	}
	if sembro2 || id2 != 0 {
		t.Errorf("la 2.ª siembra devolvió (%d, %t); se esperaba (0, false)", id2, sembro2)
	}

	var filas int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.intake_case_bank WHERE tenant_id = $1`, tenantIntegracion).
		Scan(&filas); err != nil {
		t.Fatalf("contando: %v", err)
	}
	if filas != 1 {
		t.Errorf("hay %d filas del caso; se esperaba 1", filas)
	}

	// El barrido de PII en SQL, con sus DOS mitades: sin la segunda, una tabla
	// vacía daría cero en la primera y no probaría nada.
	var conPII int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.intake_case_bank
		 WHERE tenant_id = $1
		   AND (source_text ~ '@(s\.whatsapp\.net|g\.us|c\.us|lid)'
		        OR source_text ~ '[0-9][0-9 ()+.-]{6,}[0-9]')`, tenantIntegracion).
		Scan(&conPII); err != nil {
		t.Fatalf("barriendo PII: %v", err)
	}
	if conPII != 0 {
		t.Errorf("%d filas del caso sembrado llevan JID o teléfono en claro", conPII)
	}

	// Y la procedencia, que tiene que haber viajado DENTRO de la fila.
	var calidad sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT expected->'_procedencia'->>'calidad'
		  FROM public.intake_case_bank WHERE id = $1`, id).Scan(&calidad); err != nil {
		t.Fatalf("leyendo la procedencia: %v", err)
	}
	if !calidad.Valid || calidad.String != "C" {
		t.Errorf("expected->_procedencia->calidad = %v; la fila sembrada tiene que declarar que es material REDACTADO", calidad)
	}
}
