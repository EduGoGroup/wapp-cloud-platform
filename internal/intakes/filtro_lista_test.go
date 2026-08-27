package intakes_test

import (
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// TestStoredVariantsOf_ExpandeCADAElemento es la unidad de la condición de
// D-044.47 §2. `confirmed` va el ÚLTIMO a propósito: es el único estado del ciclo
// con clave legada (`closed`), así que una implementación que solo expandiera el
// primer elemento devolvería la lista sin `closed` y perdería en silencio todas las
// solicitudes que cerró el módulo cart.
func TestStoredVariantsOf_ExpandeCADAElemento(t *testing.T) {
	got := intakes.StoredVariantsOf([]string{intakes.StatusOpen, intakes.StatusConfirmed})
	quiero := []string{intakes.StatusClosedLegacy, intakes.StatusConfirmed, intakes.StatusOpen}
	if !slices.Equal(got, quiero) {
		t.Fatalf("StoredVariantsOf=%v, quiero %v (`closed` sale porque `confirmed` también se expande)", got, quiero)
	}
}

// TestStoredVariantsOf_OrdenDeterministaYSinRepetidos: el resultado viaja a un
// `= ANY($n)` y a los tests, así que dos filtros con los mismos estados en distinto
// orden no pueden producir dos consultas distintas.
func TestStoredVariantsOf_OrdenDeterministaYSinRepetidos(t *testing.T) {
	unSentido := intakes.StoredVariantsOf([]string{intakes.StatusOpen, intakes.StatusCancelled})
	elOtro := intakes.StoredVariantsOf([]string{intakes.StatusCancelled, intakes.StatusOpen})
	if !slices.Equal(unSentido, elOtro) {
		t.Fatalf("el orden de entrada cambió la salida: %v vs %v", unSentido, elOtro)
	}

	// `confirmed` y su alias `closed` piden lo MISMO: la expansión no puede sacar
	// la clave dos veces o el `= ANY` cargaría con duplicados.
	got := intakes.StoredVariantsOf([]string{intakes.StatusConfirmed, intakes.StatusClosedLegacy})
	if !slices.Equal(got, []string{intakes.StatusClosedLegacy, intakes.StatusConfirmed}) {
		t.Fatalf("StoredVariantsOf con la clave y su alias = %v, quiero las dos variantes UNA vez", got)
	}
}

// TestStoredVariantsOf_ListaVaciaEsSinFiltro: nil y no `[]`. La diferencia la
// consume filterArgs, que apaga la rama del predicado con `$4 IS NULL`; un slice
// vacío llegaría como un array vacío y `status = ANY('{}')` no casa con NADA — la
// bandeja saldría en blanco.
func TestStoredVariantsOf_ListaVaciaEsSinFiltro(t *testing.T) {
	if got := intakes.StoredVariantsOf(nil); got != nil {
		t.Fatalf("StoredVariantsOf(nil)=%v, quiero nil", got)
	}
	if got := intakes.StoredVariantsOf([]string{}); got != nil {
		t.Fatalf("StoredVariantsOf([])=%v, quiero nil", got)
	}
}

// TestFilterNormalized_EstadosSaneados: normaliza los alias, tira los vacíos,
// ordena y colapsa. Es idempotente, como el resto de Normalized.
func TestFilterNormalized_EstadosSaneados(t *testing.T) {
	f := intakes.Filter{Statuses: []string{
		intakes.StatusOpen, "", intakes.StatusClosedLegacy, intakes.StatusOpen,
	}}.Normalized()

	quiero := []string{intakes.StatusConfirmed, intakes.StatusOpen}
	if !slices.Equal(f.Statuses, quiero) {
		t.Fatalf("Statuses=%v, quiero %v (`closed` normaliza a `confirmed`, el vacío se cae y el repetido se colapsa)", f.Statuses, quiero)
	}
	if segunda := f.Normalized(); !slices.Equal(segunda.Statuses, quiero) {
		t.Fatalf("Normalized no es idempotente: %v", segunda.Statuses)
	}
}

// TestFilterNormalized_TodoVacioEsSinFiltro: `?status=` repetido no puede dejar el
// filtro pidiendo un estado que no existe.
func TestFilterNormalized_TodoVacioEsSinFiltro(t *testing.T) {
	if got := (intakes.Filter{Statuses: []string{"", ""}}).Normalized().Statuses; got != nil {
		t.Fatalf("Statuses=%v, quiero nil", got)
	}
}

// TestFilterNormalized_OrdenPorDefectoEsNewest: la mitad «cero regresión» de
// D-044.48 §3 vista desde el dominio. Un filtro que nadie tocó pide lo que el Plan
// 041 sirve.
func TestFilterNormalized_OrdenPorDefectoEsNewest(t *testing.T) {
	if got := (intakes.Filter{}).Normalized().Sort; got != intakes.SortNewest {
		t.Fatalf("Sort=%q, quiero %q", got, intakes.SortNewest)
	}
	// Y un valor que no es ninguno de los dos cae al default en vez de llegar al
	// store: fail-safe, no un ORDER BY inventado.
	if got := (intakes.Filter{Sort: "antiguas"}).Normalized().Sort; got != intakes.SortNewest {
		t.Fatalf("Sort=%q ante basura, quiero caer a %q", got, intakes.SortNewest)
	}
	if got := (intakes.Filter{Sort: intakes.SortOldest}).Normalized().Sort; got != intakes.SortOldest {
		t.Fatalf("Sort=%q, quiero respetar %q", got, intakes.SortOldest)
	}
}

// TestIsSort_SoloLosDos: lo que el borde HTTP usa para contestar 400.
func TestIsSort_SoloLosDos(t *testing.T) {
	for _, ok := range []string{intakes.SortNewest, intakes.SortOldest} {
		if !intakes.IsSort(ok) {
			t.Fatalf("IsSort(%q)=false", ok)
		}
	}
	for _, malo := range []string{"", "NEWEST", "asc", "desc", "antiguas"} {
		if intakes.IsSort(malo) {
			t.Fatalf("IsSort(%q)=true", malo)
		}
	}
}

// TestMemoryStore_ListPorDosEstados_AlcanzaLasLEGADAS ejercita el mismo predicado
// que el store real por el doble que usan los tests de handler: sin esta paridad,
// un test de handler contra el MemoryStore diría algo que producción no cumple.
func TestMemoryStore_ListPorDosEstados_AlcanzaLasLEGADAS(t *testing.T) {
	st := intakes.NewMemoryStore()
	st.Add("t1", intakes.Intake{ID: "a", Status: intakes.StatusClosedLegacy, CreatedAt: día(1)})
	st.Add("t1", intakes.Intake{ID: "b", Status: intakes.StatusCancelled, CreatedAt: día(2)})
	st.Add("t1", intakes.Intake{ID: "c", Status: intakes.StatusOpen, CreatedAt: día(3)})

	// `confirmed` en SEGUNDA posición: la fila `a` solo aparece si la expansión de
	// legadas llegó al segundo elemento de la lista.
	got, total, err := st.List(t.Context(), "t1",
		intakes.Filter{Statuses: []string{intakes.StatusCancelled, intakes.StatusConfirmed}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(got) != 2 || got[0].ID != "b" || got[1].ID != "a" {
		t.Fatalf("got=%+v total=%d; quiero b y a (la `closed` legada incluida)", got, total)
	}
}

// TestMemoryStore_ListSortOldest: el doble ordena como el ORDER BY del store real,
// desempate por id incluido y en el MISMO sentido.
func TestMemoryStore_ListSortOldest(t *testing.T) {
	st := intakes.NewMemoryStore()
	// Dos con el MISMO created_at: el desempate por id es lo que hace el orden
	// total, y tiene que girar con el resto.
	st.Add("t1", intakes.Intake{ID: "b", Status: intakes.StatusOpen, CreatedAt: día(1)})
	st.Add("t1", intakes.Intake{ID: "a", Status: intakes.StatusOpen, CreatedAt: día(1)})
	st.Add("t1", intakes.Intake{ID: "c", Status: intakes.StatusOpen, CreatedAt: día(2)})

	viejas, _, err := st.List(t.Context(), "t1", intakes.Filter{Sort: intakes.SortOldest})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if ids := idsDeIntakes(viejas); !slices.Equal(ids, []string{"a", "b", "c"}) {
		t.Fatalf("oldest=%v, quiero [a b c]", ids)
	}

	nuevas, _, err := st.List(t.Context(), "t1", intakes.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if ids := idsDeIntakes(nuevas); !slices.Equal(ids, []string{"c", "b", "a"}) {
		t.Fatalf("newest=%v, quiero [c b a] (el desempate por id gira con el orden)", ids)
	}
}

func idsDeIntakes(in []intakes.Intake) []string {
	out := make([]string, 0, len(in))
	for _, i := range in {
		out = append(out, i.ID)
	}
	return out
}

// día fija una fecha determinista para las cabeceras sembradas.
func día(d int) time.Time { return time.Date(2026, 8, d, 12, 0, 0, 0, time.UTC) }
