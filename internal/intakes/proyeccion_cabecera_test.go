package intakes

// proyeccion_cabecera_test.go — LAS COLUMNAS Y LOS DESTINOS DEL Scan CUADRAN
// (Plan 044 · Ola 4 · T4.5).
//
// 🔴 EL DEFECTO QUE ESTE FICHERO EXISTE PARA CAZAR ya está descrito en el propio
// postgres.go: «meter una columna entre dos ya existentes obligaría a mover los
// destinos de los dos escaneos a la vez — un descuadre que COMPILA y devuelve el
// estado en el total». Es la peor clase de fallo: el compilador no lo ve, el
// linter no lo ve, y en producción o revienta el Scan o —si los tipos casan— la
// cabecera sale con los valores corridos.
//
// Y HASTA HOY SOLO LO VEÍA POSTGRES. Todo lo que ejercita estos dos escaneos son
// tests de integración (TestIntegration*/TestE2E*), que se SALTAN sin DATABASE_URL:
// en la corrida normal el descuadre pasaba entero, en verde, hasta el despliegue.
// Estos dos tests corren SIEMPRE y sin base de datos.
//
// Es un test INTERNO (package intakes) porque las dos piezas que tienen que cuadrar
// —la constante de columnas y la función de escaneo— son privadas, y sacarlas al
// wire solo para poder mirarlas sería empeorar el diseño para poder probarlo.
//
// T4.5 lo escribe porque es la tarea que añadió la undécima columna
// (expiry_reminded_at) y tuvo que tocar los dos escaneos; la deuda, sin embargo, es
// vieja y esto la salda para todas las columnas, no solo la suya.

import (
	"strings"
	"testing"
)

// contadorDeDestinos es un rowScanner que no lee nada: solo cuenta cuántos destinos
// le pasó la función de escaneo. Devolver nil deja que el resto del cuerpo corra
// sobre valores cero, que es exactamente lo que se quiere — aquí se mide la FORMA,
// no el contenido.
type contadorDeDestinos struct{ destinos int }

func (c *contadorDeDestinos) Scan(dest ...any) error {
	c.destinos = len(dest)
	return nil
}

var _ rowScanner = (*contadorDeDestinos)(nil)

// contarColumnas cuenta las columnas de una lista SQL de proyección. Sirve para las
// dos porque las dos son literales del propio fichero: nada de esto viene de fuera.
func contarColumnas(lista string) int {
	n := 0
	for _, col := range strings.Split(lista, ",") {
		if strings.TrimSpace(col) != "" {
			n++
		}
	}
	return n
}

// TestProyecciónCabecera_ScanIntakeTieneUnDestinoPorColumna cuadra intakeCols con
// scanIntake. Son la MISMA lista vista desde los dos lados, y su orden es un
// contrato implícito que nada más vigila.
func TestProyecciónCabecera_ScanIntakeTieneUnDestinoPorColumna(t *testing.T) {
	columnas := contarColumnas(intakeCols)
	if columnas == 0 {
		t.Fatal("intakeCols salió con CERO columnas: el contador está roto y su verde no vale nada")
	}

	c := &contadorDeDestinos{}
	if _, err := scanIntake(c); err != nil {
		t.Fatalf("scanIntake sobre un scanner que no falla devolvió error: %v", err)
	}
	if c.destinos != columnas {
		t.Fatalf("intakeCols tiene %d columnas y scanIntake pasa %d destinos.\n\n"+
			"Es el descuadre que el comentario de intakeCols advierte: COMPILA y solo revienta "+
			"—o miente— contra Postgres, que en la corrida normal ni se ejecuta (los tests que "+
			"tocan esto se saltan sin DATABASE_URL). Al añadir una columna se tocan LOS DOS lados, "+
			"y siempre AL FINAL de la lista.\ncolumnas=%s", columnas, c.destinos, intakeCols)
	}
}

// TestProyecciónCabecera_ScanDetailRowTieneUnDestinoPorColumna hace lo mismo con el
// join del export, que proyecta la cabecera CON el prefijo `p.` más las seis
// columnas de la línea. No comparte la constante con el anterior —la escribe a mano
// por el prefijo—, y por eso es justo el que más fácil se queda atrás.
func TestProyecciónCabecera_ScanDetailRowTieneUnDestinoPorColumna(t *testing.T) {
	columnas := contarColumnas(listaDelJoin(t))
	if columnas == 0 {
		t.Fatal("la proyección del join salió con CERO columnas: el recorte está roto")
	}

	c := &contadorDeDestinos{}
	if _, _, _, err := scanDetailRow(c); err != nil {
		t.Fatalf("scanDetailRow sobre un scanner que no falla devolvió error: %v", err)
	}
	if c.destinos != columnas {
		t.Fatalf("la proyección del join tiene %d columnas y scanDetailRow pasa %d destinos.\n\n"+
			"Mismo descuadre silencioso que en scanIntake, y en el camino del EXPORT: una hoja de "+
			"cálculo con las columnas corridas es peor que un error", columnas, c.destinos)
	}
}

// TestProyecciónCabecera_LasDosProyeccionesTraenLoMismo es la mitad que los dos
// anteriores no pueden afirmar: que el listado y el export leen las MISMAS columnas.
// Cada uno por su lado puede cuadrar consigo mismo y traer cosas distintas, y
// entonces una solicitud enseñaría un campo en la bandeja y no en el CSV.
func TestProyecciónCabecera_LasDosProyeccionesTraenLoMismo(t *testing.T) {
	deLaCabecera := contarColumnas(intakeCols)
	delJoin := contarColumnas(listaDelJoin(t))
	const columnasDeLaLínea = 6 // sku, label, customization, qty, unit_price, added_at

	if delJoin-columnasDeLaLínea != deLaCabecera {
		t.Fatalf("el join proyecta %d columnas de cabecera y intakeCols tiene %d.\n\n"+
			"Las dos leen la MISMA fila de public.intakes: si divergen, un campo nuevo sale en la "+
			"bandeja y falta en el export (o al revés) sin que nada se ponga rojo",
			delJoin-columnasDeLaLínea, deLaCabecera)
	}
}

// listaDelJoin recorta la lista de proyección de listIntakeDetailsBody: lo que hay
// entre su SELECT y su FROM. Se recorta en vez de mantener una copia porque una
// copia envejecería por su cuenta, que es exactamente el fallo que estos tests
// persiguen.
func listaDelJoin(t *testing.T) string {
	t.Helper()
	const desde, hasta = "SELECT ", "FROM page p"
	i := strings.Index(listIntakeDetailsBody, desde)
	j := strings.Index(listIntakeDetailsBody, hasta)
	if i < 0 || j < 0 || j <= i {
		t.Fatalf("no se pudo recortar la proyección de listIntakeDetailsBody (SELECT en %d, FROM en "+
			"%d). La consulta se reescribió: arregla este recorte ANTES de fiarte de su verde", i, j)
	}
	return listIntakeDetailsBody[i+len(desde) : j]
}
