package contacthelpertest

import (
	"context"
	"errors"
	"fmt"
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

// exigirDuenoEs falla el test si la sesión no tiene estado o no es del contacto quiere.
func exigirDuenoEs(t *testing.T, e *EstadoMemoria, tenantID, sessionID, quiere string) {
	t.Helper()
	got, ok := e.Dueno(t, tenantID, sessionID)
	if !ok || got != quiere {
		t.Errorf("Dueno(%s, %s) = (%q, %v); quiere (%q, true)", tenantID, sessionID, got, ok, quiere)
	}
}

// exigirSinEstado falla el test si la sesión tiene estado.
func exigirSinEstado(t *testing.T, e *EstadoMemoria, tenantID, sessionID string) {
	t.Helper()
	if got, ok := e.Dueno(t, tenantID, sessionID); ok {
		t.Errorf("Dueno(%s, %s) = (%q, true); quiere (\"\", false): la sesión no debía tener estado", tenantID, sessionID, got)
	}
}

// migrar migra el estado de from a to en el tenant y falla el test si hay error.
func migrar(t *testing.T, e *EstadoMemoria, tenantID, from, to string) {
	t.Helper()
	if err := e.MigrateContactID(t.Context(), tenantID, from, to); err != nil {
		t.Fatalf("MigrateContactID(%s, %s, %s): %v", tenantID, from, to, err)
	}
}

// Un estado nuevo está vacío; lo sembrado se observa por (tenant, sesión) y resiste a repetirse.
func TestEstadoMemoria_SembrarYObservar(t *testing.T) {
	e := NuevoEstado()
	exigirSinEstado(t, e, tenantUno, "s1")

	e.Sembrar(t, tenantUno, "s1", idHuerfano)
	e.Sembrar(t, tenantUno, "s2", idCanonico)
	e.Sembrar(t, tenantUno, "s1", idHuerfano) // repetir lo ya sembrado no cambia nada

	exigirDuenoEs(t, e, tenantUno, "s1", idHuerfano)
	exigirDuenoEs(t, e, tenantUno, "s2", idCanonico)
	exigirSinEstado(t, e, tenantUno, "s3")
	if got := e.duenosDe(tenantUno, "s1"); !slices.Equal(got, []string{idHuerfano}) {
		t.Errorf("duenosDe tras sembrar dos veces lo mismo = %q; quiere [%s]", got, idHuerfano)
	}
}

// Dos contactos sembrados en la misma sesión son dos dueños (el conflicto de la fusión), y se
// listan ordenados para que el mensaje de error sea estable.
func TestEstadoMemoria_DosDuenosEnLaMismaSesion(t *testing.T) {
	e := NuevoEstado()
	e.Sembrar(t, tenantUno, "s1", idHuerfano)
	e.Sembrar(t, tenantUno, "s1", idCanonico)

	got := e.duenosDe(tenantUno, "s1")

	if want := []string{idCanonico, idHuerfano}; !slices.Equal(got, want) {
		t.Errorf("duenosDe = %q; quiere %q (ordenados)", got, want)
	}
	if got := e.duenosDe(tenantUno, "otra-sesion"); len(got) != 0 {
		t.Errorf("duenosDe de una sesión sin estado = %q; quiere vacío", got)
	}
}

// Migrar sin conflicto re-clava todas las sesiones del huérfano en el canónico y deja en paz
// el estado que el canónico ya tenía.
func TestEstadoMemoria_MigrarSinConflicto(t *testing.T) {
	e := NuevoEstado()
	e.Sembrar(t, tenantUno, "s1", idHuerfano)
	e.Sembrar(t, tenantUno, "s2", idHuerfano)
	e.Sembrar(t, tenantUno, "s-propia", idCanonico)

	migrar(t, e, tenantUno, idHuerfano, idCanonico)

	exigirDuenoEs(t, e, tenantUno, "s1", idCanonico)
	exigirDuenoEs(t, e, tenantUno, "s2", idCanonico)
	exigirDuenoEs(t, e, tenantUno, "s-propia", idCanonico)
}

// Migrar con conflicto (R-17): en la sesión donde los dos tenían estado se conserva el del
// canónico y se descarta el del huérfano; en las demás, el estado del huérfano migra. Dueno
// falla el test si quedaran dos dueños, así que basta con leerlo.
func TestEstadoMemoria_MigrarConConflictoConservaElCanonico(t *testing.T) {
	e := NuevoEstado()
	e.Sembrar(t, tenantUno, "s-conflicto", idCanonico)
	e.Sembrar(t, tenantUno, "s-conflicto", idHuerfano)
	e.Sembrar(t, tenantUno, "s-solo-huerfano", idHuerfano)

	migrar(t, e, tenantUno, idHuerfano, idCanonico)

	exigirDuenoEs(t, e, tenantUno, "s-conflicto", idCanonico)
	exigirDuenoEs(t, e, tenantUno, "s-solo-huerfano", idCanonico)
	if got := e.duenosDe(tenantUno, "s-conflicto"); !slices.Equal(got, []string{idCanonico}) {
		t.Errorf("duenosDe de la sesión en conflicto = %q; quiere solo [%s]: el estado del huérfano se descarta", got, idCanonico)
	}
}

// Un tenant no ve el estado de otro ni lo toca al migrar: la misma sesión y los mismos
// contact_id en dos tenants son dos estados distintos.
func TestEstadoMemoria_AislamientoPorTenant(t *testing.T) {
	e := NuevoEstado()
	e.Sembrar(t, tenantUno, "s1", idHuerfano)
	e.Sembrar(t, tenantDos, "s1", idHuerfano)
	e.Sembrar(t, tenantDos, "solo-en-b", idAjeno)

	exigirSinEstado(t, e, tenantUno, "solo-en-b")

	migrar(t, e, tenantUno, idHuerfano, idCanonico)

	exigirDuenoEs(t, e, tenantUno, "s1", idCanonico)
	exigirDuenoEs(t, e, tenantDos, "s1", idHuerfano) // el de B sigue siendo del huérfano
	exigirDuenoEs(t, e, tenantDos, "solo-en-b", idAjeno)
}

// Migrar dos veces lo mismo deja el estado como lo dejó la primera, sin error.
func TestEstadoMemoria_MigrarEsIdempotente(t *testing.T) {
	e := NuevoEstado()
	e.Sembrar(t, tenantUno, "s-conflicto", idCanonico)
	e.Sembrar(t, tenantUno, "s-conflicto", idHuerfano)
	e.Sembrar(t, tenantUno, "s-solo-huerfano", idHuerfano)

	for vez := range 3 {
		migrar(t, e, tenantUno, idHuerfano, idCanonico)

		exigirDuenoEs(t, e, tenantUno, "s-conflicto", idCanonico)
		exigirDuenoEs(t, e, tenantUno, "s-solo-huerfano", idCanonico)
		if got := e.duenosDe(tenantUno, "s-solo-huerfano"); !slices.Equal(got, []string{idCanonico}) {
			t.Errorf("tras la migración %d, duenosDe = %q; quiere solo [%s]", vez+1, got, idCanonico)
		}
	}
}

// Un huérfano sin estado no hace nada, ni da error, ni crea estado para el canónico; y un
// contacto migrado sobre sí mismo tampoco pierde el suyo.
func TestEstadoMemoria_MigrarSinEstadoYSobreSiMismo(t *testing.T) {
	e := NuevoEstado()
	e.Sembrar(t, tenantUno, "s-propia", idCanonico)

	migrar(t, e, tenantUno, idHuerfano, idCanonico) // el huérfano no tiene estado
	migrar(t, e, tenantUno, idCanonico, idCanonico) // sobre sí mismo

	exigirDuenoEs(t, e, tenantUno, "s-propia", idCanonico)
	exigirSinEstado(t, e, tenantUno, "otra-sesion")

	vacio := NuevoEstado()
	migrar(t, vacio, tenantUno, idHuerfano, idCanonico)
	exigirSinEstado(t, vacio, tenantUno, "s-propia")
}

// Con el contexto cancelado devuelve su error, envuelto, y no migra nada.
func TestEstadoMemoria_MigrarConContextoCancelado(t *testing.T) {
	e := NuevoEstado()
	e.Sembrar(t, tenantUno, "s1", idHuerfano)
	ctx, cancelar := context.WithCancel(t.Context())
	cancelar()

	err := e.MigrateContactID(ctx, tenantUno, idHuerfano, idCanonico)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("MigrateContactID con el contexto cancelado = %v; quiere context.Canceled (errors.Is)", err)
	}
	exigirDuenoEs(t, e, tenantUno, "s1", idHuerfano) // no migró
}

// Sembrar, observar y migrar desde muchas goroutines a la vez: cada sesión termina en el
// canónico, también cuando varias goroutines migran el mismo huérfano a la vez (idempotencia
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
				e.Sembrar(t, tenantUno, sesion, suyo)
				exigirDuenoEs(t, e, tenantUno, sesion, suyo)
				if err := e.MigrateContactID(t.Context(), tenantUno, suyo, idCanonico); err != nil {
					t.Errorf("MigrateContactID(%s): %v", suyo, err)
				}
				exigirDuenoEs(t, e, tenantUno, sesion, idCanonico)
			})
		})
	}
	wg.Wait()
	for i := range sesiones {
		exigirDuenoEs(t, e, tenantUno, fmt.Sprintf("s-%d", i), idCanonico)
	}

	// Aquí no hace falta subtest: estas goroutines solo llaman a MigrateContactID y a t.Errorf,
	// que no hace FailNow y sí puede llamarse desde cualquier goroutine.
	e.Sembrar(t, tenantUno, "s-contendida", idCanonico)
	e.Sembrar(t, tenantUno, "s-contendida", idHuerfano)
	for range sesiones {
		wg.Go(func() {
			if err := e.MigrateContactID(t.Context(), tenantUno, idHuerfano, idCanonico); err != nil {
				t.Errorf("MigrateContactID contendida: %v", err)
			}
		})
	}
	wg.Wait()
	exigirDuenoEs(t, e, tenantUno, "s-contendida", idCanonico)
}
