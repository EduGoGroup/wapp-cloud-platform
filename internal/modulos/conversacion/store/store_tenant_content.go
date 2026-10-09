// Porta internal/flujos/store/store.go @ c0c0c03
//
// Trozo de store.go (05 E-13): el contenido de negocio por tenant (tenant_content) y
// su versionado (tenant_content_versions). Solo declaraciones movidas.
//
// El auxiliar no exportado que valida la procedencia contra el conjunto cerrado
// (validVersionSource en el viejo) nace con el verde de los adaptadores, que son
// quienes lo usan: en el rojo no puede existir (lint `unused`).

package store

import (
	"context"
	"errors"
	"time"
)

// TenantContentReader lee el contenido de negocio por-tenant (Plan 015 · T2).
type TenantContentReader interface {
	// GetTenantContent devuelve el blob JSON crudo de contenido de negocio para
	// (tenantID, ref) de public.tenant_content (Plan 015 · T2). Su firma coincide
	// EXACTAMENTE con content.Store (structural typing): el PostgresRepository lo
	// satisface. Devuelve ErrTenantContentNotFound si la ref no existe.
	GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error)
}

// TenantContentSummary es la cabecera de un blob de contenido por-tenant
// (public.tenant_content, Plan 018 · T6): su ref lógica y las marcas de tiempo. NO
// incluye el blob (se obtiene con GetTenantContent). La devuelve ListTenantContent
// para alimentar el listado de la API pública (GET /api/v1/tenant-content). CERO
// PII: ref es un identificador de negocio (ADR-0009).
type TenantContentSummary struct {
	Ref       string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Procedencias válidas de una fila de public.tenant_content_versions (D-041.8).
// Describen el ACTO que archivó la versión, no el blob archivado —la de este
// último no la registró nadie cuando se escribió—. Espejan el CHECK de la
// migración 0044: si aquí se admitiera una más, Postgres la rechazaría y el
// repositorio en memoria no, y los tests dejarían de decir la verdad.
const (
	// VersionSourceImportJSON: la desplazó un POST /api/v1/catalog/import con
	// documento JSON (Plan 041 · T3.3).
	VersionSourceImportJSON = "import_json"
	// VersionSourceImportTabular: la desplazó el import de planilla CSV/XLSX
	// (Plan 041 · T3.4).
	VersionSourceImportTabular = "import_tabular"
	// VersionSourceManual: la desplazó una escritura a mano del contenido. HOY NO
	// SE EMITE: el PUT /api/v1/tenant-content genérico no versiona (follow-up
	// declarado en D-041.8). Existe para que ese follow-up no tenga que migrar el
	// CHECK.
	VersionSourceManual = "manual"
)

// ErrInvalidVersionSource lo devuelve ReplaceTenantContentVersioned cuando la
// procedencia no es una de las tres válidas. Es un error de PROGRAMACIÓN (la
// procedencia la elige el código, nunca el usuario), así que se rechaza antes de
// tocar la BD en vez de dejar que reviente el CHECK.
var ErrInvalidVersionSource = errors.New("procedencia de versión de contenido inválida")

// TenantContentVersion es UNA fila archivada de public.tenant_content_versions:
// el blob que estaba vigente ANTES del import que la creó. Content es el blob
// VIEJO (no el que quedó), Source la procedencia del acto que lo desplazó. La
// devuelve el repositorio en memoria para que los tests puedan afirmar QUÉ se
// archivó; el camino Postgres la escribe y hoy nadie la relee (la restauración es
// follow-up de D-041.8). CERO PII: aquí solo hay catálogo.
type TenantContentVersion struct {
	Version   int
	Content   []byte
	Source    string
	CreatedAt time.Time
}

// TenantContentVersioner archiva el contenido vigente y escribe el nuevo en UN
// SOLO ACTO (Plan 041 · T3.3, D-041.8). Lo satisfacen *PostgresRepository y
// *MemoryRepository.
//
// POR QUÉ UN MÉTODO Y NO DOS LLAMADAS DEL LLAMANTE: archivar y escribir por
// separado deja una ventana en la que el proceso se cae entre medias y queda o
// una versión de algo que sigue vigente, o un catálogo nuevo sin rastro del que
// sustituyó. Con dos operaciones no hay orden que salve las dos: el único
// arreglo es que sean una.
type TenantContentVersioner interface {
	// ReplaceTenantContentVersioned archiva el blob VIGENTE de (tenantID, ref)
	// como la siguiente versión y escribe blob en su lugar, todo en la misma
	// transacción. source debe ser una de las constantes VersionSource*.
	//
	// Devuelve el número de versión ARCHIVADA, o 0 si no había blob vigente que
	// archivar: el primer import sobre una ref vacía escribe y no versiona
	// (D-041.8; «no hay versiones» y «no hay contenido» son casos distintos).
	ReplaceTenantContentVersioned(ctx context.Context, tenantID, ref string, blob []byte, source string) (archived int, err error)
}

// ErrTenantContentNotFound lo devuelve GetTenantContent cuando no existe blob de
// contenido para (tenant_id, ref) en tenant_content. Se inspecciona con errors.Is.
var ErrTenantContentNotFound = errors.New("contenido de tenant no encontrado")
