package candados

import (
	"path"
	"slices"
	"sort"
	"strings"
)

// Puente es un import declarado de un paquete NUEVO a uno VIEJO (05 §4.1): la única forma
// legítima de que el árbol nuevo toque lo viejo fuera de internal/arranque.
//
//   - Desde: directorio del paquete nuevo que importa, relativo a la raíz del repo
//     (p. ej. "internal/modulos/captacion/pipeline"). Casa con el directorio del fichero si
//     es igual o si es un prefijo terminado en «/» (Desde "internal/apipublica" cubre
//     "internal/apipublica/x").
//   - Hacia: paquete viejo importado, relativo a la raíz (p. ej. "internal/flujos/store").
//     Casa solo con un import EXACTAMENTE igual (un subpaquete necesita su propio puente).
//   - Motivo: por qué existe; Nace y Muere: fase que lo crea y fase que lo borra ("F7", "F8").
type Puente struct {
	Desde  string
	Hacia  string
	Motivo string
	Nace   string
	Muere  string
}

// Reglas es la tabla de fronteras. Vive en Go, en internal/modulos/fronteras_test.go
// (diseno.md §4.1): tipada y diffable, sin parser propio.
//
//   - Capas: módulo nuevo → módulos nuevos que PUEDE importar, además de platform, nucleo y
//     pendiente, que todo el árbol nuevo puede importar. Un módulo sin entrada no puede
//     importar ningún otro.
//   - Mapa: prefijo viejo ("internal/iam") → módulo ("acceso"), según 04 §4. Gana el prefijo
//     más largo que case en frontera de «/».
//   - Conmutados: módulos que el arranque nuevo ya cablea con lo nuevo (commit conmutar(<m>)).
//   - Puentes: imports nuevo → viejo declarados.
//   - FasesCerradas: fases ya cerradas ("F0", "F1"…); un Puente cuyo Muere está aquí debió
//     borrarse.
type Reglas struct {
	Capas         map[string][]string
	Mapa          map[string]string
	Conmutados    []string
	Puentes       []Puente
	FasesCerradas []string
}

// Fronteras comprueba los imports de fuentes contra r y devuelve una violación por arista
// prohibida.
//
// modulo es la ruta del módulo Go ("github.com/EduGoGroup/wapp-cloud-platform"): un import
// "<modulo>/internal/x/y" se relativiza a "internal/x/y"; los imports que no empiezan por
// modulo+"/" (biblioteca estándar, terceros) se ignoran. fuentes trae el árbol nuevo Y el
// viejo, producción y tests (un test nuevo que importa lo viejo sería portar a escondidas,
// 05 E-8): cada fichero se clasifica por su Ruta, y cada import por su ruta relativizada.
//
// Clasificación (por prefijo en frontera de «/»):
//   - árbol NUEVO: internal/modulos, internal/nucleo, internal/apipublica,
//     internal/pendiente, internal/candados, internal/arranque, cmd/server-modular y
//     cmd/cobertura-ficheros;
//   - platform: internal/platform. Es código viejo, pero todo el árbol nuevo puede
//     importarlo, igual que nucleo y pendiente;
//   - VIEJO: cualquier otra ruta del módulo.
//
// El módulo de una ruta internal/modulos/<m>/… es <m>. La arista de una violación se
// escribe "<directorio del fichero> → <import relativizado>" y aparece literal en Motivo,
// que empieza por "regla N: " con N la regla que la dispara:
//
//  1. un fichero de internal/modulos/<m> importa internal/modulos/<n>, n ≠ m, con n fuera
//     de Capas[m];
//  2. un fichero del árbol nuevo, fuera de internal/arranque (y sus subpaquetes: es el
//     composition root, en F0 cablea lo viejo), importa un paquete VIEJO sin un Puente
//     cuyo Desde y Hacia casen;
//  3. un fichero de internal/arranque importa un paquete viejo cuyo módulo según Mapa
//     (prefijo más largo) está en Conmutados;
//  4. un fichero de internal/apipublica importa CUALQUIER paquete viejo, aunque haya un
//     Puente que lo declare (la cara nueva solo habla con lo nuevo);
//  5. un fichero VIEJO importa algo del árbol nuevo. Única excepción: el test
//     internal/bootstrap/arranque/huella_vieja_test.go puede importar
//     internal/arranque/huellatest;
//  6. un Puente declarado que ningún fichero de fuentes usa —ninguno cuyo directorio case
//     con Desde importa Hacia— (puente muerto: se borra), o
//     cuyo Muere está en FasesCerradas. Estas violaciones no tienen fichero culpable: su
//     Fichero es el Desde del puente y su Motivo nombra "Desde → Hacia".
//
// Un mismo import da como mucho UNA violación: la regla 4 manda sobre la 2 (apipublica sin
// puente no da dos). Importar platform, nucleo, pendiente, el propio módulo o un módulo de
// Capas[m] nunca es violación.
func Fronteras(modulo string, fuentes []Fuente, r Reglas) []Violacion {
	vs := make([]Violacion, 0)
	usados := make([]bool, len(r.Puentes)) // regla 6: qué puentes usa algún fichero
	for _, f := range fuentes {
		dir := path.Dir(f.Ruta)
		// Un mismo import da como mucho una violación: Go admite importar dos veces la misma
		// ruta con nombres distintos, y eso no debe duplicar la arista.
		vistos := make(map[string]bool)
		for _, imp := range f.Archivo.Imports {
			rel, ok := relativizar(modulo, imp.Path.Value)
			if !ok || vistos[rel] {
				continue
			}
			vistos[rel] = true
			marcarPuentes(r.Puentes, dir, rel, usados)
			if motivo := juzgarImport(f.Ruta, dir, rel, r); motivo != "" {
				vs = append(vs, Violacion{Fichero: f.Ruta, Motivo: motivo})
			}
		}
	}
	vs = append(vs, puentesCaducos(r, usados)...)
	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Fichero != vs[j].Fichero {
			return vs[i].Fichero < vs[j].Fichero
		}
		return vs[i].Motivo < vs[j].Motivo
	})
	return vs
}

// arbolNuevo son los prefijos del árbol nuevo (clasificación del contrato de Fronteras).
var arbolNuevo = []string{
	"internal/modulos",
	"internal/nucleo",
	"internal/apipublica",
	"internal/pendiente",
	"internal/candados",
	"internal/arranque",
	"cmd/server-modular",
	"cmd/cobertura-ficheros",
}

const (
	prefijoModulos    = "internal/modulos"
	prefijoPlatform   = "internal/platform"
	prefijoArranque   = "internal/arranque"
	prefijoApipublica = "internal/apipublica"
	// La única arista viejo → nuevo admitida (regla 5): la huella del arranque viejo compara
	// contra el mismo contenedor que la del nuevo (diseno.md §6).
	huellaVieja = "internal/bootstrap/arranque/huella_vieja_test.go"
	huellatest  = "internal/arranque/huellatest"
)

// bajo dice si ruta es prefijo o cuelga de él en frontera de «/»: "internal/iam" cubre
// "internal/iam/app" pero no "internal/iamx".
func bajo(ruta, prefijo string) bool {
	prefijo = strings.TrimSuffix(prefijo, "/")
	return ruta == prefijo || strings.HasPrefix(ruta, prefijo+"/")
}

func esNuevo(ruta string) bool {
	for _, p := range arbolNuevo {
		if bajo(ruta, p) {
			return true
		}
	}
	return false
}

// relativizar quita las comillas del literal del import y el prefijo del módulo Go; false si
// el import no es del módulo (biblioteca estándar, terceros), que el candado ignora.
func relativizar(modulo, literal string) (string, bool) {
	p := strings.Trim(literal, "\"`")
	rel, ok := strings.CutPrefix(p, modulo+"/")
	return rel, ok && rel != ""
}

// moduloDe devuelve <m> para una ruta internal/modulos/<m>/…, o "" si no cuelga de un módulo.
func moduloDe(ruta string) string {
	resto, ok := strings.CutPrefix(ruta, prefijoModulos+"/")
	if !ok {
		return ""
	}
	m, _, _ := strings.Cut(resto, "/")
	return m
}

// juzgarImport aplica las reglas 1–5 a una arista fichero → import y devuelve el Motivo de
// la violación, o "" si la arista está permitida. Cada arista cae en una sola rama, así que
// da como mucho una violación.
func juzgarImport(ruta, dir, rel string, r Reglas) string {
	arista := dir + " → " + rel
	if !esNuevo(ruta) {
		// platform es código viejo a estos efectos: tampoco puede importar el árbol nuevo.
		if esNuevo(rel) && (ruta != huellaVieja || rel != huellatest) {
			return "regla 5: " + arista + ": código viejo que importa el árbol nuevo"
		}
		return ""
	}
	if esNuevo(rel) {
		return juzgarModulos(dir, rel, arista, r)
	}
	if bajo(rel, prefijoPlatform) {
		return "" // viejo, pero importable desde todo el árbol nuevo
	}
	// De aquí abajo: árbol nuevo → paquete viejo.
	switch {
	case bajo(ruta, prefijoApipublica):
		// Manda sobre la regla 2: ni un Puente la levanta.
		return "regla 4: " + arista + ": la cara nueva solo habla con lo nuevo, ni con puente"
	case bajo(ruta, prefijoArranque):
		// El composition root puede cablear lo viejo (exento de la regla 2) salvo lo de un
		// módulo ya conmutado.
		if m := moduloViejo(r.Mapa, rel); m != "" && slices.Contains(r.Conmutados, m) {
			return "regla 3: " + arista + ": el módulo " + m + " ya está conmutado; el arranque cablea lo nuevo"
		}
		return ""
	case hayPuente(r.Puentes, dir, rel):
		return ""
	default:
		return "regla 2: " + arista + ": árbol nuevo que importa lo viejo sin un Puente declarado"
	}
}

// juzgarModulos aplica la regla 1 a una arista nuevo → nuevo: solo muerde entre módulos
// distintos de internal/modulos, y el propio módulo o los de Capas[m] siempre valen.
func juzgarModulos(dir, rel, arista string, r Reglas) string {
	m, n := moduloDe(dir), moduloDe(rel)
	if m == "" || n == "" || m == n || slices.Contains(r.Capas[m], n) {
		return ""
	}
	return "regla 1: " + arista + ": el módulo " + m + " no puede importar " + n + " (fuera de Capas)"
}

// moduloViejo devuelve el módulo de rel según Mapa, ganando el prefijo más largo; "" si
// ninguno casa.
func moduloViejo(mapa map[string]string, rel string) string {
	mejor, modulo := "", ""
	for prefijo, m := range mapa {
		if bajo(rel, prefijo) && len(prefijo) > len(mejor) {
			mejor, modulo = prefijo, m
		}
	}
	return modulo
}

func casaPuente(p Puente, dir, rel string) bool {
	return bajo(dir, p.Desde) && rel == p.Hacia
}

func hayPuente(puentes []Puente, dir, rel string) bool {
	for _, p := range puentes {
		if casaPuente(p, dir, rel) {
			return true
		}
	}
	return false
}

// marcarPuentes anota qué puentes usa la arista dir → rel. El uso es literal (el contrato de
// la regla 6): cuenta cualquier fichero cuyo directorio case con Desde.
func marcarPuentes(puentes []Puente, dir, rel string, usados []bool) {
	for i, p := range puentes {
		if casaPuente(p, dir, rel) {
			usados[i] = true
		}
	}
}

// puentesCaducos aplica la regla 6: un puente sin uso o cuyo Muere es una fase cerrada. No
// hay fichero culpable, así que se nombra el Desde del puente. Si se dan las dos causas, son
// dos violaciones: cada una se arregla por separado (borrar el puente arregla ambas).
func puentesCaducos(r Reglas, usados []bool) []Violacion {
	var vs []Violacion
	for i, p := range r.Puentes {
		arista := p.Desde + " → " + p.Hacia
		if !usados[i] {
			vs = append(vs, Violacion{Fichero: p.Desde,
				Motivo: "regla 6: " + arista + ": puente muerto, ningún fichero lo usa; se borra"})
		}
		if slices.Contains(r.FasesCerradas, p.Muere) {
			vs = append(vs, Violacion{Fichero: p.Desde,
				Motivo: "regla 6: " + arista + ": su Muere " + p.Muere + " es una fase cerrada; debió borrarse"})
		}
	}
	return vs
}
