// Adaptador de tipos entre la inferencia NUEVA (internal/modulos/inferencia/{llmvia,tenantllm}) y
// los dos consumidores VIEJOS que piden tipos del paquete viejo: internal/turnoacotado (hasta F8)
// e internal/reanalisis (hasta F7). No porta ningún fichero: nace en F4 (T4.10, arquitectura §4
// del plan de F4) para que el arranque nuevo cablee UN solo selector de vía y UN solo almacén de
// tenant_llm, los nuevos, sin tocar a esos dos consumidores (E-1).

package arranque

import (
	"context"
	"errors"

	legacyllm "github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/reanalisis"
	legacytenantllm "github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
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

// llmConfigReader es lo único que llmConfigBridge necesita del almacén nuevo de tenant_llm: leer
// la configuración SIN la credencial. Lo satisface *tenantllm.Postgres, que es lo que cablea el
// arranque (lo afirma el test de cableado, por reflexión); se guarda como puerto y no como el
// tipo concreto para que el test unitario lo ejercite con un doble y sin BD, igual que
// contactBridge. No tiene APIKey a propósito: por este adaptador no puede salir la clave.
type llmConfigReader interface {
	Get(ctx context.Context, tenantID string) (tenantllm.Config, bool, error)
}

// llmConfigBridge presenta el almacén NUEVO de tenant_llm como el reanalisis.ConfigLLM que pide
// la puerta vieja del re-análisis (reanalisis.NewServicio, fase 5). Hace falta porque ConfigLLM
// devuelve el Config del tenantllm VIEJO, y los dos Config son tipos distintos para Go aunque
// tengan los mismos ocho campos.
//
// Delega en store, que es EL MISMO almacén que recibe el selector de vía: la alternativa (un
// tenantllm.Postgres viejo aparte) abriría un segundo camino al SQL de tenant_llm.
//
// Promesas:
//   - Get delega en store.Get con el mismo ctx y tenantID y copia el Config nuevo en uno viejo
//     campo a campo, los ocho, sin reinterpretar ninguno (Via es un string con el mismo
//     vocabulario en los dos paquetes). Devuelve found tal cual: false significa «sin fila», que
//     reanalisis lee como la vía por defecto.
//   - Un error de store pasa INTACTO, sin envolver ni traducir, junto a un Config vacío y found
//     false: reanalisis no compara ningún centinela de tenantllm sobre el error de Get (lo
//     envuelve con su propio texto), y Get no devuelve ErrNotConfigured (ese es de APIKey).
//
// Vida: nace en F4 · muere en F7, cuando el reanalisis nuevo reciba el almacén nuevo.
type llmConfigBridge struct {
	// store es el almacén nuevo en el que se delega; el adaptador no guarda más estado.
	store llmConfigReader
}

// llmConfigBridge es un reanalisis.ConfigLLM: si la firma cambia, esto no compila.
var _ reanalisis.ConfigLLM = (*llmConfigBridge)(nil)

// Get implementa reanalisis.ConfigLLM: delega y copia la configuración al tipo viejo (ver
// llmConfigBridge).
func (b *llmConfigBridge) Get(ctx context.Context, tenantID string) (legacytenantllm.Config, bool, error) {
	cfg, found, err := b.store.Get(ctx, tenantID)
	if err != nil {
		return legacytenantllm.Config{}, false, err
	}
	return toLegacyLLMConfig(cfg), found, nil
}

// toLegacyLLMConfig copia el Config nuevo en el viejo campo a campo. Es una función aparte para
// que el test afirme por reflexión que no se queda ningún campo sin copiar.
func toLegacyLLMConfig(cfg tenantllm.Config) legacytenantllm.Config {
	return legacytenantllm.Config{
		TenantID:    cfg.TenantID,
		Via:         cfg.Via,
		Provider:    cfg.Provider,
		Model:       cfg.Model,
		HasAPIKey:   cfg.HasAPIKey,
		ConsentedAt: cfg.ConsentedAt,
		CreatedAt:   cfg.CreatedAt,
		UpdatedAt:   cfg.UpdatedAt,
	}
}
