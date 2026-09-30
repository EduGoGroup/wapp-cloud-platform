package candados

import "github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"

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
	panic(pendiente.Implementar("candados.Fronteras"))
}
