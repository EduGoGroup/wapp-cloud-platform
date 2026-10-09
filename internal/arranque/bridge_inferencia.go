// Adaptador de tipos entre la inferencia NUEVA (internal/modulos/inferencia/llmvia) y el consumidor
// VIEJO que pide tipos del paquete viejo: internal/turnoacotado (hasta F8). No porta ningún
// fichero: nace en F4 (T4.10, arquitectura §4 del plan de F4) para que el arranque nuevo cablee UN
// solo selector de vía, el nuevo, sin tocar a ese consumidor (E-1).
//
// Tuvo una segunda mitad, llmConfigBridge, que presentaba el almacén nuevo de tenant_llm al
// internal/reanalisis viejo: murió en F7 (T7.23, conmutar(captacion)), cuando el reanalisis nuevo
// pasó a recibir ese almacén tal cual.

package arranque

import (
	"context"
	"errors"

	legacyllm "github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/turnoacotado"
)

// turneroBridge presenta el *llmvia.Selector NUEVO como el turnoacotado.Turnero que pide el
// resolutor viejo del turno acotado (turnoacotado.New, fase 5). Hace falta porque Turnero nombra
// el TurnoRequest del llmvia VIEJO, y los dos TurnoRequest son tipos distintos para Go aunque
// tengan los mismos dos campos: un *llmvia.Selector nuevo no satisface turnoacotado.Turnero.
//
// Delega en sel, que es EL MISMO selector que reciben las etapas, el aforo, quotetext e
// intakeahead: la alternativa (un selector viejo aparte, solo para el turno) serían dos
// selectores en el proceso (R4.7.b) y el adaptador de local.Frame de F3 vivo para siempre.
//
// Promesas:
//   - Turno copia el TurnoRequest viejo en uno nuevo campo a campo (Prompt y Formato), sin
//     mirarlos, y delega en sel.Turno con el mismo ctx, tenantID y originSessionID. Sin error,
//     devuelve el texto crudo del selector tal cual.
//   - Si el error del selector casa (errors.Is) con el llmvia.ErrViaSinTurnoAcotado NUEVO,
//     devuelve un error con EXACTAMENTE el mismo texto que casa además con el centinela VIEJO:
//     es el que compara turnoacotado (turnoacotado.go y troceado.go) para degradar a
//     modules.MotivoSinResolutor. Sin esa traducción, un tenant que no puede servir el turno
//     recibiría un error en vez de la degradación: cambio de conducta observable (R4.7.c).
//   - Cualquier otro error (el de lectura de la configuración, local.ErrSinTransporte, el del
//     frame con o sin motivo) pasa INTACTO, sin envolver: el aviso al dueño ya se escribió dentro
//     del selector, y turnoacotado no compara ningún otro centinela.
//   - Junto a un error devuelve "".
//
// Vida: nace en F4 · muere en F8, cuando el turnoacotado nuevo reciba el selector nuevo; con él
// muere el fichero, y entonces `inferencia` entra en Conmutados.
type turneroBridge struct {
	// sel es el selector nuevo en el que se delega; el adaptador no guarda más estado.
	sel *llmvia.Selector
}

// turneroBridge es un turnoacotado.Turnero: si la firma cambia, esto no compila.
var _ turnoacotado.Turnero = (*turneroBridge)(nil)

// Turno implementa turnoacotado.Turnero: copia la petición al tipo nuevo y delega (ver
// turneroBridge).
func (b *turneroBridge) Turno(ctx context.Context, tenantID, originSessionID string, t legacyllm.TurnoRequest) (string, error) {
	raw, err := b.sel.Turno(ctx, tenantID, originSessionID, toNewTurnoRequest(t))
	if err != nil {
		return "", translateTurnErr(err)
	}
	return raw, nil
}

// toNewTurnoRequest copia el TurnoRequest viejo en el nuevo campo a campo. Es una función aparte
// para poder afirmar la copia de todos los campos sin un Edge conectado.
func toNewTurnoRequest(t legacyllm.TurnoRequest) llmvia.TurnoRequest {
	return llmvia.TurnoRequest{Prompt: t.Prompt, Formato: t.Formato}
}

// translateTurnErr envuelve en un bridgeError (bridge_contact.go) el error que casa con el
// ErrViaSinTurnoAcotado nuevo, para que case también con el viejo sin cambiar de texto; cualquier
// otro error sale tal cual, porque no hay centinela viejo que turnoacotado compare con él.
func translateTurnErr(err error) error {
	if errors.Is(err, llmvia.ErrViaSinTurnoAcotado) {
		return &bridgeError{original: err, oldSentinel: legacyllm.ErrViaSinTurnoAcotado}
	}
	return err
}
