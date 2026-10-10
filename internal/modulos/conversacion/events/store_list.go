// Porta internal/flujos/events/store.go @ 9d5a4b6 (trozo: rescatables y listado del dueño; ver el
// reparto en store.go).
//
// Lo que este trozo lleva en el verde, además de sus cuatro exportados: LA consulta de rescatables
// del sistema (D-043.15), en piezas con nombre —el origen con sus dos LEFT JOIN (la vista
// public.event_content y public.tenant_settings), las dos mitades del contenido (`c.event_id IS
// NULL` y `c.state = 'alive'`), la marca derivada «vencido» como columna CALCULADA y el orden—, y
// el listado del dueño, que reusa esas mismas piezas. Reglas de esa consulta que los contratos de
// abajo prometen:
//
//   - INV-17: un evento cuyo contenido murió (`discarded`) o cuajó (`settled`) no se lista, no se
//     rescata y no se menciona;
//   - INV-19: ningún WHERE compara fechas. «Vencido» es una MARCA que informa, calculada en el
//     SELECT con el instante del reloj inyectado; el filtro `Stale` del listado se aplica sobre esa
//     columna ya calculada;
//   - el TTL de inactividad es el del tenant (tenant_settings.event_inactivity_ttl_seconds) y, sin
//     fila, 7200 s (2 h), el mismo default que la columna; 0 es «sin vencimiento», no «cero
//     segundos».

package events

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ListRescuable devuelve los eventos que se le pueden ofrecer al contacto para
// retomarlos, ordenados por ÚLTIMA ACTIVIDAD descendente y marcados con «vencido».
//
// No es ListAlive con otro ORDER BY, aunque lo pareciera antes de T3.6: filtra
// además por el contenido (INV-17). Son dos preguntas distintas —«¿qué
// tiene abierto?» y «¿qué le ofrezco retomar?»— y desde INV-17 tienen respuestas
// distintas: un carrito cuyo pedido descartó el dueño sigue vivo pero ya no se
// ofrece.
//
// limit <= 0 ⇒ sin tope. Un evento suspendido sigue aquí, marcado: suspendido no es
// muerto (E-6), y por eso la marca va en el SELECT y no en el WHERE (INV-19).
//
// Cada Rescuable trae, además del evento, Stale (la marca) y el contenido DERIVADO de la vista:
// ContentState ("" sin contenido, o "alive") y ContentRef ("" o el id del contenido). Desempate
// del orden: id ascendente. Acota por (tenant, sesión, contacto) y status = 'open'.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "events: listar eventos rescatables: %w" si la consulta falla;
//   - "events: leer fila de evento: %w", "events: recorrer eventos vivos: %w" y
//     "events: cerrar filas de eventos: %w", como ListAlive (el segundo texto dice «vivos» también
//     aquí: se porta tal cual).
func (s *Store) ListRescuable(ctx context.Context, tenantID, sessionID, contactID string, limit int) ([]Rescuable, error) {
	panic(pendiente.Implementar("events.Store.ListRescuable"))
}

// ── Los tipos que el tenant PUEDE ver (decisión de Jhoan, 2026-08-09) ────────
//
// Las dos funciones de aquí no tienen tabla propia ni criterio propio: leen el
// MISMO mapa tipo → feature con el que el despachador arma el menú del cliente (T2.3).
// Que las dos superficies discrepen —que el menú ofrezca «encuesta» y la bandeja
// del dueño no enseñe sus encuestas, o al revés— sería peor que cualquiera de las
// dos políticas por separado, y con dos mapas acabaría pasando.

// KindFeatures devuelve, en orden alfabético y sin repetir, las features que
// habilitan ALGÚN tipo de evento. Es la lista con la que se gatea el listado del
// dueño: basta UNA para entrar (entitlements.RequireAnyFeature).
//
// El orden es alfabético por ser determinista y no significar nada más: el gate
// pasa con cualquiera, así que un orden con intención sería inventarse una
// prioridad que no existe.
//
// Hoy son cuatro tipos y cuatro features: menu → entitlements.FeatureMenu, cart →
// entitlements.FeatureCartBasic, survey → entitlements.FeatureSurvey, media →
// entitlements.FeatureMedia. Cada llamada devuelve un slice NUEVO.
func KindFeatures() []string {
	panic(pendiente.Implementar("events.KindFeatures"))
}

// AllowedKinds devuelve los tipos de evento que ESTE tenant tiene habilitados, en
// orden alfabético. Es lo que acota el CONTENIDO del listado: pasar el gate por
// tener una feature no da derecho a ver los tipos de las otras.
//
// Consecuencia viva y DESEADA: si un tenant pierde una feature, sus eventos de ese
// tipo dejan de aparecer en la bandeja. No se borra nada —las filas siguen ahí,
// intactas— y vuelven a listarse en cuanto la recupere. Es la misma regla que ya
// gobierna el menú que ve el cliente, y tenerla en un solo sitio es justo lo que
// impide que las dos pantallas cuenten historias distintas.
//
// Un error del resolver se PROPAGA, y aquí sí se separa del gate: aquel decide el
// ACCESO y falla cerrado con un 403 (mejor negar que abrir por un fallo
// transitorio); este decide el CONTENIDO, y devolver silenciosamente una lista
// recortada le diría a quien limpia «ya no queda nada» cuando la verdad es «no
// pude mirar». Eso se contesta con un 5xx, no con una bandeja vacía.
//
// Hace un Has por tipo. Sin ninguna feature devuelve la lista vacía (no nil) y sin error.
//
// Textos de error (literales):
//   - "events: sin resolver de features no se puede saber qué tipos ve el tenant" si feats es nil;
//   - "events: resolver la feature %q del tenant: %w" si el resolver falla.
func AllowedKinds(ctx context.Context, feats entitlements.Resolver, tenantID string) ([]string, error) {
	panic(pendiente.Implementar("events.AllowedKinds"))
}

// ListEvents devuelve la página de eventos del TENANT que casan con el filtro,
// ordenados por última actividad descendente y con la marca «vencido» resuelta.
//
// Es la lectura de REQ-28: la que hace ejecutable «ve limpiando». Nunca acota a una
// sesión ni a un contacto salvo que el filtro lo pida, y el tenant lo pone el
// llamante desde el token (INV-8) — este método no tiene forma de sacarlo de otro
// sitio, que es justo la garantía.
//
// El filtro se normaliza (ListFilter.Normalized) antes de consultar, y la página devuelta trae el
// Page y el PageSize YA normalizados. Total cuenta las coincidencias del filtro entero, no las de
// la página. Son DOS sentencias —la cuenta y la página— sobre la misma subconsulta: la cuenta
// lleva los ocho argumentos del filtro y la página esos ocho más el tamaño y el desplazamiento.
// El instante del reloj inyectado viaja como cuarto argumento de las dos.
//
// Filtros: Status exacto; Kind exacto si no va vacío; Kinds como conjunto cerrado (nil = sin
// filtro, lista vacía = ninguno pasa); ContactID exacto si no va vacío; Content any|none|alive
// (un evento con contenido `settled` o `discarded` no sale con NINGUNO de los tres, INV-17);
// Stale sobre la marca calculada. Orden: last_activity_at descendente y, a igual instante, id.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "events: contar los eventos del tenant: %w";
//   - "events: listar los eventos del tenant: %w";
//   - los tres de recorrido de ListAlive, tal cual.
func (s *Store) ListEvents(ctx context.Context, tenantID string, f ListFilter) (EventPage, error) {
	panic(pendiente.Implementar("events.Store.ListEvents"))
}
