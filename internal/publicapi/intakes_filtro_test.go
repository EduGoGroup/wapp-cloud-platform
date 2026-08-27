package publicapi_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// listar pide la bandeja con la query dada y exige 200.
func listar(t *testing.T, query string) intakeListDTO {
	t.Helper()
	api := newAPI(intakesDeps(seedIntakes()), intakesKeys())
	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes"+query, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; query=%s body=%s", rec.Code, query, rec.Body.String())
	}
	return decodeList(t, rec.Body.Bytes())
}

// ==================== filtro por LISTA de estados (D-044.47 §2) ====================

// TestIntakes_200_DosEstadosEnUnaSolaLlamada es lo que la tarea existe para
// permitir: la bandeja del dueño necesita `pending_approval` Y `needs_info` en UNA
// pantalla, y hasta el 044 eso eran dos llamadas cuyos paginadores no se podían
// componer.
//
// El fixture no tiene ninguna de esas dos, así que el test usa el par que sí tiene
// —`open` y `cancelled`— y afirma lo mismo: la unión de los dos filtros sueltos.
func TestIntakes_200_DosEstadosEnUnaSolaLlamada(t *testing.T) {
	sueltos := append(idsDe(listar(t, "?status=open")), idsDe(listar(t, "?status=cancelled"))...)
	if len(sueltos) != 2 {
		t.Fatalf("el fixture cambió: los dos filtros sueltos dan %v", sueltos)
	}

	juntos := idsDe(listar(t, "?status=open&status=cancelled"))
	if !slices.Equal(juntos, []string{intakeA3, intakeA2}) {
		t.Fatalf("ids=%v, quiero [%s %s] (la unión, más recientes primero)", juntos, intakeA3, intakeA2)
	}
	if got := listar(t, "?status=open&status=cancelled").Total; got != 2 {
		t.Fatalf("total=%d, quiero 2: el total cuenta la unión, no el primer estado", got)
	}
}

// TestIntakes_200_ElSegundoEstadoNoSeDescartaEnSilencio es el test que caza la
// implementación VIEJA, y por eso está aparte del anterior.
//
// Hasta el 044 el parseo usaba `q.Get("status")`, que devuelve el PRIMER valor y
// tira el resto sin decir nada: `?status=open&status=cancelled` respondía 200 con
// solo las `open` y quien lo llamaba no tenía forma de enterarse. Un 200 con menos
// filas de las pedidas es peor que un error.
func TestIntakes_200_ElSegundoEstadoNoSeDescartaEnSilencio(t *testing.T) {
	soloElPrimero := idsDe(listar(t, "?status=open"))
	losDos := idsDe(listar(t, "?status=open&status=cancelled"))

	if slices.Equal(soloElPrimero, losDos) {
		t.Fatalf("pedir dos estados devolvió lo mismo que pedir uno (%v): el segundo se está descartando", losDos)
	}
}

// TestIntakes_200_CadaEstadoDeLaListaAlcanzaSusFilasLEGADAS es la condición
// explícita de D-044.47 §2: StoredVariants se aplica a CADA elemento de la lista y
// no solo al primero.
//
// `confirmed` va en SEGUNDA posición a propósito. Las filas que el módulo cart
// cerró están guardadas como `closed`, así que solo aparecen si la expansión de
// variantes legadas alcanzó al segundo elemento. Una implementación que expandiera
// la cabeza de la lista y metiera el resto tal cual pasaría el test anterior y
// fallaría éste.
func TestIntakes_200_CadaEstadoDeLaListaAlcanzaSusFilasLEGADAS(t *testing.T) {
	got := idsDe(listar(t, "?status=cancelled&status=confirmed"))
	quiero := []string{intakeA5, intakeA4, intakeA3, intakeA1}
	if !slices.Equal(got, quiero) {
		t.Fatalf("ids=%v, quiero %v: las dos filas `closed` legadas (%s, %s) salen porque `confirmed` —el SEGUNDO estado— también se expande",
			got, quiero, intakeA4, intakeA1)
	}
}

// TestIntakes_200_ElOrdenDeLosEstadosNoCambiaElResultado: el filtro es un CONJUNTO.
// Los mismos dos estados al revés tienen que dar exactamente la misma página.
func TestIntakes_200_ElOrdenDeLosEstadosNoCambiaElResultado(t *testing.T) {
	unSentido := idsDe(listar(t, "?status=confirmed&status=cancelled"))
	elOtro := idsDe(listar(t, "?status=cancelled&status=confirmed"))
	if !slices.Equal(unSentido, elOtro) {
		t.Fatalf("el orden de los estados cambió el resultado: %v vs %v", unSentido, elOtro)
	}
}

// TestIntakes_200_EstadoRepetidoNoDuplicaFilas: `?status=open&status=open` es una
// petición legítima de un cliente que arma la query en un bucle.
func TestIntakes_200_EstadoRepetidoNoDuplicaFilas(t *testing.T) {
	got := listar(t, "?status=open&status=open")
	if !slices.Equal(idsDe(got), []string{intakeA2}) || got.Total != 1 {
		t.Fatalf("estado repetido dio %v con total=%d, quiero la fila una sola vez", idsDe(got), got.Total)
	}
}

// TestIntakes_200_UnSoloEstadoSigueSignificandoLoMismo: cero regresión para el 041.
func TestIntakes_200_UnSoloEstadoSigueSignificandoLoMismo(t *testing.T) {
	if got := idsDe(listar(t, "?status=open")); !slices.Equal(got, []string{intakeA2}) {
		t.Fatalf("ids=%v, quiero solo la `open`", got)
	}
}

// TestIntakes_400_UnEstadoMaloEntreDosBuenos: el desconocido se DICE aunque venga
// acompañado. Servir la unión de los dos válidos e ignorar el tercero sería el
// mismo descarte mudo que esta tarea vino a cerrar.
func TestIntakes_400_UnEstadoMaloEntreDosBuenos(t *testing.T) {
	api := newAPI(intakesDeps(seedIntakes()), intakesKeys())
	rec := call(api, keyAIntakes, http.MethodGet,
		"/api/v1/intakes?status=open&status=abiertas&status=cancelled", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestIntakes_200_StatusVacioEsSinFiltro: `?status=` no es un estado vacío que no
// casa con nada, es «no filtres». Una UI que arma la query siempre con la clave y
// la deja en blanco cuando el usuario no eligió no puede recibir cero filas.
func TestIntakes_200_StatusVacioEsSinFiltro(t *testing.T) {
	if got := listar(t, "?status=").Total; got != 5 {
		t.Fatalf("total=%d, quiero las 5 del tenant: `?status=` es sin filtro", got)
	}
}

// ============================ orden (D-044.48 §3) ============================

// TestIntakes_200_SinSortSigueSiendoNewest: el default es lo que el Plan 041 sirve
// y documenta, de modo que un usuario del 041 no ve cambiar su pantalla sin
// pedirlo. Es la mitad «cero regresión» de la decisión.
func TestIntakes_200_SinSortSigueSiendoNewest(t *testing.T) {
	sinPedir := idsDe(listar(t, ""))
	pidiendoNewest := idsDe(listar(t, "?sort=newest"))
	quiero := []string{intakeA5, intakeA4, intakeA3, intakeA2, intakeA1}
	if !slices.Equal(sinPedir, quiero) {
		t.Fatalf("ids=%v, quiero %v (más recientes primero por defecto)", sinPedir, quiero)
	}
	if !slices.Equal(sinPedir, pidiendoNewest) {
		t.Fatalf("no pedir orden y pedir `newest` dieron cosas distintas: %v vs %v", sinPedir, pidiendoNewest)
	}
}

// TestIntakes_200_SortOldest: lo que pide la bandeja del 044, porque un plazo que
// vence (T4.5) tiene que salir arriba.
func TestIntakes_200_SortOldest(t *testing.T) {
	got := idsDe(listar(t, "?sort=oldest"))
	quiero := []string{intakeA1, intakeA2, intakeA3, intakeA4, intakeA5}
	if !slices.Equal(got, quiero) {
		t.Fatalf("ids=%v, quiero %v (más antiguas primero)", got, quiero)
	}
}

// TestIntakes_200_SortOldest_PaginaDesdeLaMasAntigua: el orden tiene que gobernar
// la PÁGINA, no solo el bloque devuelto. Sin esto, un store que ordenara después de
// paginar pasaría el test anterior con page_size=5 y devolvería las filas
// equivocadas en cuanto la bandeja tuviera más de una página.
func TestIntakes_200_SortOldest_PaginaDesdeLaMasAntigua(t *testing.T) {
	got := listar(t, "?sort=oldest&page=1&page_size=2")
	if !slices.Equal(idsDe(got), []string{intakeA1, intakeA2}) {
		t.Fatalf("página 1 = %v, quiero las DOS más antiguas", idsDe(got))
	}
	if got.Total != 5 {
		t.Fatalf("total=%d, quiero 5: el orden no cambia cuántas hay", got.Total)
	}
	ultima := listar(t, "?sort=oldest&page=3&page_size=2")
	if !slices.Equal(idsDe(ultima), []string{intakeA5}) {
		t.Fatalf("página 3 = %v, quiero la más reciente al final", idsDe(ultima))
	}
}

// TestIntakes_400_SortDesconocido: mismo criterio que `status desconocido`. Servir
// otro orden en silencio es peor que un error, porque quien pidió `oldest` para ver
// lo que vence creería estar viéndolo.
func TestIntakes_400_SortDesconocido(t *testing.T) {
	api := newAPI(intakesDeps(seedIntakes()), intakesKeys())
	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes?sort=antiguas", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestIntakes_200_SortYFiltroDeListaSeCombinan: las dos mitades de la tarea en la
// MISMA llamada, que es como la bandeja del 044 la va a hacer.
func TestIntakes_200_SortYFiltroDeListaSeCombinan(t *testing.T) {
	got := idsDe(listar(t, "?status=cancelled&status=confirmed&sort=oldest"))
	quiero := []string{intakeA1, intakeA3, intakeA4, intakeA5}
	if !slices.Equal(got, quiero) {
		t.Fatalf("ids=%v, quiero %v", got, quiero)
	}
}

// TestIntakes_200_SortNoAfectaAlEstadoNormalizado es una no-regresión barata: el
// `closed` legado se sigue sirviendo como `confirmed` mire la lista por donde mire.
func TestIntakes_200_SortNoAfectaAlEstadoNormalizado(t *testing.T) {
	for _, in := range listar(t, "?status=confirmed&sort=oldest").Intakes {
		if in.Status != intakes.StatusConfirmed {
			t.Fatalf("status=%q; el `closed` legado se sirve NORMALIZADO", in.Status)
		}
	}
}

// ================= el filtro de HUÉRFANAS `orphan` (T4.8, REQ-21c) =================

// Los dos eventos conversacionales que separan una bandeja normal de la de
// huérfanos: uno `open` —hay alguien escribiendo ahora mismo— y uno `cancelled`,
// que es un padre muerto y por tanto NO protege a su contenido.
const (
	evVivo   = "ev-vivo-open"
	evMuerto = "ev-muerto-cancelled"
)

// bandejaConEventos toma el fixture de siempre y le cuelga las conversaciones, que
// es lo que el filtro mira. Reparte los tres casos que existen:
//
//   - A2 declara un evento `open` ⇒ NO es huérfana. Es la única.
//   - A5 declara un evento `cancelled` ⇒ SÍ es huérfana, y es el caso que separa
//     este filtro de un `event_id IS NULL`: tiene padre, pero el padre está muerto.
//     Un filtro que preguntara «¿tiene ligadura?» la dejaría fuera y el dueño no
//     podría limpiar justo lo que vino a limpiar.
//   - A1, A3 y A4 quedan sin ligadura (filas LEGADAS pre-0054) ⇒ huérfanas, por lo
//     mismo que hasLiveEventTx las deja descartar: sin padre declarado no existe la
//     conversación que la guarda protege.
func bandejaConEventos() *intakes.MemoryStore {
	st := seedIntakes()
	st.BindEvent(intakeA2, evVivo)
	st.SetEvent(evVivo, "open")
	st.BindEvent(intakeA5, evMuerto)
	st.SetEvent(evMuerto, "cancelled")
	return st
}

// listarEn es `listar` sobre una bandeja concreta, para los tests que necesitan
// sembrar conversaciones además de solicitudes.
func listarEn(t *testing.T, st *intakes.MemoryStore, query string) intakeListDTO {
	t.Helper()
	api := newAPI(intakesDeps(st), intakesKeys())
	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes"+query, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; query=%s body=%s", rec.Code, query, rec.Body.String())
	}
	return decodeList(t, rec.Body.Bytes())
}

// TestIntakes_200_OrphanDevuelveSOLOLasQueNoTienenEventoVIVO es el criterio de T4.8
// tal como está escrito: la vista de huérfanos enseña las solicitudes sin evento
// vivo, y ninguna más.
//
// Lo que hace verificable el «solo»: A2 tiene un evento `open` y las otras cuatro
// no, así que la respuesta correcta es la bandeja MENOS una fila — no la bandeja, y
// no la lista vacía.
func TestIntakes_200_OrphanDevuelveSOLOLasQueNoTienenEventoVIVO(t *testing.T) {
	got := listarEn(t, bandejaConEventos(), "?orphan=true")
	quiero := []string{intakeA5, intakeA4, intakeA3, intakeA1}
	if !slices.Equal(idsDe(got), quiero) {
		t.Fatalf("ids=%v, quiero %v (todas menos %s, que tiene conversación viva)", idsDe(got), quiero, intakeA2)
	}
	if got.Total != 4 {
		t.Fatalf("total=%d, quiero 4: el total cuenta las huérfanas, no la bandeja entera", got.Total)
	}
}

// TestIntakes_200_OrphanNoDevuelveLaBandejaENTERA es el test que caza la
// implementación que NO FILTRA — un `orphan` que se parsea, viaja y no se aplica—,
// y por eso está aparte del anterior.
//
// Es el fallo más caro que puede tener esta vista y el más silencioso: responde 200,
// con datos, ordenados, y el dueño marca «todo lo visible» sobre una pantalla que
// cree de huérfanos. Descartar es irreversible (D-041.22).
func TestIntakes_200_OrphanNoDevuelveLaBandejaENTERA(t *testing.T) {
	st := bandejaConEventos()
	sinFiltro := idsDe(listarEn(t, st, ""))
	huérfanas := idsDe(listarEn(t, st, "?orphan=true"))

	if slices.Equal(sinFiltro, huérfanas) {
		t.Fatalf("pedir huérfanas devolvió la bandeja entera (%v): el filtro no se está aplicando", huérfanas)
	}
	if slices.Contains(huérfanas, intakeA2) {
		t.Fatalf("%s tiene un evento `open` y no puede salir en la vista de huérfanos: %v", intakeA2, huérfanas)
	}
}

// TestIntakes_200_OrphanAlcanzaLaQueDECLARAUnEventoMUERTO: tener padre no salva a
// nadie; lo que salva es que el padre esté `open`.
//
// Es el callejón del journal 2026-08-10 visto desde el listado: el pedido de un
// evento ya `cancelled` es exactamente lo que el dueño necesita limpiar, y un filtro
// escrito como «event_id IS NULL» —la aproximación fácil— lo escondería para
// siempre.
func TestIntakes_200_OrphanAlcanzaLaQueDECLARAUnEventoMUERTO(t *testing.T) {
	if got := idsDe(listarEn(t, bandejaConEventos(), "?orphan=true")); !slices.Contains(got, intakeA5) {
		t.Fatalf("ids=%v, quiero que incluya %s: declara un evento `cancelled`, y un padre muerto no protege",
			got, intakeA5)
	}
}

// TestIntakes_200_SinOrphanNoCambiaNada: cero regresión para el Plan 041. La bandeja
// de todos los días sigue siendo la de todos los días, con evento vivo o sin él.
//
// Vigila el fallo simétrico del filtro: que el `$6` viajara como `false` en vez de
// NULL dejaría el NOT EXISTS encendido SIEMPRE y esta pantalla perdería justo las
// solicitudes que el cliente está escribiendo ahora.
func TestIntakes_200_SinOrphanNoCambiaNada(t *testing.T) {
	st := bandejaConEventos()
	quiero := []string{intakeA5, intakeA4, intakeA3, intakeA2, intakeA1}
	for _, query := range []string{"", "?orphan=false", "?orphan="} {
		got := listarEn(t, st, query)
		if !slices.Equal(idsDe(got), quiero) {
			t.Fatalf("con %q ids=%v, quiero las 5 del tenant %v", query, idsDe(got), quiero)
		}
		if got.Total != 5 {
			t.Fatalf("con %q total=%d, quiero 5", query, got.Total)
		}
	}
}

// TestIntakes_400_OrphanInvalido: mismo criterio que `status` y `sort` desconocidos.
// Un `?orphan=si` que se sirviera como «sin filtro» daría una bandeja entera con
// pinta de vista de huérfanos, que es la pantalla desde la que se descarta sin
// vuelta atrás.
func TestIntakes_400_OrphanInvalido(t *testing.T) {
	for _, malo := range []string{"si", "yes", "sí", "2", "huerfanas"} {
		api := newAPI(intakesDeps(bandejaConEventos()), intakesKeys())
		rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes?orphan="+malo, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("orphan=%q dio code=%d, quiero 400; body=%s", malo, rec.Code, rec.Body.String())
		}
	}
}

// TestIntakes_200_OrphanSeCombinaConElRestoDelFiltro: `orphan` es un filtro más y se
// compone con los otros, que es como la bandeja del 044 lo va a pedir («huérfanas y
// todavía sin cerrar»). Se comprueba contra el MISMO filtro sin `orphan` para que el
// test no pueda pasar por casualidad con una lista vacía.
func TestIntakes_200_OrphanSeCombinaConElRestoDelFiltro(t *testing.T) {
	st := bandejaConEventos()
	if got := idsDe(listarEn(t, st, "?status=open")); !slices.Equal(got, []string{intakeA2}) {
		t.Fatalf("el fixture cambió: `?status=open` da %v y quiero solo %s", got, intakeA2)
	}
	if got := idsDe(listarEn(t, st, "?status=open&orphan=true")); len(got) != 0 {
		t.Fatalf("ids=%v, quiero ninguna: la única `open` tiene conversación viva", got)
	}
	if got := idsDe(listarEn(t, st, "?status=confirmed&orphan=true&sort=oldest")); !slices.Equal(got, []string{intakeA1, intakeA4, intakeA5}) {
		t.Fatalf("ids=%v, quiero las tres `confirmed` huérfanas de la más antigua a la más nueva", got)
	}
}

// TestIntakes_200_OrphanConLaLISTADeEstadosDeT41 es la composición de las dos mitades de esta ola en
// UNA llamada: el filtro por LISTA de estados (T4.1, D-044.47 §2) Y la vista de huérfanos (T4.8).
// Es la consulta que la bandeja del dueño va a hacer de verdad, y la que ninguno de los dos tests
// sueltos comprueba.
//
// El par elegido no es cualquiera: `open` es el estado de la ÚNICA solicitud con conversación viva,
// y `confirmed` trae además sus dos filas legadas escritas como `closed`. Así el mismo assert
// verifica las tres cosas a la vez — que el segundo estado no se pierde, que sigue expandiéndose a
// su variante legada con el orphan encendido, y que la `open` viva queda fuera.
func TestIntakes_200_OrphanConLaLISTADeEstadosDeT41(t *testing.T) {
	st := bandejaConEventos()

	sinOrphan := idsDe(listarEn(t, st, "?status=open&status=confirmed&sort=oldest"))
	if !slices.Equal(sinOrphan, []string{intakeA1, intakeA2, intakeA4, intakeA5}) {
		t.Fatalf("el fixture cambió: los dos estados sin orphan dan %v", sinOrphan)
	}

	conOrphan := idsDe(listarEn(t, st, "?status=open&status=confirmed&orphan=true&sort=oldest"))
	quiero := []string{intakeA1, intakeA4, intakeA5}
	if !slices.Equal(conOrphan, quiero) {
		t.Fatalf("ids=%v, quiero %v: los dos estados siguen valiendo (con el `closed` legado dentro) y solo cae %s, la `open` con conversación viva",
			conOrphan, quiero, intakeA2)
	}
}
