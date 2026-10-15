//go:build laboratorio

package common

import (
	"context"
	"fmt"

	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// AmberIndex construye el índice del match con el catálogo Ámbar, por el MISMO camino que la caché
// de producción (indice.Cache): el árbol del catálogo —lo que vive en tenant_content bajo la ref
// «catalogo», o sea la clave "catalog" del contrato de import— pasa por catalogo.ParseCatalog y luego
// por indice.Construir con textmatch.Normalize, el normalizador que la cascada del match exige.
//
// Lo único que se salta es la lectura de tenant_content y la invalidación por contenido: el
// catálogo del laboratorio no cambia.
func AmberIndex() (*indice.Indice, error) {
	tree, ok := AmberCatalog()["catalog"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("common: el catálogo Ámbar no trae la clave catalog")
	}
	cat, err := catalogo.ParseCatalog(model.Content{Raw: tree})
	if err != nil {
		return nil, fmt.Errorf("common: parsear el catálogo Ámbar: %w", err)
	}
	idx, err := indice.Construir(cat, textmatch.Normalize)
	if err != nil {
		return nil, fmt.Errorf("common: indexar el catálogo Ámbar: %w", err)
	}
	return idx, nil
}

// StaticCatalog sirve SIEMPRE el mismo índice, para cualquier tenant. Satisface el puerto
// pipeline.Catalogs (Obtener) que en producción cubre indice.Cache.
type StaticCatalog struct{ Index *indice.Indice }

// Obtener implementa pipeline.Catalogs.
func (c StaticCatalog) Obtener(context.Context, string) (*indice.Indice, error) {
	if c.Index == nil {
		return nil, fmt.Errorf("common: StaticCatalog sin índice")
	}
	return c.Index, nil
}
