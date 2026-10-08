// Porta internal/publicapi/intakes_llm_gate.go @ ed60c24 (180 líneas) e
// internal/publicapi/intakes.go @ ed60c24 (writeIntakeDetail, líneas 310-327).
//
// intakes_llm_gate.go — EL GATE POR CAMPO DEL DETALLE DE UNA SOLICITUD (Plan 044 · T4.1,
// D-044.47 §1 y D-044.48 §2) y la ÚNICA salida por la que ese detalle llega al wire. Van juntos
// a propósito: quien escribe el detalle es quien lo filtra, y no hay otro camino.
//
// No exporta nada y nació con el verde (05 E-4, P6): sus promesas están en el contrato de
// MountIntakes y las prueba intakes_llm_gate_test.go a través de las cuatro rutas que responden
// el detalle.

package apipublica

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// Las dos claves del contrato §7.4 que PERTENECEN al pipeline LLM del Plan 044 y
// que un tenant sin `llm_intake` no puede ver. En la cara vieja eran
// claveSuggestedQuestions y claveVariantOptions.
//
// Están aquí como literales y no importadas del productor a propósito: esto es
// el CONTRATO DEL CABLE, y el cable no puede cambiar de forma porque alguien
// renombre un campo de Go.
//
// ⚠️ En la cara vieja lo que impedía que se desincronizaran era
// TestGateLLM_LasClavesSonLasDelContrato, que las comparaba por reflexión contra las
// etiquetas JSON de stages.PayloadRevision y stages.Linea. Ese candado NO se ha portado: el
// productor (captación) sigue siendo código viejo hasta F7 y la cara no puede importarlo. Le
// toca a F7 reponerlo contra el paquete nuevo; hasta entonces las fija la pareja de golden.
//
// 🔑 NO ESTÁN AL MISMO NIVEL, y por eso esto no es un filtro de dos claves planas:
//   - `suggested_questions` es clave RAÍZ del payload y NO lleva `omitempty`
//     en el productor, así que hoy está SIEMPRE presente.
//   - `variant_options` va anidada dentro de CADA elemento de `lines`, con `omitempty`.
const (
	intakeKeySuggestedQuestions = "suggested_questions"
	intakeKeyVariantOptions     = "variant_options"
)

// intakeWriteDetail (writeIntakeDetail en la cara vieja) escribe el detalle YA FILTRADO por el
// plan del tenant. Es el ÚNICO camino por el que el detalle sale al wire —lo usan el GET, el PUT
// de líneas, `approve` y `request-info`—, por la misma razón por la que intakeToDetailResponse es
// un solo punto: dos salidas separadas divergirían en el primer campo nuevo, y aquí divergir
// significa que una de las dos filtra y la otra no.
//
// Un fallo al filtrar responde 500 y NO el payload sin filtrar: servir el cuerpo
// entero ante un error dejaría el gate abierto justo cuando algo va mal, que es lo
// contrario de fail-closed. En la práctica no se alcanza —lo que se reserializa
// salió de un json.Unmarshal válido—, y por eso no tiene un cuerpo de error propio.
//
// `at` es el instante contra el que se marca el plazo (ver intakeToDTO).
func intakeWriteDetail(ctx context.Context, w http.ResponseWriter, feats entitlements.Resolver, tenantID string, detail intakes.Detail, at time.Time) {
	resp := intakeToDetailResponse(detail, at)
	if err := intakeApplyLLMGate(ctx, feats, tenantID, &resp); err != nil {
		writeError(w, http.StatusInternalServerError, "no se pudo preparar la solicitud")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// intakeApplyLLMGate (aplicarGateLLMIntake en la cara vieja) decide qué sale por el cable según
// el plan del tenant y TAPA lo que no le corresponde (Plan 044 · T4.1, D-044.47 §1 y D-044.48
// §2).
//
// 🔴 ESTA FUNCIÓN OCULTA, NO EXPONE, y conviene saber por qué existe: el `payload`
// de revisión viaja como json.RawMessage y se copia ENTERO (intakeToDetailResponse),
// así que los campos del 044 YA SALÍAN al wire para cualquier tenant con
// `cart_basic` — incluido uno sin `llm_intake`, que es una fuga viva contra un plan
// que existe de verdad (Basic, Comercio, Asesor IA).
//
// El gate va sobre los CAMPOS y no sobre la puerta: las siete rutas de la bandeja
// siguen tras `cart_basic`, que es quien las protege desde el Plan 041, y
// `llm_intake` NO lo sustituye. Un 403 aquí rompería una pantalla que el cliente ya
// paga (D-044.47 §1).
//
// Y va en la cara y no en el dominio porque es una regla COMERCIAL, no una
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
// destruyó un dato suyo. Lo fija TestMountIntakes_LLMGate_DoesNotHideTheLiteralPruneSeal.
//
// FAIL-CLOSED en los modos de no-resolución —resolver caído, feature ausente—, igual que
// entitlements.RequireFeature: ante la duda, se tapa. Un fallo transitorio del resolver no
// puede abrir un campo de pago.
//
// La guarda vieja «feats == nil ⇒ se tapa» no se porta: la bandeja solo se monta con un resolver
// no nil (MountIntakes), así que era inalcanzable.
func intakeApplyLLMGate(ctx context.Context, feats entitlements.Resolver, tenantID string, resp *intakeDetailResponse) error {
	if has, err := feats.Has(ctx, tenantID, entitlements.FeatureLLMIntake); err == nil && has {
		return nil // el tenant compró el nivel: el payload sale entero
	}
	for i, rev := range resp.Revisions {
		clean, err := intakeHideLLMFields(rev.Payload)
		if err != nil {
			return fmt.Errorf("apipublica: filtrar la revisión %d de la solicitud: %w", rev.RevisionNo, err)
		}
		resp.Revisions[i].Payload = clean
	}
	return nil
}

// intakeHideLLMFields (ocultarCamposLLM en la cara vieja) borra del payload de una revisión las
// dos claves del pipeline LLM: la raíz `suggested_questions` y la `variant_options` de CADA
// línea.
//
// 🔑 DECISIÓN DE CONTRATO — LA CLAVE DESAPARECE, NO QUEDA EN `[]`:
// `suggested_questions` no lleva `omitempty`, así que al filtrarla había que elegir
// entre borrarla y dejarla como lista vacía. Se BORRA, por dos razones:
//
//  1. D-044.47 §1 lo dice literal: «simplemente `suggested_questions` y
//     `variant_options` no aparecen en el cuerpo».
//  2. `[]` YA SIGNIFICA OTRA COSA en este contrato. El productor escribe
//     `[]` y no `null` a propósito, porque «no hay nada que preguntar» es una
//     respuesta. Servir `[]` a un tenant sin la feature le contaría esa respuesta
//     —«el sistema no tenía preguntas»— cuando la verdad es «este servidor no te
//     publica ese campo». La ausencia de la clave no miente; el `[]` sí.
//
// El que consume la diferencia es la app del Plan 045: con la clave ausente
// puede no pintar la sección; con `[]` pintaría una sección vacía que el dueño
// leería como «el LLM no supo qué preguntar».
//
// Sigue el molde de intakes.SplitLiteral, que recorre EL MISMO payload por los
// mismos dos niveles:
//   - Lo que no es un objeto JSON se devuelve TAL CUAL, sin error: un payload que no
//     tiene forma de objeto no puede llevar la clave raíz ni una lista `lines`, así
//     que no hay nada que tapar (y `versión desconocida` no es un fallo de esta capa).
//   - Si no se tocó nada se devuelve el original SIN reserializar. Eso importa: sin
//     esa guarda, la revisión `cart` del 041 —que no tiene ninguna de las dos
//     claves— saldría con las claves reordenadas solo para los tenants sin
//     `llm_intake`, y dos planes verían dos cuerpos distintos del MISMO dato.
func intakeHideLLMFields(payload json.RawMessage) (json.RawMessage, error) {
	root, ok := intakeAsJSONObject(payload)
	if !ok {
		return payload, nil
	}

	touched := false
	if _, found := root[intakeKeySuggestedQuestions]; found {
		delete(root, intakeKeySuggestedQuestions)
		touched = true
	}

	lines, hasLines := intakeAsJSONList(root[intakes.PayloadKeyLines])
	for i, raw := range lines {
		line, isObject := intakeAsJSONObject(raw)
		if !isObject {
			continue
		}
		if _, found := line[intakeKeyVariantOptions]; !found {
			continue
		}
		delete(line, intakeKeyVariantOptions)
		rewritten, err := json.Marshal(line)
		if err != nil {
			return nil, fmt.Errorf("apipublica: reserializar la línea %d sin %s: %w", i, intakeKeyVariantOptions, err)
		}
		lines[i] = rewritten
		touched = true
	}

	if !touched {
		return payload, nil
	}
	if hasLines {
		relisted, err := json.Marshal(lines)
		if err != nil {
			return nil, fmt.Errorf("apipublica: reserializar %s: %w", intakes.PayloadKeyLines, err)
		}
		root[intakes.PayloadKeyLines] = relisted
	}
	clean, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("apipublica: reserializar el payload filtrado: %w", err)
	}
	return clean, nil
}

// intakeAsJSONObject (comoObjetoJSON en la cara vieja) decodifica un JSON a objeto SIN
// interpretar sus valores. Devuelve false —y no error— cuando no es un objeto: aquí «no tiene
// esta forma» es un caso normal, no un fallo. Espejo de intakes.asObject, que es privada de su
// paquete.
func intakeAsJSONObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

// intakeAsJSONList (comoListaJSON en la cara vieja) es intakeAsJSONObject para arrays. El bool
// distingue «no hay lista» de «hay una lista vacía», que es la diferencia entre no tocar la
// clave y reescribirla con [].
func intakeAsJSONList(raw json.RawMessage) ([]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil || list == nil {
		return nil, false
	}
	return list, true
}
