package events

import (
	"context"
	"fmt"
	"slices"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// RuleLister es la parte de trigger.Store que necesita TriggerKindOffer. Se
// declara aquí, estrecha, para que el despachador dependa de UNA lectura y no de
// todo el CRUD de reglas: lo satisfacen tanto el store Postgres como el de
// memoria de los tests.
type RuleLister interface {
	ListByKind(ctx context.Context, tenantID, sessionID string, k trigger.Kind) ([]trigger.Rule, error)
}

// TriggerKindOffer responde «qué tipos ofrece este tenant» leyendo sus reglas
// kind='event_start' (T2.1).
//
// Por qué esa es la fuente y no la lista de módulos compilados ni los flujos
// publicados: un tipo está OFRECIDO cuando el tenant le ha puesto una palabra
// con la que pedirlo. Un módulo que existe en el binario pero que este tenant no
// ha configurado no es algo que se le pueda ofrecer a su cliente, y un flujo
// publicado sin disparo no tiene por dónde arrancarse.
type TriggerKindOffer struct {
	rules RuleLister
}

// NewTriggerKindOffer construye el adapter sobre el store de reglas.
func NewTriggerKindOffer(rules RuleLister) *TriggerKindOffer {
	return &TriggerKindOffer{rules: rules}
}

// OfferedKinds devuelve los tipos de evento distintos que el tenant ofrece en
// esa sesión, en orden alfabético y sin repetir.
//
// Descarta las reglas DESHABILITADAS (ListByKind no filtra por enabled: eso es
// del llamante) y las que no dicen qué tipo paren. Devuelve tipos, no reglas:
// dos palabras distintas para el mismo carrito son UNA opción del menú, no dos.
//
// El orden es alfabético por ser determinista y no significar nada más. Si
// algún día el menú debe ordenarse por otra cosa, la política va aquí —en la
// oferta— y el despachador no se entera: Build respeta el orden que reciba.
func (o *TriggerKindOffer) OfferedKinds(ctx context.Context, tenantID, sessionID string) ([]string, error) {
	rules, err := o.rules.ListByKind(ctx, tenantID, sessionID, trigger.KindEventStart)
	if err != nil {
		return nil, fmt.Errorf("events: leer las reglas event_start del tenant: %w", err)
	}

	kinds := make([]string, 0, len(rules))
	for _, r := range rules {
		if !r.Enabled || r.EventKind == "" {
			continue
		}
		if !slices.Contains(kinds, r.EventKind) {
			kinds = append(kinds, r.EventKind)
		}
	}
	slices.Sort(kinds)
	return kinds, nil
}
