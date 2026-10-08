// Porta internal/evidence/evidence.go @ 8d875ab

// Package evidence es LA REGLA DE LA EVIDENCIA, y solo eso: decidir si la frase que
// el modelo dice haber copiado del cliente aparece DE VERDAD en el texto del cliente.
//
// # POR QUÉ ES UN PAQUETE Y NO UNA COPIA EN CADA ETAPA
//
// La regla nació dentro de `intakeahead/saneo.go` (Plan 044 · Ola 1.6 · T1.6-4) para
// la etapa P1, y aquel comentario dejó escrito lo que iba a pasar: «partir la regla en
// dos funciones el día que aparezca [otro consumidor] es como las dos empiezan a decir
// cosas distintas». Ese día fue T2.2 —P2 ancla cada idea a una frase del literal—, así
// que la regla se MUDÓ aquí entera en vez de copiarse. Hay una regla y un solo sitio
// donde se cambia: la llaman `anclaje`, `stages` e `intakeahead`, no la reimplementan.
//
// # DE DÓNDE SALE LA REGLA: ESTÁ MEDIDA
//
// El invariante viene del clasificador del Edge: «los modelos pequeños a veces copian
// valores de los ejemplos del prompt (alucinaron un pedido "887" que el cliente nunca
// escribió). Exigir que el valor aparezca en el mensaje elimina esa clase entera de
// fallo».
//
// # LA GRANULARIDAD ES LA FRASE, NO LA PALABRA
//
// Una frase se acepta si aparece COMO SUBCADENA del texto del cliente. No se parte en
// palabras y no se acepta «alguna palabra en común»: eso convertiría cualquier
// invención que reusara dos términos del mensaje en un valor «respaldado», que es justo
// lo que esta regla existe para impedir.
//
// # QUÉ NORMALIZA, Y QUÉ NO
//
// La comparación normaliza DOS cosas y ninguna más:
//
//   - **mayúsculas/minúsculas**, porque el modelo capitaliza a su gusto;
//   - **los espacios en blanco**, colapsados a uno, porque copia con saltos de línea
//     donde el original tenía uno o al revés. El literal que compone el flush es
//     MULTILÍNEA (una entrada del hilo por línea) y una evidencia legítima puede cruzar
//     ese salto.
//
// 🔴 **NO normaliza acentos, y es una decisión.** La evidencia es, por contrato, una
// COPIA LITERAL de una frase del original: un modelo que escribe «cafe» donde el
// cliente escribió «café» no está copiando, está reescribiendo, y esa es exactamente la
// conducta que hay que cazar. El coste es real y va dicho: alguna evidencia legítima se
// rechazará. Y es el lado seguro, porque lo que se pierde al rechazar es UNA idea o UN
// adelanto de ventana, nunca la solicitud del cliente.
//
// Tampoco toca la puntuación ni los signos («hola, quería» no es «hola quería»;
// `a@@b` no es `a@b`), ni traduce dígitos (`١٢` no es `12`), ni compone o descompone
// Unicode («é» precompuesta no es «e» + acento combinante).
//
// # ESTE PAQUETE NO DECIDE QUÉ HACER CON LO QUE NO PASA
//
// Y es deliberado, porque cada llamante responde distinto y las dos respuestas son
// correctas: en P1 una evidencia que no aparece TUMBA la clasificación entera (es el
// único campo que la sostiene); en P2 descarta ESA idea y deja vivas las demás. Aquí
// solo se responde «aparece» o «no aparece».
package evidence

import "strings"

// Normalize baja a minúsculas y colapsa TODO blanco (espacios, tabuladores, saltos de
// línea) a un espacio simple, recortando los extremos. Es la ÚNICA normalización: texto
// y frase pasan por ella antes de compararse.
//
// «Blanco» es el de Unicode (unicode.IsSpace), que es el que corresponde a un texto
// escrito por una persona en WhatsApp: el espacio duro U+00A0, los espacios
// tipográficos U+2003 y U+202F, el ideográfico U+3000, NEL y los separadores de línea
// y párrafo cuentan. El espacio de ancho cero U+200B y el BOM U+FEFF NO son blancos y
// se conservan. Las minúsculas son también las de Unicode, sin tabla por idioma
// («İ» → «i», «ẞ» → «ß», «Σ» → «σ»). Un byte que no es UTF-8 válido sale como U+FFFD.
//
// Una cadena vacía o solo de blancos devuelve "". Es idempotente.
//
// Se exporta —y no se esconde dentro de Contains— porque quien comprueba VARIAS frases
// contra el MISMO texto (P2 tiene una evidencia por idea) normaliza el texto una vez y
// no una por frase.
func Normalize(s string) string {
	// strings.Fields hace las tres cosas (partir, colapsar, recortar) de una pasada y
	// con la definición de blanco de Unicode.
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// Contains dice si phrase aparece, como subcadena, en un texto YA NORMALIZADO con
// Normalize (antes `textoNorm`, `frase`). La frase se normaliza aquí dentro, con la
// misma regla, para que las dos partes de la comparación se midan con la misma vara y
// para que sea imposible olvidarse de una. El texto NO se normaliza aquí: pasarlo
// crudo es un error del llamante y lo normal es que dé false.
//
// Una frase vacía —o vacía tras normalizar: solo blancos— devuelve false: no hay nada
// que respaldar, y aceptarla convertiría la omisión del campo en un pase libre.
func Contains(normalizedText, phrase string) bool {
	f := Normalize(phrase)
	if f == "" {
		return false
	}
	return strings.Contains(normalizedText, f)
}
