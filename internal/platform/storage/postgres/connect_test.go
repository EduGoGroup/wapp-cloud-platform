package postgres

import (
	"context"
	"testing"
	"time"
)

// TestOpen_EmptyDSN verifica el fail-fast con DSN vacío (sin tocar la red).
func TestOpen_EmptyDSN(t *testing.T) {
	if _, err := Open(context.Background(), Config{DSN: ""}); err == nil {
		t.Fatal("Open con DSN vacío debería devolver error")
	}
}

// TestDefaultsDelPool_NoSeMovieronEnLaOla4 clava los CUATRO defaults del pool
// contra su valor literal (Plan 050 · Ola 4 · T4.5).
//
// Existe porque el resto de la red no puede ver este cambio. Desde T4.2,
// internal/platform/config REFERENCIA estas constantes en vez de copiarlas —
// que es lo correcto, una sola fuente— pero tiene un efecto de segundo orden
// incómodo: el test de configuración afirma que el default de config ES esta
// constante, así que sigue verde con cualquier valor. Mover el 25 a 26 no
// rompía absolutamente nada en todo el árbol. Este test es el único sitio
// donde el NÚMERO está escrito dos veces a propósito.
//
// Lo que protege es el criterio literal de la ola: "expone e instrumenta; no
// cambia ni un valor" (ADR-0040 §Decisión.8, "primero se mide, después se
// mueve"). Ponerlo rojo no es un fallo: es la señal de que alguien movió el
// pool. Quien lo haga a propósito —hoy solo T4.6, con la curva de T5.5
// delante— actualiza este test EN EL MISMO commit y deja escrito el número
// medido al lado. No se relaja, no se borra.
//
// Contexto para ese día, medido el 2026-08-18 contra el Neon de UAT
// (ep-purple-star-adg2lp8a, host directo): max_connections = 901 con 6
// reservadas para superusuario ⇒ 895 usables. Hoy abren pool contra esa misma
// base DOS procesos —cloud-platform y guardian-bff— así que el peor caso
// teórico son 2 × 25 = 50 conexiones, un 5,6 % del techo. Neon no es la
// restricción; la restricción es este 25.
func TestDefaultsDelPool_NoSeMovieronEnLaOla4(t *testing.T) {
	casos := []struct {
		nombre string
		tiene  any
		quiero any
	}{
		{"DefaultMaxOpenConns", DefaultMaxOpenConns, 25},
		{"DefaultMaxIdleConns", DefaultMaxIdleConns, 5},
		{"DefaultConnMaxLifetime", DefaultConnMaxLifetime, time.Hour},
		{"DefaultConnMaxIdleTime", DefaultConnMaxIdleTime, 10 * time.Minute},
	}
	for _, c := range casos {
		if c.tiene != c.quiero {
			t.Errorf("%s = %v, quiero %v — la Ola 4 del Plan 050 expone el pool, NO lo mueve. "+
				"Si el cambio es deliberado (T4.6), actualiza este test en el mismo commit "+
				"y escribe al lado el número medido que lo justifica",
				c.nombre, c.tiene, c.quiero)
		}
	}
}
