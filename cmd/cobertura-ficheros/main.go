// Command cobertura-ficheros es el INFORME de cobertura POR FICHERO de la reconstrucción
// modular (05 §5 y E-9; F0 T0.9): lee un perfil de `go test -coverprofile`, recorre los
// ficheros de producción del alcance e imprime el porcentaje de sentencias cubiertas de cada
// fichero en verde. Nació como candado (D-12: ≥ 80 % o rc=1); desde P2 (Jhoan, 2026-10-03) NO
// bloquea: un fichero por debajo se cuenta y se nombra, pero el comando sale con rc=0. La
// lógica vive en internal/candados (probada contra árboles de prueba); este comando solo la
// cablea e imprime la tabla.
//
// Uso (lo invoca `make cobertura-ficheros`, que es quien fija el alcance):
//
//	cobertura-ficheros -perfil <cobertura.out> -dirs <dir1,dir2,…> [-bridges <dir,…>] [-umbral 80] [-raiz .]
//
// -dirs son directorios que entran ENTEROS (recursivos). -bridges son directorios de los que
// entran SOLO los adaptadores de arranque bridge_<x>.go, hijos directos (D-F1-16, 05 §4.2):
// así el informe mide un adaptador (el último, internal/arranque/bridge_contact.go, murió en
// F8 con conmutar(conversacion)) sin medir el resto del arranque.
//
// Salida: una línea por fichero evaluado con su porcentaje, una "BAJO fichero: motivo" por
// cada fichero por debajo de -umbral, y el resumen FICHEROS_EVALUADOS=N y POR_DEBAJO=M.
// -umbral ya no decide nada: es la línea de referencia con la que se cuenta POR_DEBAJO. No hay
// exentos por umbral: los adaptadores Postgres se miden y salen en la tabla como los demás.
//
// Código de salida: 0 siempre que el informe se haya podido hacer, haya o no ficheros por
// debajo; 2 por un error real: de uso, perfil ilegible o mal formado, fuente que no parsea o
// salida que no se pudo escribir.
//
// Solo importa internal/candados y la biblioteca estándar: el comando está fuera de los
// alcances de los candados, pero no debe arrastrar ningún paquete viejo.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/candados"
)

// Códigos de salida del comando.
const (
	rcBien = 0
	rcUso  = 2
)

func main() {
	os.Exit(ejecutar(os.Args[1:], os.Stdout))
}

// ejecutar es el comando entero sin os.Exit ni os.Stdout, para poder probarlo: interpreta
// args, escribe la tabla y los errores en salida y devuelve el código de salida.
func ejecutar(args []string, salida io.Writer) int {
	out := &escritor{w: salida}
	opc, err := leerOpciones(args, salida)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			out.linea("cobertura-ficheros: %v", err)
		}
		return rcUso
	}

	perfil, err := leerPerfil(opc.perfil)
	if err != nil {
		out.linea("cobertura-ficheros: %v", err)
		return rcUso
	}
	fuentes, err := leerFuentes(opc)
	if err != nil {
		out.linea("cobertura-ficheros: %v", err)
		return rcUso
	}

	porDebajo := candados.Cobertura(perfil, fuentes, opc.umbral)
	evaluables := candados.Evaluables(perfil, fuentes)

	out.linea("cobertura-ficheros: informe (no bloquea), referencia %g %%, alcance %s", opc.umbral, strings.Join(opc.dirs, ", "))
	if len(opc.bridges) > 0 {
		out.linea("cobertura-ficheros: más los bridge_*.go de %s", strings.Join(opc.bridges, ", "))
	}
	medidas := medirPorFichero(perfil, fuentes)
	for _, ruta := range evaluables {
		m := medidas[ruta]
		pct := 100 * float64(m.Cubiertas) / float64(m.Sentencias)
		// Se trunca al decimal, como el Motivo de candados.Cobertura: un 79.96 % no se
		// imprime como 80.0 %.
		out.linea("%6.1f %%  %s", math.Floor(pct*10)/10, ruta)
	}
	for _, v := range porDebajo {
		out.linea("BAJO %s", v)
	}
	out.linea("FICHEROS_EVALUADOS=%d", len(evaluables))
	out.linea("POR_DEBAJO=%d", len(porDebajo))

	if out.err != nil {
		return rcUso // la tabla no llegó entera: el informe no se puede leer
	}
	// P2 (05 E-9): un fichero por debajo NO cambia el código de salida. Es un informe.
	return rcBien
}

// escritor escribe líneas en w y recuerda el primer error de escritura; tras él no escribe
// más. Evita comprobar el error de cada Fprintf sin perderlo.
type escritor struct {
	w   io.Writer
	err error
}

// linea escribe format (con args) y un salto de línea, salvo que ya haya fallado antes.
func (e *escritor) linea(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format+"\n", args...)
}

// opciones son los argumentos ya validados.
type opciones struct {
	perfil string
	umbral float64
	raiz   string
	dirs   []string
	// bridges son los directorios de los que solo entran los bridge_<x>.go (puede ir vacío).
	bridges []string
}

// leerOpciones interpreta args; -perfil y -dirs son obligatorios y el umbral va en (0, 100].
// El alcance no tiene valor por defecto a propósito: la lista vive en UN sitio, la variable
// COBERTURA_DIRS del Makefile. -bridges es opcional (vacío = ningún adaptador) y su lista es
// COBERTURA_BRIDGE_DIRS.
func leerOpciones(args []string, salida io.Writer) (opciones, error) {
	var (
		opc           opciones
		dirs, bridges string
	)
	fs := flag.NewFlagSet("cobertura-ficheros", flag.ContinueOnError)
	fs.SetOutput(salida)
	fs.StringVar(&opc.perfil, "perfil", "", "perfil de `go test -coverprofile` (obligatorio)")
	fs.Float64Var(&opc.umbral, "umbral", 80, "línea de referencia de POR_DEBAJO, en % de sentencias cubiertas por fichero (no bloquea)")
	fs.StringVar(&opc.raiz, "raiz", ".", "raíz desde la que se resuelven los -dirs")
	fs.StringVar(&dirs, "dirs", "", "directorios del alcance, relativos a -raiz y separados por comas (obligatorio)")
	fs.StringVar(&bridges, "bridges", "", "directorios de los que solo entran los bridge_*.go directos, relativos a -raiz y separados por comas (opcional)")
	if err := fs.Parse(args); err != nil {
		return opc, err
	}
	switch {
	case fs.NArg() > 0:
		return opc, fmt.Errorf("argumentos sobrantes: %q", fs.Args())
	case opc.perfil == "":
		return opc, errors.New("falta -perfil")
	case opc.umbral <= 0 || opc.umbral > 100:
		return opc, fmt.Errorf("-umbral %g fuera de (0, 100]", opc.umbral)
	}
	opc.dirs = splitList(dirs)
	if len(opc.dirs) == 0 {
		return opc, errors.New("falta -dirs")
	}
	opc.bridges = splitList(bridges)
	return opc, nil
}

// splitList parte una lista separada por comas, quita los espacios y descarta los vacíos.
func splitList(lista string) []string {
	var out []string
	for _, d := range strings.Split(lista, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// leerFuentes reúne las fuentes de producción del informe: los -dirs enteros más los
// bridge_<x>.go directos de cada -bridges. Un fichero que llegue por los dos caminos (un
// -bridges dentro de un -dirs) cuenta UNA vez; el resultado va ordenado por Ruta.
func leerFuentes(opc opciones) ([]candados.Fuente, error) {
	fuentes, err := candados.Recorrer(opc.raiz, opc.dirs, false)
	if err != nil {
		return nil, err
	}
	vistas := make(map[string]bool, len(fuentes))
	for _, f := range fuentes {
		vistas[f.Ruta] = true
	}
	for _, dir := range opc.bridges {
		bridges, err := candados.WalkBridges(opc.raiz, dir, false)
		if err != nil {
			return nil, err
		}
		for _, b := range bridges {
			if !vistas[b.Ruta] {
				vistas[b.Ruta] = true
				fuentes = append(fuentes, b)
			}
		}
	}
	sort.Slice(fuentes, func(i, j int) bool { return fuentes[i].Ruta < fuentes[j].Ruta })
	return fuentes, nil
}

// leerPerfil abre y agrega el perfil de cobertura.
func leerPerfil(ruta string) (map[string]candados.Fichero, error) {
	datos, err := os.ReadFile(ruta) //nolint:gosec // G304: la ruta la pasa nuestro propio Makefile (fichero temporal de go test), no un tercero
	if err != nil {
		return nil, err
	}
	return candados.Agregar(bytes.NewReader(datos))
}

// medirPorFichero lleva el perfil a las Ruta de las fuentes de producción para imprimir el
// porcentaje de cada fichero evaluado. Sigue el cruce que documenta candados.Cobertura (la
// clave entera o un sufijo tras «/»; gana la Ruta más larga). Es solo presentación: la lista
// de ficheros por debajo la da candados.Cobertura.
func medirPorFichero(perfil map[string]candados.Fichero, fuentes []candados.Fuente) map[string]candados.Fichero {
	produccion := make(map[string]bool, len(fuentes))
	for _, f := range fuentes {
		if !f.EsTest {
			produccion[f.Ruta] = true
		}
	}
	out := make(map[string]candados.Fichero)
	for k, fi := range perfil {
		ruta, ok := casar(k, produccion)
		if !ok {
			continue
		}
		m := out[ruta]
		m.Ruta = ruta
		m.Sentencias += fi.Sentencias
		m.Cubiertas += fi.Cubiertas
		out[ruta] = m
	}
	return out
}

// casar devuelve la Ruta más larga de rutas que es k o un sufijo de k tras una «/».
func casar(k string, rutas map[string]bool) (string, bool) {
	if rutas[k] {
		return k, true
	}
	for i := 0; i < len(k); i++ {
		if k[i] == '/' && rutas[k[i+1:]] {
			return k[i+1:], true
		}
	}
	return "", false
}
