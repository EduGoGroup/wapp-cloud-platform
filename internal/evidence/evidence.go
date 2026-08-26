// Package evidence es LA REGLA DE LA EVIDENCIA, y solo eso: decidir si la frase que
// el modelo dice haber copiado del cliente aparece DE VERDAD en el texto del cliente.
//
// # POR QUÉ ES UN PAQUETE Y NO UNA COPIA EN CADA ETAPA
//
// La regla nació dentro de `intakeahead/saneo.go` (Plan 044 · Ola 1.6 · T1.6-4) para
// la etapa P1, y aquel comentario dejó escrito, palabra por palabra, lo que iba a
// pasar: «el consumidor llega en la Ola 2 (P3 extrae ítems con evidencia) y partir la
// regla en dos funciones el día que aparezca es como las dos empiezan a decir cosas
// distintas». Ese día es T2.2 —P2 ancla cada idea a una frase del literal—, así que la
// regla se MUDÓ aquí entera en vez de copiarse. Hay una regla y un solo sitio donde se
// cambia; `saneo.go` la llama, no la reimplementa.
//
// # DE DÓNDE SALE LA REGLA: ESTÁ MEDIDA
//
// El invariante viene del clasificador del Edge y es el que justifica todo esto: «los
// modelos pequeños a veces copian valores de los ejemplos del prompt (alucinaron un
// pedido "887" que el cliente nunca escribió). Exigir que el valor aparezca en el
// mensaje elimina esa clase entera de fallo».
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
//     donde el original tenía uno o al revés. Importa más de lo que parece desde la
//     Ola 2: el literal que compone el flush es MULTILÍNEA (una entrada del hilo por
//     línea) y una evidencia legítima puede cruzar ese salto.
//
// 🔴 **NO normaliza acentos, y es una decisión.** La evidencia es, por contrato, una
// COPIA LITERAL de una frase del original: un modelo que escribe «cafe» donde el
// cliente escribió «café» no está copiando, está reescribiendo, y esa es exactamente la
// conducta que hay que cazar. El coste es real y va dicho: alguna evidencia legítima se
// rechazará. Y es el lado seguro, porque lo que se pierde al rechazar es UNA idea o UN
// adelanto de ventana, nunca la solicitud del cliente.
//
// # ESTE PAQUETE NO DECIDE QUÉ HACER CON LO QUE NO PASA
//
// Y es deliberado, porque cada llamante responde distinto y las dos respuestas son
// correctas: en P1 una evidencia que no aparece TUMBA la clasificación entera (es el
// único campo que la sostiene); en P2 descarta ESA idea y deja vivas las demás (el
// diseño es conservador: una salida malformada no puede tirar la solicitud del
// cliente). Aquí solo se responde «aparece» o «no aparece».
package evidence

import "strings"

// Normalize baja a minúsculas y colapsa TODO blanco (espacios, tabuladores, saltos de
// línea) a un espacio simple, recortando los extremos. strings.Fields hace las tres
// cosas de una pasada y con la definición de blanco de Unicode, que es la que
// corresponde a un texto escrito por una persona en WhatsApp.
//
// Se exporta —y no se esconde dentro de Contains— porque quien comprueba VARIAS frases
// contra el MISMO texto (P2 tiene una evidencia por idea) normaliza el texto una vez y
// no una por frase.
func Normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// Contains dice si `frase` aparece en un texto YA NORMALIZADO con Normalize. La frase
// se normaliza aquí dentro, con la misma regla, para que las dos partes de la
// comparación se midan con la misma vara y para que sea imposible olvidarse de una.
//
// Una frase vacía devuelve false: no hay nada que respaldar, y aceptarla convertiría la
// omisión del campo en un pase libre.
func Contains(textoNorm, frase string) bool {
	f := Normalize(frase)
	if f == "" {
		return false
	}
	return strings.Contains(textoNorm, f)
}
