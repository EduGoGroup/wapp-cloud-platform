// Porta internal/intakes/aprobadas.go @ 64c181a

// postgres_approved.go es la lectura de las cotizaciones APROBADAS del tenant. En el
// paquete viejo el método vivía en aprobadas.go, junto a su gemelo en memoria; aquí
// nace con el adaptador (D-F6-6 ampliada).

package intakes

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ApprovedRenderedTexts devuelve los textos (rendered_text) de las últimas `limit`
// revisiones RevisionKindApproved del tenant, de la más reciente a la más antigua
// (created_at descendente y revision_no descendente de desempate). Es una consulta
// suelta, acotada por el tenant de la SOLICITUD (las revisiones no llevan tenant).
//
//   - limit <= 0 ⇒ (nil, nil) SIN tocar la base: pedir cero ejemplos es una petición
//     válida, no un error;
//   - limit > MaxApprovedTexts ⇒ se recorta a MaxApprovedTexts en silencio: el
//     llamante no tiene por qué conocer una cota que no es suya;
//   - los textos NULL o EN BLANCO no cuentan. El blanco se mide como
//     strings.TrimSpace —espacio, tabulador, salto de línea, retorno, salto de página
//     y tabulador vertical—, con el conjunto EXPLÍCITO en el SQL: un btrim a secas
//     solo quita espacios y dejaría pasar un texto de espacios y un salto de línea
//     que el doble en memoria descarta;
//   - sin filas ⇒ slice vacío, no nil.
//
// Errores, envueltos con %w y devolviendo (nil, err):
//
//   - "intakes: listar cotizaciones aprobadas del tenant: " — falla la consulta;
//   - "intakes: leer cotización aprobada: " — una fila no se puede escanear;
//   - "intakes: recorrer cotizaciones aprobadas: " — falla el recorrido;
//   - "intakes: cerrar filas de cotizaciones aprobadas: " — falla el cierre cuando
//     lo demás fue bien.
func (p *Postgres) ApprovedRenderedTexts(ctx context.Context, tenantID string, limit int) (out []string, err error) {
	panic(pendiente.Implementar("intakes.Postgres.ApprovedRenderedTexts"))
}
