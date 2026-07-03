package content

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// Store es la dependencia que el adapter JSON consume para leer el blob
// de contenido crudo (JSONB) por tenant y referencia. Su respaldo Postgres real
// llega en T2; aquí (T1) se testea con un fake en memoria.
//
// GetTenantContent devuelve el blob JSON crudo asociado a (tenantID, ref), o un
// error si la referencia no existe / falla el almacén.
type Store interface {
	GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error)
}

// JSON es el adapter de contenido dinámico: lee un blob por-tenant a través del
// Store y lo deserializa al contrato model.Content. Exige que el nodo
// declare una referencia (Node.Content.Ref).
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

// Resolve lee Node.Content.Ref del almacén y lo deserializa al contrato. Errores
// controlados (nunca pánico), todos envueltos sobre model.ErrInvalidFlow:
//   - el nodo no declara `content` (ref ausente): el adapter json exige ref;
//   - el almacén falla o la ref no existe;
//   - el blob no es JSON válido para el contrato.
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
