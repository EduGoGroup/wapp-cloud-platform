// Porta internal/publicapi/intakes.go @ ed60c24 (1073 líneas: parseIntakeFilter y
// parseFilterTime, líneas 965-1073).
//
// intakes_filter.go — LA QUERY DEL LISTADO DE SOLICITUDES: cómo se traduce a un intakes.Filter.
// Es un trozo de intakes.go partido por tema (05 E-13). La comparten G1 (la bandeja) y, con el
// mismo filtro, G9 y G10 (el export y el resumen): que las tres lean la query por el MISMO sitio
// es lo que hace que el CSV que el dueño descarga sea el de la pantalla que está mirando.
//
// No exporta nada y nació con el verde (05 E-4, P6): las promesas de la query están en el
// contrato de MountIntakes y las prueba intakes_filter_test.go a través de G1.

package apipublica

import (
	"net/http"
	"strconv"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// parseIntakeFilter traduce la query a un intakes.Filter. Devuelve el mensaje de
// error (cadena vacía = todo bien) en vez de escribir la respuesta: la política de
// códigos vive en el handler.
//
// `status` acepta las claves nuevas Y el `closed` legado (se normaliza a
// `confirmed`, y el store alcanza igual las filas viejas). Una clave desconocida se
// rechaza con 400 en vez de listar todo: un typo que devuelve la bandeja entera es
// peor que un error.
//
// 🔑 `status` SE REPITE para pedir varios (`?status=pending_approval&status=
// needs_info`, Plan 044 · T4.1 / D-044.47 §2). Hasta el 044 esto usaba
// `q.Get("status")`, que devuelve el PRIMER valor y descarta el resto EN SILENCIO:
// una bandeja que pedía dos estados recibía uno y no tenía forma de enterarse. Un
// solo `?status=x` sigue significando exactamente lo de antes, así que el 041 no
// ve cambiar nada.
// ⚠️ La forma con comas (`?status=a,b`) NO se acepta: `a,b` no es un estado
// conocido y sale por el 400 de siempre, que es un error visible y no otro
// descarte mudo.
//
// `sort` elige el orden (`newest`|`oldest`, D-044.48 §3). Ausente ⇒ `newest`, que
// es lo que esta ruta sirve y documenta desde el Plan 041: quien no lo pide no ve
// cambiar su pantalla. Un valor desconocido se rechaza con 400 por la misma razón
// que un `status` desconocido — servir otro orden en silencio sería peor.
//
// `orphan` acota a las solicitudes cuyo evento conversacional ya no está `open`
// (Plan 041 · T4.8 / REQ-21c, construido en el Plan 044). Se lee con ParseBool, así
// que `1`/`t`/`TRUE` valen tanto como `true`; cualquier otra cosa —`orphan=si`,
// `orphan=yes`— es 400 y NO «no filtres», que es el mismo criterio que `status` y
// `sort`: esta vista PRESELECCIONA lo que el dueño va a descartar sin vuelta atrás,
// y servirle la bandeja entera creyendo él que mira huérfanas es exactamente el
// accidente que no puede pasar.
// ⚠️ `orphan=false` y `orphan` ausente significan LO MISMO —sin cota por este
// lado—, como el cero-valor del resto de campos de Filter. No existe «enséñame solo
// las que SÍ tienen conversación viva»: nadie lo ha pedido y sería una vista sin
// acción detrás.
func parseIntakeFilter(r *http.Request) (intakes.Filter, string) {
	q := r.URL.Query()

	from, err := parseFilterTime(q.Get("from"), false)
	if err != nil {
		return intakes.Filter{}, "from inválido: usa YYYY-MM-DD o RFC3339"
	}
	to, err := parseFilterTime(q.Get("to"), true)
	if err != nil {
		return intakes.Filter{}, "to inválido: usa YYYY-MM-DD o RFC3339"
	}

	// q["status"] y NO q.Get("status"): el Get se queda con el primero.
	var statuses []string
	for _, raw := range q["status"] {
		if raw == "" {
			continue // `?status=` es "sin filtro", no un estado vacío
		}
		status := intakes.NormalizeStatus(raw)
		if !intakes.IsStatus(status) {
			return intakes.Filter{}, "status desconocido"
		}
		statuses = append(statuses, status)
	}

	sort := q.Get("sort")
	if sort != "" && !intakes.IsSort(sort) {
		return intakes.Filter{}, "sort desconocido: usa newest u oldest"
	}

	// `?orphan=` (clave presente y vacía) es «sin filtro», igual que `?status=`: una
	// UI que arma la query siempre con la clave y la deja en blanco cuando el
	// operador no marcó la casilla no puede recibir un 400.
	orphan := false
	if raw := q.Get("orphan"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return intakes.Filter{}, "orphan inválido: usa true o false"
		}
		orphan = parsed
	}

	return intakes.Filter{
		From:      from,
		To:        to,
		Statuses:  statuses,
		SessionID: q.Get("session"),
		Orphan:    orphan,
		Sort:      sort,
		Page:      parseIntQuery(r, "page", 1),
		PageSize:  parseIntQuery(r, "page_size", intakes.DefaultPageSize),
	}, ""
}

// parseFilterTime acepta una fecha suelta (YYYY-MM-DD, en UTC) o un instante
// RFC3339. Vacío ⇒ sin cota.
//
// El rango del filtro es [From, To). Una fecha suelta en `to` significa "hasta el
// final de ESE día", así que se le suma un día: sin eso, `to=2026-08-06` no
// devolvería ninguna solicitud del 6 de agosto y el usuario juraría que perdió
// pedidos. Un RFC3339 explícito se respeta tal cual (quien escribe un instante
// sabe lo que pide).
func parseFilterTime(raw string, endOfDay bool) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if day, err := time.ParseInLocation(time.DateOnly, raw, time.UTC); err == nil {
		if endOfDay {
			return day.AddDate(0, 0, 1), nil
		}
		return day, nil
	}
	return time.Parse(time.RFC3339, raw)
}
