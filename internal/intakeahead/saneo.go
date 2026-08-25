package intakeahead

import (
	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/evidence"
)

// ============================================================================
// EL SANEO CONTRA EL TEXTO ORIGINAL (Plan 044 · Ola 1.6 · T1.6-4)
//
// # DE DÓNDE SALE ESTA REGLA: HAY UN `sanitizeParams`, Y ESTÁ EN EL EDGE
//
// `tasks.md` · T1.6-4 lo cita como si estuviera a mano («armar el prompt desde
// intent_configs, `sanitizeParams`/umbral en el caller»). Lo hay, pero NO donde se
// puede llamar: vive en `edge/wapp-edge-intent/classifier/sanitize.go`, es minúscula
// —no exportada— y está en OTRO repo y en OTRO proceso. Era el allowlist del
// clasificador que corría en el Edge; con el push muerto (D-044.31) ese camino ya no
// se recorre, así que el saneo hay que rehacerlo AQUÍ, que es donde ahora vive el
// texto. El módulo compartido lo dice sin rodeos:
//
//	«Este paquete NO sanea los params contra el texto original — ese allowlist lo
//	aplica el caller, que es quien tiene el texto» (llm/parse.go, Classification.Params)
//
// # QUÉ SE HEREDA DEL EDGE Y QUÉ NO, CON EL MOTIVO DE CADA COSA
//
// Se hereda el INVARIANTE, que está MEDIDO y es el que justifica todo esto: «los
// modelos pequeños a veces copian valores de los ejemplos del prompt (alucinaron un
// pedido "887" que el cliente nunca escribió). Exigir que el valor aparezca en el
// mensaje elimina esa clase entera de fallo». Y se hereda su primer filtro, que es el
// más barato y el que más corta: **un param cuya CLAVE no está declarada por la
// intención elegida se cae sin mirar su valor**. Con el catálogo publicado en campo
// —`params: []`, D-044.20— eso significa que TODO param que devuelva el modelo es, por
// definición, invención.
//
// NO se hereda su tolerancia a plural/tipeo (allí basta con que aparezca el prefijo de
// 4 caracteres del valor). Allí tenía sentido porque el sujeto era un valor corto
// —«pizzas» ~ «pizza»—; aquí el sujeto principal es la EVIDENCIA, que es una frase, y
// un prefijo de 4 caracteres sobre una frase no comprueba nada («quie» está en
// cualquier mensaje que empiece por «quiero»). Aplicarla solo a los params y no a la
// evidencia daría dos reglas que mantener sincronizadas para cubrir un campo que hoy
// no tiene consumidor. Una regla, y dicha.
//
// # LA GRANULARIDAD ES LA FRASE, NO LA PALABRA — Y LA REGLA YA NO VIVE AQUÍ
//
// Un valor se acepta si aparece COMO SUBCADENA del mensaje del cliente, con las
// mayúsculas y los blancos normalizados y los acentos NO. El enunciado entero, con el
// motivo de cada una de esas tres decisiones, está en `internal/evidence`: la regla se
// MUDÓ allí en T2.2 —tal como este mismo comentario predecía en su último párrafo—
// cuando P2 estrenó el segundo consumidor. Este paquete la llama; no la reimplementa.
//
// Lo que sí es de aquí es el lado seguro del rechazo: un rechazo en P1 solo significa
// NO ADELANTAR la ventana, que es lo mismo que pasa hoy cuando no llega ninguna señal
// (REQ-35). El error caro es el contrario.
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
//
// 🔧 **2026-08-25 (T2.2): el consumidor llegó, y no fue el que este párrafo esperaba.**
// Fue **P2** —una `evidence` por idea— antes que P3. La regla NO se partió en dos: se
// mudó a `internal/evidence` y las dos etapas llaman a la misma función.
// ============================================================================

// sanear aplica el allowlist a la clasificación IN SITU y devuelve (a) si la
// evidencia se sostiene sobre el texto y (b) cuántos params se descartaron.
//
// Recibe la MISMA entrada con la que se armó el prompt, y no el texto suelto, por el
// mismo motivo por el que lo hace ParseClassification: de ahí salen las DOS cosas que
// el allowlist necesita —el texto original y los params que la intención elegida
// declara— y pasarlas por separado es la forma de que un día dejen de coincidir.
//
// Modifica `c.Params` a propósito: lo que sale de aquí es lo único que el resto del
// pipeline puede ver, y dejar los inventados dentro «por si acaso» sería dejar la
// puerta abierta a que alguien los lea más adelante sin saber que no valen.
func sanear(c *llm.Classification, in llm.ClassifyRequestInput) (evidenciaOK bool, descartados int) {
	if c == nil {
		return false, 0
	}
	norm := normalizar(in.Text)
	declarados := paramsDeclarados(in, c.Intent)
	for k, v := range c.Params {
		if _, ok := declarados[k]; !ok {
			// Clave que la intención elegida NO declara: fuera sin mirar el valor. Es el
			// filtro heredado del Edge y el que más corta con el catálogo de campo.
			delete(c.Params, k)
			descartados++
			continue
		}
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

// paramsDeclarados devuelve los nombres de parámetro que declara la intención elegida.
//
// Una intención que no está en el catálogo —solo puede ser la etiqueta reservada de lo
// desconocido, que el parser acepta sin declararla— no declara ninguno, y entonces
// TODO param se cae. Es lo correcto: quien acaba de decir que no entendió nada no puede
// haber extraído un parámetro de nada.
func paramsDeclarados(in llm.ClassifyRequestInput, intent string) map[string]struct{} {
	for _, spec := range in.Catalog {
		if spec.Name != intent {
			continue
		}
		out := make(map[string]struct{}, len(spec.Params))
		for _, p := range spec.Params {
			out[p] = struct{}{}
		}
		return out
	}
	return nil
}

// contieneFrase es UN ALIAS de la regla compartida, y existe solo para que las tres
// llamadas de `sanear` se lean igual que antes de la mudanza. Una frase vacía devuelve
// false: la evidencia vacía solo es legítima cuando la intención es la etiqueta de lo
// desconocido, y ese caso ni llega a disparar nada.
func contieneFrase(textoNorm, frase string) bool {
	return evidence.Contains(textoNorm, frase)
}

// normalizar es el otro alias: ver evidence.Normalize.
func normalizar(s string) string {
	return evidence.Normalize(s)
}
