// Porta internal/intake/pipeline/memoria.go @ 56097aa (su mitad CatalogoEnMemoria; D-F7-5)

// Package pipelinehelpertest son los DOBLES del worker del pipeline que necesitan paquetes
// de test que no se pueden ver entre sí. Hoy es uno: el doble en memoria del puerto
// `pipeline.Catalogs`. El doble de la máquina (`intake.PipelineStore`) NO vive aquí: se
// portó a `intake/intakehelpertest` como MachineMemory (D-F7-5).
//
// Es un doble de test, no producción (D-F7-3): nada fuera de un `_test.go` lo importa.
package pipelinehelpertest

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
)

// ════════════════════════════════════════════════════════════════════════════
// EL DOBLE EN MEMORIA DE `pipeline.Catalogs` (T3.8)
// ════════════════════════════════════════════════════════════════════════════
//
// # POR QUÉ ES CÓDIGO EXPORTADO Y NO UN FAKE EN UN `_test.go`
//
// Lo necesitan dos paquetes de test que no se pueden ver entre sí: el del worker del
// pipeline y el del runtime de flujos (el criterio INV-10).
//
// 🔴 Y HAY UNA FRONTERA QUE NO SE PUEDE CRUZAR NI EN UN TEST: ningún fichero del turno
// conversacional —de producción o de test— puede importar el índice del catálogo, porque
// el índice vive en el worker del pipeline y llevarlo al turno devuelve el parseo del
// catálogo al camino del entrante (INV-02/T1.5). Un test de flujos que quisiera fabricarse
// su propio `Catalogs` tendría que nombrar `*indice.Indice` en la firma y cruzaría esa
// frontera. Con el doble aquí, allí solo se USA un valor cuyo tipo no hace falta nombrar.
//
// No importa el paquete `pipeline` a propósito: satisface su puerto de forma estructural,
// y así los tests del propio `pipeline` pueden usarlo sin ciclo de imports.

// CatalogMemory (antes `pipeline.CatalogoEnMemoria`) es el doble del puerto
// `pipeline.Catalogs`. Es seguro para uso concurrente.
type CatalogMemory struct {
	mu    sync.Mutex
	idx   *indice.Indice
	err   error
	reads int
}

// NewCatalogMemory (antes `NuevoCatalogoEnMemoria`) construye el doble con UN artículo por
// etiqueta, todos en una sola categoría («Catálogo», código "1"). El artículo de la
// etiqueta i-ésima (desde 1) lleva código "i", SKU "ART-i" y precio 1000.
//
// 🔴 INDEXA CON EL NORMALIZADOR DE PRODUCCIÓN (`textmatch.Normalize`) y no con uno de
// laboratorio: el índice y la cascada del match tienen que opinar lo mismo sobre la ñ y
// sobre los acentos, y un doble con `strings.ToLower` haría que «Café» dejara de casar
// «cafe» solo dentro de los tests.
//
// Sin etiquetas sirve un catálogo VACÍO, que es un estado legítimo —el tenant que aún no
// ha subido el suyo— y con el que todo ítem sale `unmatched`.
//
// Si el índice no se puede construir devuelve `pipeline: catálogo en memoria: %w` y doble
// nil.
func NewCatalogMemory(labels ...string) (*CatalogMemory, error) {
	articles := make([]catalogo.Article, 0, len(labels))
	for i, label := range labels {
		articles = append(articles, catalogo.Article{
			Code:  strconv.Itoa(i + 1),
			SKU:   "ART-" + strconv.Itoa(i+1),
			Label: label,
			Price: 1000,
		})
	}
	idx, err := indice.Construir(catalogo.Catalog{Categories: []catalogo.Category{
		{Code: "1", Label: "Catálogo", Items: articles},
	}}, textmatch.Normalize)
	if err != nil {
		return nil, fmt.Errorf("pipeline: catálogo en memoria: %w", err)
	}
	return &CatalogMemory{idx: idx}, nil
}

// BreakRead (antes `RomperLaLectura`) hace que Obtener falle con `err` a partir de ahora;
// nil lo repara. Es el «tenant_content no contesta», que sin esto sería inalcanzable desde
// un test.
func (c *CatalogMemory) BreakRead(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

// Obtener implementa `pipeline.Catalogs`: devuelve SIEMPRE el mismo índice, sea cual sea
// el tenant, o el error de BreakRead (con índice nil). Toda llamada cuenta en Reads, falle
// o no.
func (c *CatalogMemory) Obtener(_ context.Context, _ string) (*indice.Indice, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	if c.err != nil {
		return nil, c.err
	}
	return c.idx, nil
}

// Reads (antes `Lecturas`) es cuántas veces se pidió el índice. Es lo que permite afirmar
// desde fuera el criterio (a) de T3.7: UNA lectura por job, no una por ítem.
func (c *CatalogMemory) Reads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}
