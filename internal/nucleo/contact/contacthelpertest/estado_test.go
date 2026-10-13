package contacthelpertest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
)

// Los identificadores de estos tests. EstadoMemoria no valida que sean UUID: solo los compara.
const (
	tenantUno  = "tenant-a"
	tenantDos  = "tenant-b"
	idHuerfano = "contacto-huerfano"
	idCanonico = "contacto-canonico"
	idAjeno    = "contacto-ajeno"
)

// Las marcas de las filas que siembran estos tests: una por fila, para ver cuál sobrevive.
const (
	markOrphan    = "mark-orphan"
	markCanonical = "mark-canonical"
	markOther     = "mark-other"
	markSecond    = "mark-second"
)

// exigirDuenoEs falla el test si la sesión no tiene estado, no es del contacto quiere o su fila
// no lleva la marca wantMark.
func exigirDuenoEs(t *testing.T, e *EstadoMemoria, tenantID, sessionID, quiere, wantMark string) {
	t.Helper()
	got, gotMark, ok := e.Dueno(t, tenantID, sessionID)
	if !ok || got != quiere || gotMark != wantMark {
		t.Errorf("Dueno(%s, %s) = (%q, %q, %v); quiere (%q, %q, true)", tenantID, sessionID, got, gotMark, ok, quiere, wantMark)
	}
}

// exigirSinEstado falla el test si la sesión tiene estado o si, sin tenerlo, Dueno devuelve un
// contacto o una marca no vacíos.
func exigirSinEstado(t *testing.T, e *EstadoMemoria, tenantID, sessionID string) {
	t.Helper()
	if got, gotMark, ok := e.Dueno(t, tenantID, sessionID); ok || got != "" || gotMark != "" {
		t.Errorf("Dueno(%s, %s) = (%q, %q, %v); quiere (\"\", \"\", false): la sesión no debía tener estado", tenantID, sessionID, got, gotMark, ok)
	}
}

// migrar migra el estado de from a to en el tenant y falla el test si hay error.
func migrar(t *testing.T, e *EstadoMemoria, tenantID, from, to string) {
	t.Helper()
	if err := e.MigrateContactID(t.Context(), tenantID, from, to); err != nil {
		t.Fatalf("MigrateContactID(%s, %s, %s): %v", tenantID, from, to, err)
	}
}

// Un estado nuevo está vacío; lo sembrado se observa por (tenant, sesión), con su marca tal cual,
// y resiste a repetirse: sembrar otra vez el mismo (tenant, sesión, contacto), aunque sea con otra
// marca, no cambia nada y se queda la primera.
func TestEstadoMemoria_SembrarYObservar(t *testing.T) {
	e := NuevoEstado()
	exigirSinEstado(t, e, tenantUno, "s1")

	e.Sembrar(t, tenantUno, "s1", idHuerfano, markOrphan)
	e.Sembrar(t, tenantUno, "s2", idCanonico, markCanonical)
	e.Sembrar(t, tenantUno, "s1", idHuerfano, markOrphan) // repetir lo ya sembrado no cambia nada
	e.Sembrar(t, tenantUno, "s1", idHuerfano, markSecond) // ni con otra marca: gana la primera

	exigirDuenoEs(t, e, tenantUno, "s1", idHuerfano, markOrphan)
	exigirDuenoEs(t, e, tenantUno, "s2", idCanonico, markCanonical)
	exigirSinEstado(t, e, tenantUno, "s3")
	if got := e.duenosDe(tenantUno, "s1"); !slices.Equal(got, []string{idHuerfano}) {
		t.Errorf("duenosDe tras sembrar dos veces lo mismo = %q; quiere [%s]", got, idHuerfano)
	}
}

// Dos contactos sembrados en la misma sesión son dos dueños (el conflicto de la fusión), y se
// listan ordenados para que el mensaje de error sea estable.
func TestEstadoMemoria_DosDuenosEnLaMismaSesion(t *testing.T) {
	e := NuevoEstado()
	e.Sembrar(t, tenantUno, "s1", idHuerfano, markOrphan)
	e.Sembrar(t, tenantUno, "s1", idCanonico, markCanonical)

	got := e.duenosDe(tenantUno, "s1")

	if want := []string{idCanonico, idHuerfano}; !slices.Equal(got, want) {
		t.Errorf("duenosDe = %q; quiere %q (ordenados)", got, want)
	}
	if got := e.duenosDe(tenantUno, "otra-sesion"); len(got) != 0 {
		t.Errorf("duenosDe de una sesión sin estado = %q; quiere vacío", got)
	}
	// Cada dueño conserva la marca de su fila.
	if got, want := e.rowsOf(tenantUno, "s1"), map[string]string{idHuerfano: markOrphan, idCanonico: markCanonical}; !maps.Equal(got, want) {
		t.Errorf("rowsOf = %q; quiere %q", got, want)
	}
}

// Migrar sin conflicto re-clava todas las sesiones del huérfano en el canónico, cada una con la
// marca que tenía (la fila se mueve, no se rehace), y deja en paz el estado que el canónico ya
// tenía, con la suya.
func TestEstadoMemoria_MigrarSinConflicto(t *testing.T) {
	e := NuevoEstado()
	e.Sembrar(t, tenantUno, "s1", idHuerfano, markOrphan)
	e.Sembrar(t, tenantUno, "s2", idHuerfano, markSecond)
	e.Sembrar(t, tenantUno, "s-propia", idCanonico, markCanonical)

	migrar(t, e, tenantUno, idHuerfano, idCanonico)

	exigirDuenoEs(t, e, tenantUno, "s1", idCanonico, markOrphan)
	exigirDuenoEs(t, e, tenantUno, "s2", idCanonico, markSecond)
	exigirDuenoEs(t, e, tenantUno, "s-propia", idCanonico, markCanonical)
}

// Migrar con conflicto (R-17): en la sesión donde los dos tenían estado se conserva el del
// canónico, con SU marca, y se descarta el del huérfano; en las demás, el estado del huérfano
// migra con la suya. Dueno falla el test si quedaran dos dueños, así que basta con leerlo. El
// canónico se siembra aquí después del huérfano, para que ganar no dependa del orden de siembra.
func TestEstadoMemoria_MigrarConConflictoConservaElCanonico(t *testing.T) {
	e := NuevoEstado()
	e.Sembrar(t, tenantUno, "s-conflicto", idHuerfano, markOrphan)
	e.Sembrar(t, tenantUno, "s-conflicto", idCanonico, markCanonical)
	e.Sembrar(t, tenantUno, "s-solo-huerfano", idHuerfano, markSecond)

	migrar(t, e, tenantUno, idHuerfano, idCanonico)

	exigirDuenoEs(t, e, tenantUno, "s-conflicto", idCanonico, markCanonical)
	exigirDuenoEs(t, e, tenantUno, "s-solo-huerfano", idCanonico, markSecond)
	if got := e.duenosDe(tenantUno, "s-conflicto"); !slices.Equal(got, []string{idCanonico}) {
		t.Errorf("duenosDe de la sesión en conflicto = %q; quiere solo [%s]: el estado del huérfano se descarta", got, idCanonico)
	}
}

// Un tenant no ve el estado de otro ni lo toca al migrar: la misma sesión y los mismos
// contact_id en dos tenants son dos estados distintos.
func TestEstadoMemoria_AislamientoPorTenant(t *testing.T) {
	e := NuevoEstado()
	e.Sembrar(t, tenantUno, "s1", idHuerfano, markOrphan)
	e.Sembrar(t, tenantDos, "s1", idHuerfano, markSecond)
	e.Sembrar(t, tenantDos, "solo-en-b", idAjeno, markOther)

	exigirSinEstado(t, e, tenantUno, "solo-en-b")

	migrar(t, e, tenantUno, idHuerfano, idCanonico)

	exigirDuenoEs(t, e, tenantUno, "s1", idCanonico, markOrphan)
	exigirDuenoEs(t, e, tenantDos, "s1", idHuerfano, markSecond) // el de B sigue siendo del huérfano
	exigirDuenoEs(t, e, tenantDos, "solo-en-b", idAjeno, markOther)
}

// Migrar dos veces lo mismo deja el estado como lo dejó la primera, sin error.
func TestEstadoMemoria_MigrarEsIdempotente(t *testing.T) {
	e := NuevoEstado()
	e.Sembrar(t, tenantUno, "s-conflicto", idCanonico, markCanonical)
	e.Sembrar(t, tenantUno, "s-conflicto", idHuerfano, markOrphan)
	e.Sembrar(t, tenantUno, "s-solo-huerfano", idHuerfano, markSecond)

	for vez := range 3 {
		migrar(t, e, tenantUno, idHuerfano, idCanonico)

		exigirDuenoEs(t, e, tenantUno, "s-conflicto", idCanonico, markCanonical)
		exigirDuenoEs(t, e, tenantUno, "s-solo-huerfano", idCanonico, markSecond)
		if got := e.duenosDe(tenantUno, "s-solo-huerfano"); !slices.Equal(got, []string{idCanonico}) {
			t.Errorf("tras la migración %d, duenosDe = %q; quiere solo [%s]", vez+1, got, idCanonico)
		}
	}
}

// Un huérfano sin estado no hace nada, ni da error, ni crea estado para el canónico; y un
// contacto migrado sobre sí mismo tampoco pierde el suyo.
func TestEstadoMemoria_MigrarSinEstadoYSobreSiMismo(t *testing.T) {
	e := NuevoEstado()
	e.Sembrar(t, tenantUno, "s-propia", idCanonico, markCanonical)

	migrar(t, e, tenantUno, idHuerfano, idCanonico) // el huérfano no tiene estado
	migrar(t, e, tenantUno, idCanonico, idCanonico) // sobre sí mismo

	exigirDuenoEs(t, e, tenantUno, "s-propia", idCanonico, markCanonical)
	exigirSinEstado(t, e, tenantUno, "otra-sesion")

	vacio := NuevoEstado()
	migrar(t, vacio, tenantUno, idHuerfano, idCanonico)
	exigirSinEstado(t, vacio, tenantUno, "s-propia")
}

// Con el contexto cancelado devuelve su error, envuelto, y no migra nada.
func TestEstadoMemoria_MigrarConContextoCancelado(t *testing.T) {
	e := NuevoEstado()
	e.Sembrar(t, tenantUno, "s1", idHuerfano, markOrphan)
	ctx, cancelar := context.WithCancel(t.Context())
	cancelar()

	err := e.MigrateContactID(ctx, tenantUno, idHuerfano, idCanonico)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("MigrateContactID con el contexto cancelado = %v; quiere context.Canceled (errors.Is)", err)
	}
	exigirDuenoEs(t, e, tenantUno, "s1", idHuerfano, markOrphan) // no migró
}

// Sembrar, observar y migrar desde muchas goroutines a la vez: cada sesión termina en el
// canónico con la marca con que se sembró, también cuando varias goroutines migran el mismo huérfano a la vez (idempotencia
// bajo contención). Corre bajo -race, que es el que ve la carrera si falta el cerrojo.
//
// Sembrar y Dueno pueden hacer t.Fatalf, y FailNow solo es válido en la goroutine que corre el
// test: llamarlos con el t del padre desde otra goroutine sería un uso incorrecto de testing. Por
// eso cada goroutine abre SU subtest con t.Run: el cuerpo corre en la goroutine propia de ese
// subtest y con su *testing.T, donde un Fatalf es legítimo y solo corta ese subtest. testing lo
// admite expresamente (Run puede llamarse a la vez desde varias goroutines) a condición de que
// todas las llamadas vuelvan antes que el test padre, que es lo que garantiza wg.Wait. Los 64
// subtests siguen corriendo a la vez: Run solo bloquea a la goroutine que lo llamó. No hay otra
// vía sin tocar el doble: Sembrar es su único camino de siembra y pide un *testing.T.
func TestEstadoMemoria_Concurrencia(t *testing.T) {
	const sesiones = 64
	e := NuevoEstado()
	var wg sync.WaitGroup
	for i := range sesiones {
		wg.Go(func() {
			t.Run(fmt.Sprintf("session-%d", i), func(t *testing.T) {
				sesion, suyo := fmt.Sprintf("s-%d", i), fmt.Sprintf("huerfano-%d", i)
				e.Sembrar(t, tenantUno, sesion, suyo, concurrentMark(i))
				exigirDuenoEs(t, e, tenantUno, sesion, suyo, concurrentMark(i))
				if err := e.MigrateContactID(t.Context(), tenantUno, suyo, idCanonico); err != nil {
					t.Errorf("MigrateContactID(%s): %v", suyo, err)
				}
				exigirDuenoEs(t, e, tenantUno, sesion, idCanonico, concurrentMark(i))
			})
		})
	}
	wg.Wait()
	for i := range sesiones {
		exigirDuenoEs(t, e, tenantUno, fmt.Sprintf("s-%d", i), idCanonico, concurrentMark(i))
	}

	// Aquí no hace falta subtest: estas goroutines solo llaman a MigrateContactID y a t.Errorf,
	// que no hace FailNow y sí puede llamarse desde cualquier goroutine.
	e.Sembrar(t, tenantUno, "s-contendida", idCanonico, markCanonical)
	e.Sembrar(t, tenantUno, "s-contendida", idHuerfano, markOrphan)
	for range sesiones {
		wg.Go(func() {
			if err := e.MigrateContactID(t.Context(), tenantUno, idHuerfano, idCanonico); err != nil {
				t.Errorf("MigrateContactID contendida: %v", err)
			}
		})
	}
	wg.Wait()
	exigirDuenoEs(t, e, tenantUno, "s-contendida", idCanonico, markCanonical)
}

// concurrentMark es la marca de la fila de la goroutine i de TestEstadoMemoria_Concurrencia.
func concurrentMark(i int) string {
	return fmt.Sprintf("mark-%d", i)
}
