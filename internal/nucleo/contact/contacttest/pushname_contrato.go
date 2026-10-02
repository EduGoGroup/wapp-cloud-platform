// Caso de push_name: no cambia la identidad (N-04). Qué nombre sobrevive NO se afirma (R-28), a
// propósito: ver Contrato.

package contacttest

import (
	"fmt"
	"testing"
)

// casoPushNameNoCambiaLaIdentidad (N-04): el push_name nunca cambia el contact_id: ni al crear,
// ni en llamadas posteriores con otro nombre, con ninguno o con uno no ASCII, ni al atar una ref
// nueva. Dos contactos distintos con el mismo nombre siguen siendo distintos. La suite no afirma
// qué nombre sobrevive (R-28).
func casoPushNameNoCambiaLaIdentidad(t *testing.T, m Montaje) {
	ana := refTel(t, numeroAna)
	id := resolverConNombre(t, m, m.TenantA, "Ana", ana)

	for _, nombre := range []string{"", "Beto", "Ana", "Ñandú 🙂"} {
		mismoID(t, fmt.Sprintf("la misma ref con push_name %q", nombre), resolverConNombre(t, m, m.TenantA, nombre, ana), id)
	}

	lid := refLID(t, lidAna)
	mismoID(t, "una ref nueva atada con otro push_name", resolverConNombre(t, m, m.TenantA, "Carla", ana, lid), id)
	mismoID(t, "esa ref, sola y sin nombre", resolverOK(t, m, m.TenantA, lid), id)

	otro := resolverConNombre(t, m, m.TenantA, "Ana", refTel(t, numeroBeto))
	distintoID(t, "otra ref con el mismo push_name", otro, id)
}
