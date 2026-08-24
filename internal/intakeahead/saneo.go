package intakeahead

import (
	"strings"

	"github.com/EduGoGroup/wapp-shared/llm"
)

// ============================================================================
// EL SANEO CONTRA EL TEXTO ORIGINAL (Plan 044 · Ola 1.6 · T1.6-4)
//
// # 🔴 `sanitizeParams` NO EXISTE, Y NUNCA EXISTIÓ
//
// `tasks.md` · T1.6-4 lo cita como si fuera una función que ya está en algún sitio
// («armar el prompt desde intent_configs, `sanitizeParams`/umbral en el caller»). No
// lo está: no hay un solo símbolo con ese nombre en ninguno de los repos del
// ecosistema. Lo que sí hay es el contrato del módulo compartido diciendo, con estas
// palabras, de quién es el trabajo:
//
//	«Este paquete NO sanea los params contra el texto original — ese allowlist lo
//	aplica el caller, que es quien tiene el texto» (llm/parse.go, Classification.Params)
//
// El caller es este paquete. El saneo es esto.
//
// # LA GRANULARIDAD ES LA FRASE, NO LA PALABRA
//
// Un valor se acepta si aparece COMO SUBCADENA del mensaje del cliente. No se parte
// en palabras y no se acepta «alguna palabra en común»: eso convertiría cualquier
// invención que reusara dos términos del mensaje en un valor «respaldado», que es
// justo lo que el allowlist existe para impedir.
//
// La comparación normaliza DOS cosas y ninguna más:
//
//   - **mayúsculas/minúsculas**, porque el modelo capitaliza a su gusto;
//   - **los espacios en blanco**, colapsados a uno, porque copia con saltos de línea
//     donde el original tenía uno o al revés.
//
// 🔴 **NO normaliza acentos, y es una decisión.** La evidencia es, por contrato, una
// COPIA LITERAL de una frase del original: un modelo que escribe «cafe» donde el
// cliente escribió «café» no está copiando, está reescribiendo, y esa es exactamente
// la conducta que el allowlist tiene que cazar. El coste de la decisión es real y va
// dicho: alguna evidencia legítima se rechazará. Y es el lado seguro — un rechazo
// aquí solo significa NO ADELANTAR la ventana, que es lo mismo que pasa hoy cuando no
// llega ninguna señal (REQ-35). El error caro es el contrario.
//
// # QUÉ SE HACE CON LO QUE NO PASA: DOS RESPUESTAS DISTINTAS
//
//   - **La EVIDENCIA que no aparece TUMBA la clasificación entera.** Es el campo que
//     sostiene la respuesta: si la frase que supuestamente la justifica no está en el
//     mensaje, el modelo se la inventó y no hay nada que creerle.
//   - **Los PARAMS que no aparecen se DESCARTAN uno a uno** y la clasificación sigue
//     viva. Su ausencia no invalida la intención: la política de disparo NI SIQUIERA
//     LOS MIRA (D-044.20), y el contrato publicado en campo declara `params: []`.
//
// ⚠️ Y por eso hay que decir qué consume hoy cada mitad, para que nadie la confunda
// con una promesa: el saneo de la EVIDENCIA decide si se adelanta la ventana; el de
// los PARAMS alimenta un contador del log y nada más, porque hoy no hay un solo
// consumidor de `Params` aguas abajo. Se mantiene bajo la MISMA regla —y no se borra
// «porque no lo usa nadie»— porque el consumidor llega en la Ola 2 (P3 extrae ítems
// con evidencia) y partir la regla en dos funciones el día que aparezca es como las
// dos empiezan a decir cosas distintas.
// ============================================================================

// sanear aplica el allowlist a la clasificación IN SITU y devuelve (a) si la
// evidencia se sostiene sobre el texto y (b) cuántos params se descartaron.
//
// Modifica `c.Params` a propósito: lo que sale de aquí es lo único que el resto del
// pipeline puede ver, y dejar los inventados dentro «por si acaso» sería dejar la
// puerta abierta a que alguien los lea más adelante sin saber que no valen.
func sanear(c *llm.Classification, texto string) (evidenciaOK bool, descartados int) {
	if c == nil {
		return false, 0
	}
	norm := normalizar(texto)
	for k, v := range c.Params {
		// Un valor VACÍO pasa: significa «el cliente no lo dijo», y eso lo dice el
		// contrato del parser, que lo acepta a propósito. No hay nada que respaldar.
		if v == "" {
			continue
		}
		if !contieneFrase(norm, v) {
			delete(c.Params, k)
			descartados++
		}
	}
	return contieneFrase(norm, c.Evidence), descartados
}

// contieneFrase dice si `frase` aparece en un texto YA NORMALIZADO. La frase se
// normaliza aquí, con la misma regla, para que las dos partes de la comparación se
// midan con la misma vara.
//
// Una frase vacía devuelve false: la evidencia vacía solo es legítima cuando la
// intención es la etiqueta de lo desconocido, y ese caso ni llega a disparar nada.
func contieneFrase(textoNorm, frase string) bool {
	f := normalizar(frase)
	if f == "" {
		return false
	}
	return strings.Contains(textoNorm, f)
}

// normalizar baja a minúsculas y colapsa TODO blanco (espacios, tabuladores, saltos
// de línea) a un espacio simple, recortando los extremos. strings.Fields hace las
// tres cosas de una pasada y con la definición de blanco de Unicode, que es la que
// corresponde a un texto escrito por una persona en WhatsApp.
func normalizar(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}
