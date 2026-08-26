// despertar_integration_test.go — EL CLAIM POR EVENTO contra Postgres REAL
// (Plan 044 · Ola 2 · T2.7, criterio (f) · D-044.43).
//
// # POR QUÉ ESTO NO PUEDE PROBARSE CON EL DOBLE EN MEMORIA
//
// Porque lo que hay que demostrar es exactamente lo que NO es Go: que
// `claimIgnorandoBackoffSQL` se salta el `next_attempt_at <= now()` y a la vez filtra
// por `tenant_id`. Las dos mitades viven en una cadena de texto que ningún compilador
// mira: `go vet` no la lee, `go build` no la ejecuta, y el doble en memoria la
// reescribe a mano en Go —así que probarla allí sería probar la reescritura—. Un
// `tenant_id` mal tecleado aquí pasaría TODOS los gates y barrería la cola de todos
// los tenants la primera vez que un Edge dijera READY en campo.
//
// Se corre como el resto de la casa: por el NOMBRE del fichero, con `WAPP_TEST_DB_DSN`
// (sin ella se salta solo). Los helpers `openTestDB`, `claveDeVentana`,
// `sembrarPendiente`, `estadoDeLaFila` y `exigeColaSinAjenos` viven en los otros dos
// ficheros de integración del mismo paquete de test.
package intake_test

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
)

// ---------------------------------------------------------------------------
// (1) LA MITAD TEMPORAL SE IGNORA — QUE ES TODO EL PROPÓSITO
// ---------------------------------------------------------------------------

// TestIntegration_Despertar_SeLlevaElPendienteAunqueSuBackoffNoHayaVencido es el
// criterio (f) visto desde el SQL, y es el gemelo EXACTAMENTE INVERSO de
// TestIntegration_Backoff_PendingConMarcaFuturaNoEsReclamable: mismo montaje, misma
// marca una hora en el futuro, y la respuesta contraria.
//
// Que los dos convivan es lo que fija el reparto de papeles de D-044.43: el backoff
// sigue reteniendo al claim normal (es el barrendero del Edge vivo-pero-atascado) y el
// evento se lo salta (el motivo del castigo acaba de desaparecer).
//
// SALIDAS ESPERADAS: ClaimNext ⇒ (_, false, nil); ClaimNextIgnorandoBackoff ⇒ (el job,
// true, nil) y la fila en 'processing'.
//
// 🔬 MUTACIÓN EJECUTADA (roja): añadir `AND next_attempt_at <= now()` al WHERE de
// claimIgnorandoBackoffSQL ⇒ el evento no reanuda nada y el criterio (f) desaparece.
func TestIntegration_Despertar_SeLlevaElPendienteAunqueSuBackoffNoHayaVencido(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	k := claveDeVentana(t, db)
	jobs := intake.NewPostgres(db)

	ahora := time.Now().UTC()
	id := sembrarPendiente(ctx, t, db, k, ahora.Add(-10*time.Minute), ahora.Add(time.Hour))
	exigeColaSinAjenos(ctx, t, db, k.TenantID)

	if j, ok, err := jobs.ClaimNext(ctx); err != nil || ok {
		t.Fatalf("ClaimNext se llevó %q (ok=%v, err=%v); con la marca en el futuro NO debe: si el claim "+
			"normal dejara de respetar el backoff, el evento no aportaría nada y la tormenta volvería", j.ID, ok, err)
	}

	j, ok, err := jobs.ClaimNextIgnorandoBackoff(ctx, k.TenantID)
	if err != nil {
		t.Fatalf("ClaimNextIgnorandoBackoff: %v", err)
	}
	if !ok || j.ID != id {
		t.Fatalf("el claim por evento se llevó (%q, %v); ESPERADO el job %s. El flanco a READY es la "+
			"noticia que invalida la espera: hacerle cumplir la hora que le queda sería sincronizar por reloj",
			j.ID, ok, id)
	}
	if st := estadoDeLaFila(ctx, t, db, id); st != "processing" {
		t.Fatalf("la fila quedó en %q; ESPERADO 'processing'", st)
	}
}

// ---------------------------------------------------------------------------
// (2) Y SOLO LOS DE ESE TENANT
// ---------------------------------------------------------------------------

// TestIntegration_Despertar_NoTocaLaColaDeOtroTenant es la guarda que impide que un
// evento LOCAL tenga un efecto GLOBAL.
//
// El flanco lo produce UN Edge de UN tenant. Sin el `tenant_id = $1`, ese flanco
// vaciaría la cola de todos los inquilinos del proceso ignorando el backoff de todos:
// una mejora convertida en tormenta, y encima disparada por un tercero.
//
// 🔴 EL MONTAJE ESTÁ CRUZADO A PROPÓSITO: el job del OTRO tenant es el más antiguo y su
// marca ya venció, así que gana por los DOS criterios del `ORDER BY`. Si el filtro no
// estuviera, es exactamente el que se llevaría el claim — o sea, el test no puede salir
// verde por casualidad.
//
// SALIDA ESPERADA: el claim por evento del tenant A se lleva el job de A, y el de B
// sigue 'pending'.
//
// 🔬 MUTACIÓN EJECUTADA (roja): quitar `AND tenant_id = $1` del WHERE ⇒ se lleva el job
// del otro tenant.
func TestIntegration_Despertar_NoTocaLaColaDeOtroTenant(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	mio := claveDeVentana(t, db)
	ajeno := claveDeVentana(t, db)
	jobs := intake.NewPostgres(db)

	ahora := time.Now().UTC()
	elAjeno := sembrarPendiente(ctx, t, db, ajeno, ahora.Add(-2*time.Hour), ahora.Add(-time.Hour))
	elMio := sembrarPendiente(ctx, t, db, mio, ahora.Add(-10*time.Minute), ahora.Add(time.Hour))

	j, ok, err := jobs.ClaimNextIgnorandoBackoff(ctx, mio.TenantID)
	if err != nil {
		t.Fatalf("ClaimNextIgnorandoBackoff: %v", err)
	}
	if !ok || j.ID != elMio {
		t.Fatalf("el claim por evento se llevó (%q, %v); ESPERADO %s, el del tenant que despertó", j.ID, ok, elMio)
	}
	if st := estadoDeLaFila(ctx, t, db, elAjeno); st != "pending" {
		t.Fatalf("el job del OTRO tenant quedó en %q; ESPERADO 'pending'. El flanco de un Edge no puede "+
			"barrer la cola de un inquilino que no ha despertado", st)
	}
}

// ---------------------------------------------------------------------------
// (3) UN TENANT VACÍO NO ES UN ERROR, Y UNO SIN NOMBRE NO RECLAMA
// ---------------------------------------------------------------------------

// TestIntegration_Despertar_SinNadaQueReanudarNoEsUnFallo cubre los dos desenlaces
// mudos, que son los frecuentes: casi todos los flancos a READY encuentran la cola de
// su tenant vacía —un Edge que arranca por la mañana— y eso tiene que ser `(_, false,
// nil)`, no un error que llene el log de ruido y esconda los de verdad.
//
// La segunda mitad es la guarda del `tenantID` vacío: un flanco sin identidad no puede
// convertirse en el barrido global que el `$1` existe para impedir, y por eso se corta
// en Go ANTES de ir a la base.
func TestIntegration_Despertar_SinNadaQueReanudarNoEsUnFallo(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	k := claveDeVentana(t, db)
	jobs := intake.NewPostgres(db)

	ahora := time.Now().UTC()
	sembrarPendiente(ctx, t, db, k, ahora.Add(-time.Minute), ahora.Add(-time.Minute))

	if _, ok, err := jobs.ClaimNextIgnorandoBackoff(ctx, "00000000-0000-0000-0000-000000000000"); ok || err != nil {
		t.Fatalf("un tenant sin cola dio (ok=%v, err=%v); ESPERADO (false, nil)", ok, err)
	}
	if _, ok, err := jobs.ClaimNextIgnorandoBackoff(ctx, ""); ok || err != nil {
		t.Fatalf("un flanco SIN tenant dio (ok=%v, err=%v); ESPERADO (false, nil) y sin tocar la base", ok, err)
	}
}
