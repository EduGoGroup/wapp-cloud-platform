// consulta.go — CUÁNDO EL CARRITO PIDE AYUDA, Y QUÉ ACEPTA DE VUELTA
// (Plan 044 · Ola 3.5 · T3.5-2).
//
// ════════════════════════════════════════════════════════════════════════════
// EL TERCER ESCALÓN
// ════════════════════════════════════════════════════════════════════════════
//
// T3.5-1 puso delante de la sub-máquina una cascada DETERMINISTA (preresolutor.go)
// que traduce «hamburgesa» al código 1. Lo que esa cascada no puede hacer, y su
// propia cabecera lo dice, es aritmética del lenguaje («mejor dos» → 2) ni
// entender un rótulo abreviado por donde no toca («finalizar» contra «Confirmar y
// finalizar»). Eso es una CONSULTA: el módulo la eleva y el engine la resuelve
// (modules/consulta.go, engine/consulta.go).
//
// El orden es el mismo de siempre y no cambia: código exacto → cascada → consulta.
// Preguntar es lo ÚLTIMO, porque es lo único que cuesta tiempo de un turno de
// WhatsApp y lo único que puede equivocarse de forma interesante.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 QUÉ NIVELES PREGUNTAN, Y CUÁLES NO PUEDEN PREGUNTAR NUNCA
// ════════════════════════════════════════════════════════════════════════════
//
// Preguntan los MISMOS que admiten cascada (opcionesDelNivel), más quantity.
//
//   - item_note, order_note y buyer_data SIGUEN EXCLUIDOS, y aquí el motivo pesa
//     MÁS que en la cascada: allí el texto del cliente se comparaba en memoria y
//     no salía del proceso; una consulta lo MANDA FUERA, a un modelo. Mandar el
//     nombre, el RUT o la dirección de alguien —que se escriben CIFRADOS en
//     intake_buyer_data justo para que no queden en claro en ningún sitio— a un
//     servicio de interpretación sería deshacer el ADR-0017 por la puerta de atrás.
//     Dos tests lo fijan (preresolutor_test.go) y este fichero no los toca:
//     consultable NO añade ni un nivel a los que opcionesDelNivel ya declara.
//
//   - quantity SÍ pregunta, y es el caso que justifica la tarea entera. Está fuera
//     de la cascada porque la similitud ortográfica no sabe convertir «dos» en 2;
//     eso es exactamente lo que un modelo hace bien. La respuesta admisible son
//     DÍGITOS: no hay catálogo que ofrecer y no hace falta.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 LO QUE VUELVE SE VALIDA CONTRA EL CATÁLOGO QUE SE OFRECIÓ
// ════════════════════════════════════════════════════════════════════════════
//
// El resolutor es un modelo: puede inventarse un código, devolver la frase del
// cliente tal cual, o contestar en prosa. Por eso el módulo NO se cree lo que le
// llega: solo acepta un código que él mismo puso en Consulta.Opciones, o dígitos
// para la cantidad. Todo lo demás se descarta y la entrada sigue INTACTA hacia la
// sub-máquina, que repromptea como el día antes de esta tarea.
//
// Esa validación es también la última barrera de privacidad: si el veredicto solo
// puede ser un código del catálogo, no hay forma de que el texto del cliente entre
// en el estado del carrito por esta puerta, ni siquiera si el modelo lo devuelve.
package cart

import (
	"strings"

	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// maxDigitosCantidad acota lo que se acepta como cantidad ANTES de dársela a la
// sub-máquina. No es la regla de negocio —esa es de stepQuantity, que exige >= 1 y
// hace su propio Atoi—: es una guarda contra un resolutor que devuelva una tira de
// dígitos absurda. Cuatro dígitos es más de lo que nadie pide por WhatsApp.
const maxDigitosCantidad = 4

// preresolveOConsulta es el punto ÚNICO por el que el carrito traduce lo que el
// cliente escribió, en las DOS pasadas del turno:
//
//	1.ª pasada (sin veredicto en Vars): corre la cascada determinista. Si resuelve,
//	   devuelve el código y el turno sigue normal. Si no resuelve y el nivel admite
//	   consulta, devuelve la PETICIÓN y el turno del módulo termina ahí.
//	2.ª pasada (con veredicto sembrado por el engine): aplica el veredicto si es
//	   admisible y sigue. NO vuelve a correr la cascada —ya corrió en la primera, y
//	   su telemetría cuenta UNA vez por mensaje, no dos— y sobre todo NO vuelve a
//	   pedir: la presencia de la clave es la señal de «ya preguntaste».
//
// Devolver (input, nil) es siempre el camino de siempre, byte a byte.
func (m Module) preresolveOConsulta(cat Catalog, st cartState, vars map[string]any, input string) (string, *modules.Consulta) {
	if v, hay := modules.VeredictoDe(vars); hay {
		return aplicaVeredicto(cat, st, input, v), nil
	}
	salida, escalon := m.preresolve(cat, st, input)
	if escalon != "" && escalon != escalonNinguno {
		return salida, nil // La cascada resolvió: no hay nada que preguntar.
	}
	c, ok := consultable(cat, st, input)
	if !ok {
		return salida, nil
	}
	return input, &c
}

// consultable decide si este turno merece una consulta y, si la merece, construye
// la petición. Las cuatro puertas de la regla de oro del pre-resolutor siguen
// valiendo aquí, y por los mismos motivos:
//
//  1. Entrada vacía: no hay nada que interpretar.
//  2. Entrada NUMÉRICA: un número que el cliente teclea es un índice de pantalla o
//     una cantidad, y los dos los resuelve el camino de siempre. Preguntar por un
//     número sería pagar un modelo para que confirme lo obvio.
//  3. Un CÓDIGO del nivel: idem, su dueño ya lo resuelve por igualdad exacta.
//  4. PROSA por encima de maxTokensEntrada: aquí el techo NO es por coste de
//     comparar (eso era la cascada) sino porque WhatsApp admite 4.096 caracteres y
//     un turno de una persona no puede quedarse esperando a que un modelo digiera
//     una parrafada. Si algún día se decide que la prosa larga también es del LLM,
//     se sube esta constante y se mide; hoy el valor de la tarea está en «mejor
//     dos» y en «finalizar», que caben de sobra.
func consultable(cat Catalog, st cartState, input string) (modules.Consulta, bool) {
	in := strings.TrimSpace(input)
	if in == "" || esNumero(in) {
		return modules.Consulta{}, false
	}
	tokens := textmatch.SplitTokens(in)
	if len(tokens) == 0 || len(tokens) > maxTokensEntrada {
		return modules.Consulta{}, false
	}
	if st.Level == LevelQuantity {
		// El nivel de la CANTIDAD no tiene opciones que ofrecer: la respuesta es un
		// número. Es el único nivel que pregunta sin estar en opcionesDelNivel, y la
		// razón está en la cabecera.
		return modules.Consulta{Clase: modules.ClaseCantidad, Nivel: st.Level, Texto: in}, true
	}
	opciones := opcionesDelNivel(cat, st)
	if len(opciones) == 0 || esCodigoDelNivel(opciones, in) {
		// 🔴 Sin opciones NO se pregunta, y ese `nil` es el que trae la exclusión de
		// item_note / order_note / buyer_data desde opcionesDelNivel: la privacidad
		// se hereda de una sola lista fail-closed en vez de repetirse aquí, donde
		// alguien podría olvidarse de mantenerla al día.
		return modules.Consulta{}, false
	}
	ofrecidas := make([]modules.OpcionConsulta, 0, len(opciones))
	for _, o := range opciones {
		ofrecidas = append(ofrecidas, modules.OpcionConsulta{Codigo: o.codigo, Etiqueta: o.etiqueta})
	}
	return modules.Consulta{Clase: modules.ClaseOpcion, Nivel: st.Level, Texto: in, Opciones: ofrecidas}, true
}

// aplicaVeredicto traduce el veredicto a la entrada que verá la sub-máquina, o
// deja la entrada INTACTA si no hay nada aplicable.
//
// Dejarla intacta cubre los cuatro casos degradados con el MISMO gesto —sin
// resolutor, fallo, no concluyente y código inadmisible— y no es una omisión: la
// pantalla que sale entonces es la que el carrito produce hoy ante algo que no
// entiende (el reprompt de su nivel, y a los tres el menú de salida). El módulo NO
// inventa un mensaje nuevo de «no te entendí porque el modelo no estaba»: eso sería
// contarle a la clienta una avería nuestra, y el motivo del veredicto está en el
// enum para quien quiera decidir otra cosa más adelante, no para imprimirlo.
func aplicaVeredicto(cat Catalog, st cartState, input string, v modules.Veredicto) string {
	if !v.Resuelto() {
		return input
	}
	c, ok := consultable(cat, st, input)
	if !ok || !codigoAdmisible(c, v.Codigo) {
		return input
	}
	return v.Codigo
}

// codigoAdmisible es la aduana: solo pasa lo que el propio módulo ofreció.
func codigoAdmisible(c modules.Consulta, codigo string) bool {
	if c.Clase == modules.ClaseCantidad {
		return esNumero(codigo) && len(codigo) <= maxDigitosCantidad
	}
	for _, o := range c.Opciones {
		if o.Codigo == codigo {
			return true
		}
	}
	return false
}
