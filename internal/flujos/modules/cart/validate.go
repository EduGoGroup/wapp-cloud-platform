package cart

import (
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// sourceJSON es la fuente de catálogo que resuelve el blob desde tenant_content
// (model.ContentRef.Source == "json"): a diferencia de la estática inline, exige
// una ref (la clave del blob). Se replica como literal para no acoplar el módulo al
// paquete content (mismo criterio que el resto de literales de contrato del cart).
const sourceJSON = "json"

// ErrInvalidCartNode es el error base de un nodo cart estructuralmente inválido en
// el alta admin. Se inspecciona con errors.Is; el Registry lo envuelve con el id.
var ErrInvalidCartNode = errors.New("nodo cart inválido")

// ValidateNode valida la ESTRUCTURA de un nodo cart en el alta admin (Plan 027 ·
// Ola 1 · T6, cierra H11), SIMÉTRICA con la validación de options de menú/encuesta:
// un nodo cart DEBE declarar una fuente de catálogo resoluble (content.source). Sin
// ella, en runtime el carrito siempre mostraría "catálogo no disponible" y degradaría
// frente al usuario real SIN que el alta avisara (antes el tipo "cart" se aceptaba
// laxo). Para la fuente "json" (catálogo en tenant_content) exige además la ref.
// Implementa modules.NodeValidator (capacidad opcional; se consulta por aserción).
func (Module) ValidateNode(n model.Node) error {
	if n.Content == nil || n.Content.Source == "" {
		return fmt.Errorf("%w: sin content.source (el catálogo no sería resoluble en runtime)", ErrInvalidCartNode)
	}
	if n.Content.Source == sourceJSON && n.Content.Ref == "" {
		return fmt.Errorf("%w: content.source %q sin ref (falta la clave del catálogo en tenant_content)", ErrInvalidCartNode, sourceJSON)
	}
	return nil
}
