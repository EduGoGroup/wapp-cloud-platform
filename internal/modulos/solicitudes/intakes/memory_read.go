// Porta internal/intakes/memory.go @ 64c181a (las lecturas) y el método
// (*MemoryStore).ApprovedRenderedTexts de internal/intakes/aprobadas.go @ 64c181a.

package intakes

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// List implementa Store. El filtro se NORMALIZA dentro (Filter.Normalized), así que
// llega crudo. Devuelve la página pedida y el TOTAL de coincidencias sin paginar; una
// página más allá del final es un slice vacío NO nil con el total intacto. Nunca
// devuelve error.
//
// El predicado es el del store real, cláusula por cláusula:
//
//   - solo el tenant pedido (INV-8): un tenant ajeno o desconocido no ve nada;
//   - rango por CreatedAt con From INCLUSIVO y To EXCLUSIVO;
//   - estados: las variantes almacenadas de CADA estado del filtro (StoredVariantsOf),
//     no solo del primero — filtrar por `confirmed` alcanza las filas `closed`;
//   - sesión exacta;
//   - Orphan: fuera las que declaran un evento que sigue `open`. La fila sin ligadura
//     (legada pre-0054) y la que declara un evento que no existe son huérfanas. Es el
//     MISMO criterio que la guarda `live_event` de Discard, negado.
//
// El orden lo dice f.Sort: SortNewest (por defecto) es CreatedAt descendente y
// SortOldest ascendente; el id desempata EN EL MISMO SENTIDO que la fecha. Cada
// cabecera sale con el Status normalizado y el resto de sus campos tal como están
// guardados.
func (m *MemoryStore) List(_ context.Context, tenantID string, f Filter) ([]Intake, int, error) {
	panic(pendiente.Implementar("intakes.MemoryStore.List"))
}

// ListDetails implementa Store: el MISMO predicado y el MISMO orden que List —si
// divergieran, el export no contendría lo que la bandeja muestra—, sin paginar (Page y
// PageSize se IGNORAN: PageSize se acota a MaxPageSize y la cota del export es otra) y
// cortado a `limit` CABECERAS, cada una con todas sus líneas en orden de alta.
//
// Una solicitud sin líneas sale con Items vacío NO nil. Revisions va vacío y
// BuyerDataPresent en false SIEMPRE: ni el export ni el summary publican nada de eso.
// Con `limit` ≤ 0 devuelve un slice vacío no nil. Nunca devuelve error. Las líneas son
// una copia del llamante.
func (m *MemoryStore) ListDetails(_ context.Context, tenantID string, f Filter, limit int) ([]Detail, error) {
	panic(pendiente.Implementar("intakes.MemoryStore.ListDetails"))
}

// Get implementa Store: la cabecera con el Status normalizado, sus líneas en orden de
// alta, sus revisiones por número ascendente y BuyerDataPresent (true si la solicitud
// tiene algún dato del comprador guardado). ErrNotFound si no hay solicitud con ese id
// EN ESE TENANT: inexistente y «de otro tenant» son indistinguibles (404 opaco).
//
// Las revisiones salen como en Revisions: con el literal devuelto a su sitio, y
// podando lo vencido (ver allí). Líneas y revisiones son copias del llamante.
func (m *MemoryStore) Get(_ context.Context, tenantID, intakeID string) (Detail, error) {
	panic(pendiente.Implementar("intakes.MemoryStore.Get"))
}

// Revisions devuelve las revisiones de una solicitud en orden de escritura, TAL COMO
// LAS LEE EL DUEÑO: es la misma lectura que hace Get, sin pasar por el tenant. Mirador
// de tests; no es parte de ningún puerto. Una solicitud sin revisiones (o desconocida)
// da un slice vacío.
//
// Es una lectura CON EFECTOS, los de la retención del literal (T3.5; reglas de los
// tests viejos de retención y del sello de la poda, R-07):
//
//   - una revisión con literal vigente lo trae FUNDIDO en su Payload (MergeLiteral),
//     sobre una copia: dos lecturas seguidas no acumulan;
//   - una revisión cuyo literal venció (LiteralExpired con la edad medida por el reloj
//     del store y el TTL de SetLiteralTTL) se PODA en esta lectura: el literal se
//     destruye, la interpretación estructurada queda intacta, LiteralPrunedAt se sella
//     con el instante del reloj —y esta misma lectura ya lo publica— y se loguea por el
//     logger de retención, en nivel Info, el mensaje
//     "retención: literal de la revisión podado por TTL vencido" con las claves
//     intake_id, revision_no, edad_segundos y ttl_segundos y CERO contenido;
//   - una ya podada conserva su instante REAL: releerla no lo mueve ni vuelve a
//     anunciar la poda;
//   - se sella UNA revisión, no la solicitud: las hermanas no se enteran;
//   - una revisión que nunca tuvo literal (las del carrito) no se sella ni se anuncia,
//     por vieja que sea;
//   - con TTL 0 no se poda nunca.
//
// Si el literal no se puede fundir, se loguea en nivel Error
// ("retención: no se pudo devolver el literal a la revisión") y la revisión sale con su
// interpretación sola: una lectura de la bandeja no se cae por eso.
func (m *MemoryStore) Revisions(intakeID string) []Revision {
	panic(pendiente.Implementar("intakes.MemoryStore.Revisions"))
}

// PersistedRevisions devuelve las revisiones TAL COMO ESTÁN GUARDADAS: sin poda, sin
// devolverle el literal al payload y sin efectos. Es el equivalente en este doble a
// mirar la tabla con SQL directo, y existe para lo mismo: comprobar que lo que se
// escribió NO lleva literal del cliente. Devuelve una copia. No es la lectura de
// nadie: para leer de verdad están Get y Revisions. Era `RevisionesPersistidas` en el
// paquete viejo.
func (m *MemoryStore) PersistedRevisions(intakeID string) []Revision {
	panic(pendiente.Implementar("intakes.MemoryStore.PersistedRevisions"))
}

// ShippingZones devuelve las zonas de envío sembradas del tenant, el mismo puerto de
// lectura que tiene *Postgres (lo consume la etapa `match` del pipeline). Un tenant
// sin zonas devuelve nil sin error, igual que un tenant sin fila en tenant_settings.
// El slice es una copia del llamante. Nunca devuelve error.
func (m *MemoryStore) ShippingZones(_ context.Context, tenantID string) ([]ShippingZone, error) {
	panic(pendiente.Implementar("intakes.MemoryStore.ShippingZones"))
}

// NotifySettings implementa SettingsReader. Un tenant sin sembrar devuelve la
// configuración de arranque —sin plantilla y con DefaultDepositDueDays—, igual que el
// store Postgres cuando no hay fila en tenant_settings; uno sembrado con un plazo ≤ 0
// devuelve su plantilla con el plazo por defecto. Nunca devuelve error.
func (m *MemoryStore) NotifySettings(_ context.Context, tenantID string) (NotifySettings, error) {
	panic(pendiente.Implementar("intakes.MemoryStore.NotifySettings"))
}

// ApprovedRenderedTexts es el espejo en memoria de la consulta del historial aprobado
// (aprobadas.go): los RenderedText de las revisiones de clase `approved` de las
// solicitudes DEL TENANT, de la más reciente a la más antigua (CreatedAt descendente y,
// de desempate, RevisionNo descendente), hasta `limit`.
//
// No cuentan —ni gastan cupo— las de otra clase ni las de texto vacío o solo de
// blancos (espacio, tabulador, salto de línea, retorno, salto de página, tabulador
// vertical: el conjunto que el SQL real recorta explícitamente; un texto con solo esos
// caracteres dejaba pasar en Postgres lo que Go descartaba, y lo cazó el test de
// integración viejo). El texto que cuenta se devuelve SIN recortar.
//
// `limit` ≤ 0 devuelve (nil, nil): pedir cero ejemplos es una petición válida. Por
// encima de MaxApprovedTexts se recorta a esa cota en silencio. Sin candidatas
// devuelve un slice vacío. Nunca devuelve error.
func (m *MemoryStore) ApprovedRenderedTexts(_ context.Context, tenantID string, limit int) ([]string, error) {
	panic(pendiente.Implementar("intakes.MemoryStore.ApprovedRenderedTexts"))
}

// StoredStatus devuelve la clave de estado de la solicitud TAL COMO ESTÁ GUARDADA, sin
// normalizar (`closed` sale `closed`), y "" si la solicitud no es de ese tenant. Sin
// efectos. Mirador de tests; no es parte de ningún puerto.
//
// NUEVO: no existía en el paquete viejo. Lo pide la suite de contrato, cuya marca de
// estado vigila la fila entera (hallazgo 35 de F1): todas las lecturas del puerto
// normalizan, y sin esto no se distingue una escritura que guarda la clave pedida de
// una que la normaliza antes de guardar. Contra Postgres es un SELECT de la columna.
func (m *MemoryStore) StoredStatus(tenantID, intakeID string) string {
	panic(pendiente.Implementar("intakes.MemoryStore.StoredStatus"))
}

// BuyerDataOf devuelve una COPIA del checklist del comprador guardado para la
// solicitud (vacío, no nil, si no tiene nada). Mirador de tests: el store real no
// publica ninguna lectura en claro, su descifrado está custodiado (buyerdata.go).
func (m *MemoryStore) BuyerDataOf(intakeID string) BuyerData {
	panic(pendiente.Implementar("intakes.MemoryStore.BuyerDataOf"))
}
