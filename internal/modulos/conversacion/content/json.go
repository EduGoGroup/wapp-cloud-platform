// Porta internal/flujos/content/json.go @ c0c0c03

package content

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// Store es la dependencia que el adapter JSON consume para leer el blob de
// contenido crudo (JSONB) por tenant y referencia.
//
// GetTenantContent devuelve el blob JSON crudo asociado a (tenantID, ref), o un
// error si la referencia no existe / falla el almacén.
type Store interface {
	GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error)
}

// JSON es el adapter de contenido dinámico: lee un blob por-tenant a través del
// Store y lo deserializa al contrato model.Content. Exige que el nodo declare
// una referencia (Node.Content.Ref).
type JSON struct {
	store Store
}

// NewJSON construye el adapter JSON sobre el Store dado.
func NewJSON(store Store) *JSON { return &JSON{store: store} }

// contentBlob es la forma esperada del blob JSONB por-tenant. Todos los campos
// son opcionales: `items` para listas de catálogo (pedido); `prompt`/`options`
// para nodos interactivos cuyo contenido vive fuera de la definición.
type contentBlob struct {
	Prompt  string              `json:"prompt"`
	Options map[string]string   `json:"options"`
	Items   []model.ContentItem `json:"items"`
}

// Resolve lee Node.Content.Ref del almacén (con el tenantID y la ref tal cual) y
// lo deserializa al contrato: Prompt, Options e Items tipados, y el blob entero
// en Raw. Errores controlados (nunca pánico), todos envueltos sobre
// model.ErrInvalidFlow:
//   - el nodo no declara `content`: «el adapter json exige node.content con una ref»;
//   - la ref está vacía: «el adapter json exige una ref no vacía»;
//   - el almacén falla o la ref no existe: «leer contenido %q del tenant», que
//     envuelve además el error del almacén;
//   - el blob no es JSON válido para el contrato: «blob de contenido %q mal
//     formado», que envuelve además el error de encoding/json.
//
// En los dos primeros casos no se llega a consultar el almacén.
func (j *JSON) Resolve(ctx context.Context, tenantID string, node model.Node) (model.Content, error) {
	if node.Content == nil {
		return model.Content{}, fmt.Errorf("%w: el adapter json exige node.content con una ref", model.ErrInvalidFlow)
	}
	ref := node.Content.Ref
	if ref == "" {
		return model.Content{}, fmt.Errorf("%w: el adapter json exige una ref no vacía", model.ErrInvalidFlow)
	}

	raw, err := j.store.GetTenantContent(ctx, tenantID, ref)
	if err != nil {
		return model.Content{}, fmt.Errorf("%w: leer contenido %q del tenant: %w", model.ErrInvalidFlow, ref, err)
	}

	var blob contentBlob
	if err := json.Unmarshal(raw, &blob); err != nil {
		return model.Content{}, fmt.Errorf("%w: blob de contenido %q mal formado: %w", model.ErrInvalidFlow, ref, err)
	}

	// Además de los campos tipados (Prompt/Options/Items), volcamos el blob crudo
	// completo en Content.Raw (map[string]any). Extensión ADITIVA y retro-compatible
	// (Plan 016 §3.1): menú/encuesta no leen Raw y siguen idénticos; los módulos que
	// necesitan estructura propia del dominio (p. ej. el carrito parsea su árbol de
	// catálogo desde Raw) la decodifican ahí. Si el unmarshal a map falla, Raw queda
	// nil sin abortar la resolución de los campos tipados.
	var rawMap map[string]any
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		rawMap = nil
	}

	return model.Content{
		Prompt:  blob.Prompt,
		Options: blob.Options,
		Items:   blob.Items,
		Raw:     rawMap,
	}, nil
}
