package storehelpertest

import (
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Las tres carreras del puerto. Los tests viejos no tenían ninguna (cero goroutines en los 18): el
// FOR UPDATE del versionado y del cierre y el compare-and-set de la bienvenida solo se probaban en
// secuencia, y así un mutante que quita el bloqueo sobrevive. Aquí N llamadas salen a la vez y se
// afirma el resultado que SOLO da una implementación que las serializa; no se duerme ni se mira
// el reloj. En memoria las serializa el mutex; contra Postgres, la base.

// racers es el número de llamadas simultáneas de cada carrera.
const racers = 8

// race lanza racers goroutines que esperan la misma señal y llaman a fn con su índice, y devuelve
// cuando todas han terminado. fn no puede llamar a t.Fatal: corre fuera de la goroutine del test.
func race(fn func(i int)) {
	var (
		start = make(chan struct{})
		done  sync.WaitGroup
	)
	for i := range racers {
		done.Add(1)
		go func() {
			defer done.Done()
			<-start
			fn(i)
		}()
	}
	close(start)
	done.Wait()
}

// caseReplaceConcurrent: N imports simultáneos sobre la MISMA (tenant, ref) con blob vigente se
// serializan: ninguno falla, cada uno archiva un número distinto y entre todos numeran 1..N sin
// huecos. Y no se pierde ningún blob: los N+1 que han existido (el sembrado y los N escritos) son
// exactamente los N archivados más el vigente.
func caseReplaceConcurrent(t *testing.T, m Montaje) {
	mustUpsertContent(t, m, m.TenantA, refCatalog, blob("sembrado"))
	seedContentWitnesses(t, m, refCatalog, refMenu)
	before := take(t, m)

	var (
		archived [racers]int
		errs     [racers]error
	)
	race(func(i int) {
		archived[i], errs[i] = m.Store.ReplaceTenantContentVersioned(
			ctx, m.TenantA, refCatalog, blob(fmt.Sprintf("carrera-%d", i)), store.VersionSourceImportJSON)
	})
	for i, err := range errs {
		if err != nil {
			t.Errorf("el import %d falló: %v", i, err)
		}
	}
	numbers := append([]int(nil), archived[:]...)
	sort.Ints(numbers)
	for i, n := range numbers {
		if n != i+1 {
			t.Fatalf("versiones archivadas = %v, quería 1..%d sin huecos ni repetidos", numbers, racers)
		}
	}

	versions := m.ContentVersions(t, m.TenantA, refCatalog)
	if len(versions) != racers {
		t.Fatalf("%d versiones archivadas, quería %d: %+v", len(versions), racers, versions)
	}
	current, err := m.Store.GetTenantContent(ctx, m.TenantA, refCatalog)
	if err != nil {
		t.Fatalf("GetTenantContent tras la carrera: %v", err)
	}
	written := make([][]byte, 0, racers+1)
	written = append(written, blob("sembrado"))
	for i := range racers {
		written = append(written, blob(fmt.Sprintf("carrera-%d", i)))
	}
	survivors := make([][]byte, 0, racers+1)
	survivors = append(survivors, current)
	for i, v := range versions {
		if v.Version != i+1 || v.Source != store.VersionSourceImportJSON {
			t.Errorf("versión archivada %d = (nº %d, %s), quería (nº %d, %s)", i, v.Version, v.Source, i+1, store.VersionSourceImportJSON)
		}
		survivors = append(survivors, v.Content)
	}
	requireSameBlobs(t, "los archivados más el vigente", survivors, written)
	requireRestUntouched(t, "carrera de imports", before, take(t, m), forgetContent(m.TenantA, refCatalog))
}

// requireSameBlobs afirma que got son exactamente los blobs de want, cada uno UNA vez y en
// cualquier orden: ni se perdió ninguno ni se archivó dos veces el mismo.
func requireSameBlobs(t *testing.T, what string, got, want [][]byte) {
	t.Helper()
	pending := append([][]byte(nil), want...)
	for _, raw := range got {
		at := slices.IndexFunc(pending, func(w []byte) bool { return sameJSON(raw, w) })
		if at < 0 {
			t.Errorf("%s: el blob %s está repetido o no es de esta carrera", what, raw)
			continue
		}
		pending = slices.Delete(pending, at, at+1)
	}
	for _, lost := range pending {
		t.Errorf("%s: se perdió el blob %s: ni archivado ni vigente", what, lost)
	}
}

// caseCloseConcurrent: N cierres simultáneos del MISMO contacto, que tiene una solicitud abierta,
// se serializan: ninguno falla y cada uno cierra una solicitud DISTINTA. Solo uno se lleva la
// abierta; los demás, que ya no la ven "open", crean la suya. Al final el contacto tiene N
// solicitudes, todas cerradas, cada una con las líneas de su cierre y solo esas.
func caseCloseConcurrent(t *testing.T, m Montaje) {
	contact := uuid.NewString()
	open := seedIntake(t, m, m.TenantA, contact, statusOpen)
	mustReplaceItems(t, m, open.ID, line("MATERIALIZADA", "Ya proyectada", 1, 1))
	seedIntakeWitnesses(t, m, contact)
	before := take(t, m)

	// Los eventos se crean antes de la carrera: NewEvent puede fallar el test.
	var (
		events [racers]string
		closed [racers]string
		errs   [racers]error
	)
	for i := range events {
		events[i] = m.NewEvent(t, m.TenantA)
	}
	sku := func(i int) string { return fmt.Sprintf("CIERRE-%d", i) }
	race(func(i int) {
		closed[i], errs[i] = m.Store.CloseIntake(ctx, store.IntakeClose{
			TenantID: m.TenantA, ContactID: contact, SessionID: sessionTwo, Total: float64(i + 1),
			EventID: events[i], Items: []IntakeItem{line(sku(i), "Cierre", i+1, 1)},
		})
	})

	winners := 0
	owner := make(map[string]int, racers)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("el cierre %d falló: %v", i, err)
		}
		if other, dup := owner[closed[i]]; dup {
			t.Fatalf("los cierres %d y %d cerraron la misma solicitud (%s)", other, i, closed[i])
		}
		owner[closed[i]] = i
		if closed[i] == open.ID {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("%d cierres se llevaron la solicitud abierta, quería exactamente 1", winners)
	}

	after := take(t, m)
	forget := make([]func(*world), 0, racers)
	for id, i := range owner {
		mark := after.intakeOf(t, m.TenantA, id)
		if mark.header.Status != statusClosed || mark.header.Total != float64(i+1) || mark.header.ContactID != contact {
			t.Errorf("la solicitud del cierre %d quedó (%s, total %v, contacto %s), quería (closed, %d, %s)",
				i, mark.header.Status, mark.header.Total, mark.header.ContactID, i+1, contact)
		}
		requireLines(t, m, fmt.Sprintf("cierre %d", i), id, line(sku(i), "Cierre", i+1, 1))
		forget = append(forget, forgetIntake(m.TenantA, id))
	}
	if got := len(after.tenants[m.TenantA].intakes) - len(before.tenants[m.TenantA].intakes); got != racers-1 {
		t.Errorf("la carrera creó %d solicitudes, quería %d (todos los cierres menos el que se llevó la abierta)", got, racers-1)
	}
	requireRestUntouched(t, "carrera de cierres", before, after, forget...)
}

// caseMarkConcurrent: N turnos que leyeron el MISMO testigo intentan sellar la bienvenida a la vez:
// gana exactamente uno, los demás devuelven false sin error, y la fila queda con el instante del
// que ganó.
func caseMarkConcurrent(t *testing.T, m Montaje) {
	fx := newFixture(m)
	touchAll(t, fx)
	requireMark(t, m, "primera bienvenida", fx.key, WelcomeMark{}, at(12, 9), true)
	witness := mustTouch(t, m, fx.key, at(20, 9))
	before := fx.take(t)

	var (
		won  [racers]bool
		errs [racers]error
	)
	stamp := func(i int) time.Time { return at(21, i) }
	race(func(i int) {
		won[i], errs[i] = m.Store.MarkWelcomed(ctx, fx.key, witness, stamp(i))
	})

	winner := -1
	for i, err := range errs {
		if err != nil {
			t.Errorf("el sello %d falló: %v", i, err)
		}
		if !won[i] {
			continue
		}
		if winner >= 0 {
			t.Fatalf("los sellos %d y %d ganaron los dos, quería exactamente uno", winner, i)
		}
		winner = i
	}
	if winner < 0 {
		t.Fatal("ningún sello ganó, quería exactamente uno")
	}
	requireRow(t, m, "fila tras la carrera", fx.key, at(20, 9), stamp(winner))
	requireRestUntouched(t, "carrera de sellos", before, fx.take(t), forgetWelcome(fx.key))
}
