package publicapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// Las dos claves del contrato §7.4 que PERTENECEN al pipeline LLM del Plan 044 y
// que un tenant sin `llm_intake` no puede ver.
//
// Están aquí como literales y no importadas de `intake/stages` a propósito: esto es
// el CONTRATO DEL CABLE, y el cable no puede cambiar de forma porque alguien
// renombre un campo de Go. Lo que impide que se desincronicen no es un import sino
// TestGateLLM_LasClavesSonLasDelContrato, que las compara por reflexión contra las
// etiquetas JSON reales de stages.PayloadRevision y stages.Linea: un renombre allí
// pone rojo ESTE fichero, que es justo donde hay que decidir qué se hace.
//
// 🔑 NO ESTÁN AL MISMO NIVEL, y por eso esto no es un filtro de dos claves planas:
//   - `suggested_questions` es clave RAÍZ del payload y NO lleva `omitempty`
//     (stages.PayloadRevision), así que hoy está SIEMPRE presente.
//   - `variant_options` va anidada dentro de CADA elemento de `lines`
//     (stages.Linea), con `omitempty`.
const (
	claveSuggestedQuestions = "suggested_questions"
	claveVariantOptions     = "variant_options"
)

// aplicarGateLLMIntake decide qué sale por el cable según el plan del tenant y
// TAPA lo que no le corresponde (Plan 044 · T4.1, D-044.47 §1 y D-044.48 §2).
//
// 🔴 ESTA FUNCIÓN OCULTA, NO EXPONE, y conviene saber por qué existe: el `payload`
// de revisión viaja como json.RawMessage y se copia ENTERO (toIntakeDetailResponse),
// así que los campos del 044 YA SALÍAN al wire para cualquier tenant con
// `cart_basic` — incluido uno sin `llm_intake`, que es una fuga viva contra un plan
// que existe de verdad (Basic, Comercio, Asesor IA).
//
// El gate va sobre los CAMPOS y no sobre la puerta: las siete rutas de la bandeja
// siguen tras `cart_basic`, que es quien las protege desde el Plan 041, y
// `llm_intake` NO lo sustituye. Un 403 aquí rompería una pantalla que el cliente ya
// paga (D-044.47 §1).
//
// Y va en `publicapi` y no en el dominio porque es una regla COMERCIAL, no una
// invariante del negocio: el store sigue devolviendo lo que hay, y quien decide qué
// sale por el cable es la capa que ya decide todo lo demás que sale por el cable.
//
// 🔑 LO QUE ESTA FUNCIÓN NO TAPA, Y NO ES UN OLVIDO: `literal_pruned_at`. El gate
// borra claves DEL PAYLOAD, y ese campo es hermano de `created_at` —vive fuera—, así
// que por construcción pasa entero. Y debe pasar: no es contenido del pipeline sino
// un hecho de RETENCIÓN («el texto original de tu cliente se destruyó, y este día»),
// y el literal MISMO —`source_text`, dentro del payload— tampoco está gateado, así
// que taparlo le contaría MENOS a un tenant sobre un texto que ya puede ver. Un plan
// comercial decide qué capacidades se compran, no si a alguien se le cuenta que se
// destruyó un dato suyo. Lo fija TestIntakeDetail_ElSelloDePodaNoLoTapaElGateLLM.
//
// FAIL-CLOSED en los tres modos de no-resolución —sin resolver, resolver caído,
// feature ausente—, igual que entitlements.RequireFeature: ante la duda, se tapa.
// Un fallo transitorio del resolver no puede abrir un campo de pago.
func aplicarGateLLMIntake(ctx context.Context, feats entitlements.Resolver, tenantID string, resp *intakeDetailResponse) error {
	if feats != nil {
		has, err := feats.Has(ctx, tenantID, entitlements.FeatureLLMIntake)
		if err == nil && has {
			return nil // el tenant compró el nivel: el payload sale entero
		}
	}
	for i, rev := range resp.Revisions {
		limpio, err := ocultarCamposLLM(rev.Payload)
		if err != nil {
			return fmt.Errorf("publicapi: filtrar la revisión %d de la solicitud: %w", rev.RevisionNo, err)
		}
		resp.Revisions[i].Payload = limpio
	}
	return nil
}

// ocultarCamposLLM borra del payload de una revisión las dos claves del pipeline
// LLM: la raíz `suggested_questions` y la `variant_options` de CADA línea.
//
// 🔑 DECISIÓN DE CONTRATO — LA CLAVE DESAPARECE, NO QUEDA EN `[]`:
// `suggested_questions` no lleva `omitempty`, así que al filtrarla había que elegir
// entre borrarla y dejarla como lista vacía. Se BORRA, por dos razones:
//
//  1. D-044.47 §1 lo dice literal: «simplemente `suggested_questions` y
//     `variant_options` no aparecen en el cuerpo».
//  2. `[]` YA SIGNIFICA OTRA COSA en este contrato. stages.PayloadRevision escribe
//     `[]` y no `null` a propósito, porque «no hay nada que preguntar» es una
//     respuesta. Servir `[]` a un tenant sin la feature le contaría esa respuesta
//     —«el sistema no tenía preguntas»— cuando la verdad es «este servidor no te
//     publica ese campo». La ausencia de la clave no miente; el `[]` sí.
//
// El que consume la diferencia es la app KMP del Plan 045: con la clave ausente
// puede no pintar la sección; con `[]` pintaría una sección vacía que el dueño
// leería como «el LLM no supo qué preguntar».
//
// Sigue el molde de intakes.PartirLiteral, que recorre EL MISMO payload por los
// mismos dos niveles:
//   - Lo que no es un objeto JSON se devuelve TAL CUAL, sin error: un payload que no
//     tiene forma de objeto no puede llevar la clave raíz ni una lista `lines`, así
//     que no hay nada que tapar (y `versión desconocida` no es un fallo de esta capa).
//   - Si no se tocó nada se devuelve el original SIN reserializar. Eso importa: sin
//     esa guarda, la revisión `cart` del 041 —que no tiene ninguna de las dos
//     claves— saldría con las claves reordenadas solo para los tenants sin
//     `llm_intake`, y dos planes verían dos cuerpos distintos del MISMO dato.
func ocultarCamposLLM(payload json.RawMessage) (json.RawMessage, error) {
	raiz, ok := comoObjetoJSON(payload)
	if !ok {
		return payload, nil
	}

	tocado := false
	if _, hay := raiz[claveSuggestedQuestions]; hay {
		delete(raiz, claveSuggestedQuestions)
		tocado = true
	}

	lineas, hayLineas := comoListaJSON(raiz[intakes.ClavePayloadLines])
	for i, cruda := range lineas {
		linea, esObjeto := comoObjetoJSON(cruda)
		if !esObjeto {
			continue
		}
		if _, hay := linea[claveVariantOptions]; !hay {
			continue
		}
		delete(linea, claveVariantOptions)
		nueva, err := json.Marshal(linea)
		if err != nil {
			return nil, fmt.Errorf("publicapi: reserializar la línea %d sin %s: %w", i, claveVariantOptions, err)
		}
		lineas[i] = nueva
		tocado = true
	}

	if !tocado {
		return payload, nil
	}
	if hayLineas {
		relista, err := json.Marshal(lineas)
		if err != nil {
			return nil, fmt.Errorf("publicapi: reserializar %s: %w", intakes.ClavePayloadLines, err)
		}
		raiz[intakes.ClavePayloadLines] = relista
	}
	limpio, err := json.Marshal(raiz)
	if err != nil {
		return nil, fmt.Errorf("publicapi: reserializar el payload filtrado: %w", err)
	}
	return limpio, nil
}

// comoObjetoJSON decodifica un JSON a objeto SIN interpretar sus valores. Devuelve
// false —y no error— cuando no es un objeto: aquí «no tiene esta forma» es un caso
// normal, no un fallo. Espejo de intakes.comoObjeto, que es privada de su paquete.
func comoObjetoJSON(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

// comoListaJSON es comoObjetoJSON para arrays. El bool distingue «no hay lista» de
// «hay una lista vacía», que es la diferencia entre no tocar la clave y reescribirla
// con [].
func comoListaJSON(raw json.RawMessage) ([]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var lista []json.RawMessage
	if err := json.Unmarshal(raw, &lista); err != nil || lista == nil {
		return nil, false
	}
	return lista, true
}
