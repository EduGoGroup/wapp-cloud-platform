package contact_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
)

// isDeadlock reporta si err (o algún error envuelto) es un deadlock de Postgres
// (SQLSTATE 40P01). El resolver envuelve los errores con %w, así que el
// *pgconn.PgError subyacente se recupera con errors.As.
func isDeadlock(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "40P01"
}

// nombreDeLaRafaga es el push_name que mandan TODOS los workers de la ráfaga, y que
// sean TODOS el mismo es parte del diseño (ver el docstring del test): un contacto
// tiene UN push_name y todos sus entrantes lo repiten. Mandar nombres distintos sería
// el escenario «último gana» que MD-046.5 descartó, y no hace falta para cruzar locks.
const nombreDeLaRafaga = "flood-name"

// TestIntegration_ContactResolve_ConcurrentUpsert_NoDeadlock reproduce el
// deadlock SQLSTATE 40P01 del procesado de entrantes bajo inundación de historial
// (Plan 026 · T4, journal 2026-07-09 "Follow-up del Cloud"):
//
// Un mismo contacto tiene DOS filas en public.contacts (phone_e164 + wa_lid, mismo
// contact_id). Bajo la ráfaga, whatsmeow enriquece la identidad de forma desigual:
// unos entrantes traen solo from_pn, otros solo from_lid, y TODOS con push_name.
// Cada Resolve:
//   - toma FOR UPDATE (lookupContactIDs) SOLO la fila de la ref presente
//     (phone-only → fila phone; lid-only → fila lid), y
//   - luego hace `UPDATE contacts SET push_name... WHERE contact_id = X`, que bloquea
//     TODAS las filas del contacto (phone Y lid) en orden de scan.
//
// Resultado: la transacción phone-only retiene la fila phone y pide la lid; la
// lid-only retiene la lid y pide la phone → ciclo de locks → 40P01. El FOR UPDATE
// parcial y el UPDATE masivo adquieren locks en orden inconsistente: no lo cura
// ordenar refs (las refs son disjuntas), sí lo cura reintentar la transacción
// atómica ante 40P01 (el perdedor hace rollback limpio y reintenta tras el commit
// del ganador; el upsert es idempotente por ON CONFLICT).
//
// SIN el fix, algún Resolve devuelve 40P01 al llamante (test ROJO). CON el fix
// (retry acotado ante 40P01), ningún deadlock aflora (test VERDE). DSN-gated.
//
// ── CÓMO SE REARMÓ TRAS T4.2, Y POR QUÉ EL SEMBRADO SIN NOMBRE ES CONTRATO ────────
// Desde MD-046.5 el guard del UPDATE ya no compara contenido: es un CENTINELA,
// `push_name_enc IS NULL` (repository_postgres.go:323). Un centinela solo casa
// MIENTRAS el sobre esté sin escribir, así que quien decide si este test reproduce
// algo o no es LA SIEMBRA:
//
//   - Sembrando CON nombre, las dos filas nacen con el sobre puesto. A partir de ahí
//     ningún Resolve de la ráfaga casa el centinela, el UPDATE afecta a CERO filas,
//     no se toma ni un row-lock y el ciclo es IMPOSIBLE de reproducir.
//   - Sembrando SIN nombre —el Resolve de la siembra recibe la cadena vacía y
//     pushNameEnvelope deja las tres columnas a NULL (repository_postgres.go:220-223)—
//     la PRIMERA oleada de la ráfaga SÍ casa `push_name_enc IS NULL` en las DOS filas,
//     el UPDATE masivo por contact_id vuelve a cruzarse con el FOR UPDATE parcial del
//     lookup, y el ciclo de locks se reproduce igual que en el Plan 026.
//
// Comparar por contenido no era alternativa: dos cifrados del mismo texto nunca son
// iguales (DEK y nonce frescos), así que un guard por valor casaría SIEMPRE y tomaría
// locks en CADA entrante — justo lo que MD-046.5 fue a quitar.
//
// 💥 MUTACIONES QUE LO PONEN ROJO — Y LA QUE NO, QUE ES LA PELIGROSA:
//
//   - 🔴 AL REVÉS DE LO HABITUAL: volver a sembrar el contacto CON nombre (pasarle un
//     nombre no vacío al Resolve de la siembra) NO PONE ROJO NADA. Deja el test VERDE
//     Y HUECO, y en SILENCIO: el UPDATE deja de casar desde el primer entrante, la
//     ráfaga no toma row-locks nuevos, no hay ciclo que reproducir y la regresión del
//     40P01 se queda sin guardián sin que nadie vea una línea roja. Por eso el
//     sembrado sin nombre es CONTRATO de este test y no un detalle de montaje; la
//     única red contra esa mutación es la precondición de abajo, que exige las dos
//     filas con el sobre a NULL ANTES de la ráfaga.
//   - quitar el reintento acotado ante 40P01 de postgres.WithTx ⇒ algún Resolve
//     devuelve el deadlock al llamante y el conteo de deadlocks deja de ser 0. Es el
//     modo de fallo en producción: el runtime pierde entrantes de la ráfaga de
//     historial, uno por transacción perdedora, sin reintento de nadie más arriba.
//   - devolver el centinela `push_name_enc IS NULL` a una comparación por contenido
//     (repository_postgres.go:323) ⇒ el sobre se reescribe en cada entrante y la
//     aserción de estabilidad falla: con ella vuelven los row-locks masivos por
//     entrante, que es el ciclo que este test vigila.
func TestIntegration_ContactResolve_ConcurrentUpsert_NoDeadlock(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	wipeContacts(t, db)
	tenant := seedTenant(t, db)

	r := newTestResolver(t, db)

	// Un contacto con AMBAS refs bajo el mismo contact_id (el estado tras varios
	// entrantes previos que fusionaron número y LID) y SIN nombre: las dos filas nacen
	// con las tres columnas del sobre a NULL. Ver el docstring: esto es contrato.
	phone := mustRef(t, contact.KindPhoneE164, "573001112233")
	lid := mustRef(t, contact.KindWALID, "998877665544")
	cid, err := r.Resolve(ctx, tenant, []contact.Ref{phone, lid}, "")
	if err != nil {
		t.Fatalf("sembrar contacto con ambas refs: %v", err)
	}
	assertSobresPushNameSinEscribir(t, db, tenant, cid)

	// Ráfaga concurrente: mitad de los workers refrescan por phone-only, mitad por
	// lid-only, TODOS con el mismo push_name. Las refs disjuntas son lo que cruza los
	// locks; el sobre a NULL es lo que hace que el UPDATE masivo llegue a tomarlos.
	sobresPrimeraOleada, deadlocks, otherErrs := rafagaResolveConcurrente(ctx, t, db, r, tenant, cid, phone, lid)

	if deadlocks > 0 {
		t.Fatalf("Resolve devolvió %d deadlock(s) 40P01 al llamante: el procesado de "+
			"entrantes NO es robusto ante la ráfaga de historial (T4). En producción eso es un "+
			"entrante PERDIDO por cada transacción perdedora: no hay reintento más arriba", deadlocks)
	}
	if otherErrs > 0 {
		t.Fatalf("Resolve devolvió %d error(es) inesperados", otherErrs)
	}

	assertSobreSelladoUnaVez(t, db, tenant, cid, sobresPrimeraOleada)
}

// rafagaResolveConcurrente lanza la inundación de historial y devuelve, además de los
// contadores de error, el snapshot de los sobres tomado JUSTO AL FINAL DE LA PRIMERA
// OLEADA (la que sí casa el centinela). Ese punto intermedio es determinista gracias a
// la barrera: las 16 goroutines paran tras su PRIMER Resolve, el test mide, y solo
// entonces se las suelta a las 59 iteraciones restantes. Sin la barrera no habría forma
// de distinguir «se escribió una vez y se quedó quieto» de «no se escribió nunca».
//
// La barrera no debilita la reproducción del ciclo: al contrario, alinea las 16
// primeras transacciones —que son justamente las que compiten por los row-locks—.
//
// Extraída y NOMBRADA por gocyclo (umbral 15, que aplica también a los tests).
func rafagaResolveConcurrente(
	ctx context.Context, t *testing.T, db *sql.DB,
	r *contact.PostgresResolver, tenantID, contactID string, phone, lid contact.Ref,
) (sobresPrimeraOleada map[string][]byte, deadlocks, otherErrs int64) {
	t.Helper()
	const (
		workers = 16
		iters   = 60
	)
	var wg, primeraOleada sync.WaitGroup
	siga := make(chan struct{})
	// El soltado va TAMBIÉN por defer: si la lectura del snapshot de abajo abortara con
	// t.Fatalf, las 16 goroutines se quedarían colgadas en `<-siga` para siempre y el
	// fallo llegaría al informe acompañado de un volcado de goroutines que no dice nada.
	var soltar sync.Once
	liberar := func() { soltar.Do(func() { close(siga) }) }
	defer liberar()
	wg.Add(workers)
	primeraOleada.Add(workers)
	for w := range workers {
		phoneOnly := w%2 == 0
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				refs := []contact.Ref{phone}
				if !phoneOnly {
					refs = []contact.Ref{lid}
				}
				if _, err := r.Resolve(ctx, tenantID, refs, nombreDeLaRafaga); err != nil {
					if isDeadlock(err) {
						atomic.AddInt64(&deadlocks, 1)
					} else {
						atomic.AddInt64(&otherErrs, 1)
						t.Errorf("Resolve error no-deadlock: %v", err)
					}
				}
				if i == 0 {
					primeraOleada.Done()
					<-siga
				}
			}
		}()
	}
	primeraOleada.Wait()
	sobresPrimeraOleada = sobresPushNamePorKind(t, db, tenantID, contactID)
	liberar()
	wg.Wait()
	return sobresPrimeraOleada, atomic.LoadInt64(&deadlocks), atomic.LoadInt64(&otherErrs)
}

// assertSobresPushNameSinEscribir es la PRECONDICIÓN que sostiene todo el test: las dos
// filas del contacto tienen que llegar a la ráfaga con el sobre sin escribir. Si esta
// aserción se cae, la siembra volvió a llevar nombre y el test ya no reproduce el
// ciclo 40P01 — es la única señal que delata esa mutación, porque la ráfaga en sí
// seguiría pasando tan campante.
func assertSobresPushNameSinEscribir(t *testing.T, db *sql.DB, tenantID, contactID string) {
	t.Helper()
	sobres := sobresPushNamePorKind(t, db, tenantID, contactID)
	if len(sobres) != 2 {
		t.Fatalf("precondición: el contacto debe tener 2 filas (phone + lid), tiene %d", len(sobres))
	}
	for kind, blob := range sobres {
		if len(blob) != 0 {
			t.Fatalf("precondición ROTA en la fila %q: push_name_enc ya viene escrito (%dB) ANTES de la "+
				"ráfaga. El contacto se sembró CON nombre, así que el centinela push_name_enc IS NULL no "+
				"casará en ningún entrante, el UPDATE afectará a CERO filas y el ciclo de locks del "+
				"deadlock 40P01 (Plan 026) NO se reproduce: este test pasaría VERDE Y HUECO",
				kind, len(blob))
		}
	}
}

// assertSobreSelladoUnaVez es el criterio (b) de T4.2 MEDIDO, no supuesto.
//
// QUÉ CUBRE: que la PRIMERA oleada escribió el sobre en las DOS filas (o sea, que el
// UPDATE llegó a casar el centinela y a tomar los row-locks cruzados que reproducen el
// ciclo), y que desde ahí hasta el final de las 960 llamadas los bytes NO se movieron
// (o sea, que el centinela dejó de casar y la ráfaga no toma un solo row-lock más).
//
// QUÉ NO CUBRE, dicho sin adornos: no cuenta escrituras, compara bytes. Una reescritura
// que produjera EXACTAMENTE los mismos bytes pasaría inadvertida. Hoy es imposible —el
// nonce es fresco por escritura, así que dos cifrados del mismo texto nunca coinciden—,
// pero la garantía viene del cifrado, no de esta aserción. Contar escrituras de verdad
// pediría un contador en el repositorio que nadie más usaría.
func assertSobreSelladoUnaVez(t *testing.T, db *sql.DB, tenantID, contactID string, primeraOleada map[string][]byte) {
	t.Helper()
	if len(primeraOleada) != 2 {
		t.Fatalf("tras la primera oleada el contacto tiene %d filas, want 2", len(primeraOleada))
	}
	final := sobresPushNamePorKind(t, db, tenantID, contactID)
	for kind, blobPrimeraOleada := range primeraOleada {
		if len(blobPrimeraOleada) == 0 {
			t.Fatalf("fila %q: la PRIMERA oleada no selló el sobre. Con las 16 transacciones mandando un "+
				"nombre no vacío sobre un sobre a NULL, el UPDATE de resolveExisting tenía que casar el "+
				"centinela y escribir las dos filas: si no escribe, tampoco toma los row-locks cruzados y "+
				"el ciclo 40P01 del Plan 026 no se está reproduciendo", kind)
		}
		blobFinal, ok := final[kind]
		if !ok {
			t.Fatalf("la fila %q del contacto desapareció durante la ráfaga", kind)
		}
		if !bytes.Equal(blobPrimeraOleada, blobFinal) {
			t.Fatalf("fila %q: el sobre del push_name se reescribió DESPUÉS de la primera oleada (%dB → %dB). "+
				"El centinela push_name_enc IS NULL tiene que dejar de casar tras el PRIMER nombre no "+
				"vacío: si vuelve a casar, cada entrante toma row-locks sobre todas las filas del "+
				"contacto y reaparece el deadlock 40P01 (MD-046.5, criterio (b) del Plan 046 · T4.2)",
				kind, len(blobPrimeraOleada), len(blobFinal))
		}
	}
}

// sobresPushNamePorKind mapea kind → push_name_enc de todas las filas de un contacto.
// Un sobre sin escribir (NULL) llega aquí como un slice vacío: esta función NO juzga,
// solo lee — quien decide si el vacío es lo correcto es cada aserción, porque este test
// mide el MISMO campo en dos estados opuestos (vacío antes de la ráfaga, poblado
// después).
//
// Va por SQL crudo porque NO HAY —ni va a haber— un lector de push_name en el
// repositorio: nadie consume ese campo, así que un método de descifrado sería código
// sin llamador (el porqué entero está en backfill_push_name_integration_test.go). Aquí
// además ni siquiera hace falta descifrar: lo que se afirma es que los BYTES no
// cambiaron, y eso se ve sin abrir el sobre.
func sobresPushNamePorKind(t *testing.T, db *sql.DB, tenantID, contactID string) map[string][]byte {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT kind, push_name_enc FROM public.contacts
		WHERE tenant_id = $1 AND contact_id = $2
	`, tenantID, contactID)
	if err != nil {
		t.Fatalf("leer los sobres del contacto %s: %v", contactID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Fatalf("cerrar los sobres del contacto %s: %v", contactID, cerr)
		}
	}()
	out := make(map[string][]byte)
	for rows.Next() {
		var (
			kind string
			enc  []byte
		)
		if serr := rows.Scan(&kind, &enc); serr != nil {
			t.Fatalf("escanear sobre: %v", serr)
		}
		out[kind] = append([]byte(nil), enc...)
	}
	if rerr := rows.Err(); rerr != nil {
		t.Fatalf("iterar los sobres del contacto %s: %v", contactID, rerr)
	}
	return out
}
