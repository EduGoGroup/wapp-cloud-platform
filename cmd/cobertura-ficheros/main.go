// Command cobertura-ficheros es el candado de cobertura POR FICHERO de la reconstrucción
// modular (05 §5, D-12; F0 T0.9): lee un perfil de `go test -coverprofile`, recorre los
// ficheros de producción del alcance y exige a cada fichero en verde un porcentaje de
// sentencias cubiertas ≥ umbral. La lógica vive en internal/candados (probada contra
// árboles que muerden); este comando solo la cablea e imprime la tabla.
//
// Uso (lo invoca `make cobertura-ficheros`, que es quien fija el alcance):
//
//	cobertura-ficheros -perfil <cobertura.out> -dirs <dir1,dir2,…> [-umbral 80] [-raiz .]
//
// Salida: una línea por fichero evaluado con su porcentaje, una por exento Postgres, una
// por violación ("fichero: motivo") y el resumen FICHEROS_EVALUADOS=N, POR_DEBAJO=M (todas
// las violaciones, también las marcas de exención ilegítimas) y EXENTOS_POSTGRES=K.
//
// Código de salida: 0 sin violaciones, 1 con alguna, 2 por error de uso o de E/S.
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
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/candados"
)

// Códigos de salida del comando.
const (
	rcBien      = 0
	rcViolacion = 1
	rcUso       = 2
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
	fuentes, err := candados.Recorrer(opc.raiz, opc.dirs, false)
	if err != nil {
		out.linea("cobertura-ficheros: %v", err)
		return rcUso
	}

	violaciones := candados.Cobertura(perfil, fuentes, opc.umbral)
	exentos := candados.Exentos(fuentes)
	evaluables := candados.Evaluables(perfil, fuentes)

	out.linea("cobertura-ficheros: umbral %g %%, alcance %s", opc.umbral, strings.Join(opc.dirs, ", "))
	medidas := medirPorFichero(perfil, fuentes)
	for _, ruta := range evaluables {
		m := medidas[ruta]
		pct := 100 * float64(m.Cubiertas) / float64(m.Sentencias)
		// Se trunca al decimal, como el Motivo de candados.Cobertura: un 79.96 % no se
		// imprime como 80.0 %.
		out.linea("%6.1f %%  %s", math.Floor(pct*10)/10, ruta)
	}
	for _, ruta := range exentos {
		out.linea("EXENTO postgres  %s", ruta)
	}
	for _, v := range violaciones {
		out.linea("VIOLACION %s", v)
	}
	out.linea("FICHEROS_EVALUADOS=%d", len(evaluables))
	out.linea("POR_DEBAJO=%d", len(violaciones))
	out.linea("EXENTOS_POSTGRES=%d", len(exentos))

	switch {
	case out.err != nil:
		return rcUso // la tabla no llegó entera: el veredicto no se puede leer
	case len(violaciones) > 0:
		return rcViolacion
	default:
		return rcBien
	}
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
}

// leerOpciones interpreta args; -perfil y -dirs son obligatorios y el umbral va en (0, 100].
// El alcance no tiene valor por defecto a propósito: la lista vive en UN sitio, la variable
// COBERTURA_DIRS del Makefile.
func leerOpciones(args []string, salida io.Writer) (opciones, error) {
	var (
		opc  opciones
		dirs string
	)
	fs := flag.NewFlagSet("cobertura-ficheros", flag.ContinueOnError)
	fs.SetOutput(salida)
	fs.StringVar(&opc.perfil, "perfil", "", "perfil de `go test -coverprofile` (obligatorio)")
	fs.Float64Var(&opc.umbral, "umbral", 80, "porcentaje mínimo de sentencias cubiertas por fichero (D-12)")
	fs.StringVar(&opc.raiz, "raiz", ".", "raíz desde la que se resuelven los -dirs")
	fs.StringVar(&dirs, "dirs", "", "directorios del alcance, relativos a -raiz y separados por comas (obligatorio)")
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
	for _, d := range strings.Split(dirs, ",") {
		if d = strings.TrimSpace(d); d != "" {
			opc.dirs = append(opc.dirs, d)
		}
	}
	if len(opc.dirs) == 0 {
		return opc, errors.New("falta -dirs")
	}
	return opc, nil
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
// clave entera o un sufijo tras «/»; gana la Ruta más larga). Es solo presentación: el
// veredicto lo da candados.Cobertura.
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
