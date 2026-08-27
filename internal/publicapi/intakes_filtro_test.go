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
