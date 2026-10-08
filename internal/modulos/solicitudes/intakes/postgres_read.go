// Porta internal/intakes/postgres.go @ 64c181a

// postgres_read.go son las tres LECTURAS del puerto Store: la página de la bandeja,
// el export con líneas y el detalle de una solicitud. Ninguna abre transacción: son
// consultas sueltas sobre el pool.
//
// Lo que comparten List y ListDetails, y que el verde porta con ellas:
//
//   - EL MISMO PREDICADO para la página y para su total. Si divergieran, el paginador
//     mentiría. Son seis argumentos, siempre en este orden: tenant, desde, hasta,
//     estados, sesión, huérfanas; cada filtro ausente viaja como NULL (patrón
//     «$n IS NULL OR …», que deja el plan estable sin SQL dinámico);
//   - el filtro de estados viaja EXPANDIDO a sus variantes almacenadas
//     (StoredVariantsOf): la base puede guardar todavía claves legadas;
//   - el filtro de HUÉRFANAS (Plan 044 · T4.8, REQ-21c) es «su evento declarado ya no
//     está open», y es el mismo predicado con el que Discard decide `live_event`: la
//     vista preselecciona lo que el descarte va a aceptar. Va como subconsulta
//     correlada y NO como LEFT JOIN, que duplicaría cabeceras; una solicitud legada
//     sin evento (event_id NULL) es huérfana;
//   - el ORDER BY lo elige el filtro ya normalizado y sale de dos constantes: más
//     reciente primero (por defecto) o más antigua primero (SortOldest), siempre con
//     el id de desempate. Ningún texto del usuario llega al SQL.
//
// tenant_id NO se lee: quien consulta ya es el dueño del tenant (INV-8).

package intakes

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// List implementa Store.List: la página pedida y el TOTAL de coincidencias sin
// paginar, con el mismo predicado.
//
// El filtro se normaliza primero (Filter.Normalized). Son DOS sentencias sueltas:
// el `count(*)` y, solo si hay coincidencias, la página (LIMIT page_size OFFSET
// (page-1)·page_size). Con total 0 devuelve ([]Intake{}, 0, nil) —slice vacío, no
// nil— sin lanzar la segunda.
//
// Errores, envueltos con %w y devolviendo (nil, 0, err):
//
//   - "intakes: contar solicitudes: " — falla el count;
//   - "intakes: listar solicitudes: " — falla la consulta de la página;
//   - "intakes: leer solicitud: " — una fila no se puede escanear;
//   - "intakes: recorrer solicitudes: " — falla el recorrido de las filas;
//   - "intakes: cerrar filas de solicitudes: " — falla el cierre de las filas cuando
//     lo demás fue bien (un error anterior no se pisa).
func (p *Postgres) List(ctx context.Context, tenantID string, f Filter) (out []Intake, total int, err error) {
	panic(pendiente.Implementar("intakes.Postgres.List"))
}

// ListDetails implementa Store.ListDetails: hasta `limit` solicitudes del filtro CON
// sus líneas, para el export. La paginación del filtro se ignora: el tope es `limit`.
//
// Con limit <= 0 devuelve ([]Detail{}, nil) SIN tocar la base. Si no, es UNA sola
// sentencia (una CTE con la página de cabeceras y un LEFT JOIN a sus líneas), y no
// 1+N: el export puede pedir MaxExportIntakes+1 cabeceras. El adaptador agrupa las
// filas consecutivas de la misma solicitud:
//
//   - las solicitudes salen en el orden del filtro, y las líneas de cada una por
//     added_at y luego id (el orden en que el cliente las añadió);
//   - una solicitud SIN líneas sale con Items vacío, no nil (la fila del LEFT JOIN
//     trae sku, label y qty a NULL y no genera línea);
//   - Revisions y BuyerDataPresent NO se rellenan: el export no los lleva.
//
// Errores, envueltos con %w y devolviendo (nil, err):
//
//   - "intakes: listar solicitudes con líneas: " — falla la consulta;
//   - "intakes: leer fila del export: " — una fila no se puede escanear;
//   - "intakes: recorrer el export: " — falla el recorrido;
//   - "intakes: cerrar filas del export: " — falla el cierre cuando lo demás fue bien.
func (p *Postgres) ListDetails(ctx context.Context, tenantID string, f Filter, limit int) (out []Detail, err error) {
	panic(pendiente.Implementar("intakes.Postgres.ListDetails"))
}

// Get implementa Store.Get: la cabecera, sus líneas, sus revisiones y si tiene datos
// del comprador.
//
// Son cuatro lecturas sueltas, en este orden y SIN transacción: cabecera (acotada por
// tenant), líneas (por added_at, id), revisiones (por revision_no) y la existencia de
// la fila de public.intake_buyer_data. De los datos del comprador solo se pregunta SI
// EXISTEN: el contenido cifrado no se lee aquí.
//
// Devuelve ErrNotFound —sin envolver— si intakeID no es un UUID (sin tocar la base) o
// si la cabecera no existe en ese tenant; en ese caso no lanza las otras lecturas.
// Items y Revisions salen vacíos, no nil, cuando no hay filas.
//
// 🔴 LEER REVISIONES PUEDE ESCRIBIR. Si una revisión trae literal de nivel 2 y su TTL
// ya venció, Get la PODA al vuelo (ver postgres_revisions.go): la revisión sale sin
// literal y con LiteralPrunedAt puesto, y la poda queda registrada por el logger de
// retención. Un fallo de la poda no hace fallar a Get.
//
// Errores de la base, envueltos con %w y devolviendo Detail{}:
//
//   - "intakes: leer solicitud: " — la cabecera no se puede leer;
//   - "intakes: listar líneas: ", "intakes: leer línea: ", "intakes: recorrer líneas: ",
//     "intakes: cerrar filas de líneas: " — las líneas;
//   - los de la lectura de revisiones, que documenta postgres_revisions.go;
//   - "intakes: comprobar datos del comprador: " — la última lectura.
func (p *Postgres) Get(ctx context.Context, tenantID, intakeID string) (Detail, error) {
	panic(pendiente.Implementar("intakes.Postgres.Get"))
}
