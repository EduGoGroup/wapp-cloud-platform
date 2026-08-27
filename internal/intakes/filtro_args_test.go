package intakes

import (
	"slices"
	"testing"
)

// Este fichero es un test INTERNO (package intakes, no intakes_test) a propósito:
// filterArgs es privada y es EL CABLEADO que conecta el filtro con el `= ANY($4)`
// del store real. Probar solo StoredVariantsOf —que es pública y ya tiene su test—
// deja ese cableado sin vigilar: se puede romper `filterArgs` para que expanda solo
// el primer estado, y la función pública sigue verde porque nadie la llama mal.
//
// 🔴 POR QUÉ NO BASTABAN LOS TESTS DE INTEGRACIÓN. Lo que cubriría esto de verdad es
// TestPostgres_List_* (postgres_integration_test.go), pero esos se SALTAN sin
// DATABASE_URL — que es el caso normal de una corrida local y de este gate. Un
// criterio que solo se comprueba cuando hay Postgres delante es un criterio que casi
// nunca se comprueba, y D-044.47 §2 es una condición escrita que no puede depender
// de eso. filterArgs es una función PURA que devuelve []any, así que se puede
// afirmar sobre el 4.º argumento sin base de datos y sin reloj.

// argStatuses saca el 4.º argumento del predicado —el `$4::text[]` de
// intakeFilterWhere— y exige que sea la lista de estados o nil.
func argStatuses(t *testing.T, args []any) []string {
	t.Helper()
	if len(args) != 6 {
		t.Fatalf("filterArgs devolvió %d argumentos, quiero 6 (tenant, from, to, statuses, session, orphan)", len(args))
	}
	if args[3] == nil {
		return nil
	}
	statuses, ok := args[3].([]string)
	if !ok {
		t.Fatalf("el 4.º argumento es %T y el SQL lo lee como $4::text[]: tiene que ser []string", args[3])
	}
	return statuses
}

// TestFilterArgs_ExpandeCADAEstado_NoSoloElPrimero es la red del cableado que
// D-044.47 §2 puso por escrito: «StoredVariants debe seguir aplicándose a cada
// elemento de la lista, no solo al primero».
//
// 💥 MUTACIÓN EJECUTADA, y COMPILA: en filterArgs, sustituir
//
//	statuses = StoredVariantsOf(f.Statuses)
//
// por
//
//	statuses = StoredVariants(f.Statuses[0])
//
// Antes de este test esa mutación pasaba `go vet` (rc=0) y la suite ENTERA seguía
// verde. Ahora pone rojos los dos subtests de aquí.
//
// Los dos casos atacan mitades distintas del fallo, y hacen falta los dos:
//
//   - «el de variantes NO es el primero» (cancelled va antes que confirmed al
//     ordenar) ⇒ con la mutación se pierde `closed`, que es la clave con la que el
//     módulo cart cerró TODAS las solicitudes históricas.
//   - «el segundo estado se pierde entero» (confirmed va antes que needs_info) ⇒ con
//     la mutación desaparece `needs_info` de la consulta, así que la bandeja del
//     dueño —que pide justo ese par— mostraría media pantalla.
//
// El filtro se normaliza antes, que es lo que hacen List y ListDetails: probarlo sin
// normalizar afirmaría sobre un estado que el store nunca ve.
func TestFilterArgs_ExpandeCADAEstado_NoSoloElPrimero(t *testing.T) {
	casos := []struct {
		nombre string
		pide   []string
		quiero []string
	}{
		{
			// Tras Normalized el orden es alfabético: cancelled, confirmed. El estado
			// CON variantes legadas queda el SEGUNDO, que es el caso que la mutación
			// no puede sobrevivir.
			nombre: "el estado con variantes legadas no es el primero",
			pide:   []string{StatusCancelled, StatusConfirmed},
			quiero: []string{StatusCancelled, StatusClosedLegacy, StatusConfirmed},
		},
		{
			// Aquí el primero SÍ trae variantes; lo que se pierde con la mutación es
			// el segundo estado entero.
			nombre: "el segundo estado no se descarta",
			pide:   []string{StatusNeedsInfo, StatusConfirmed},
			quiero: []string{StatusClosedLegacy, StatusConfirmed, StatusNeedsInfo},
		},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			f := Filter{Statuses: c.pide}.Normalized()
			got := argStatuses(t, filterArgs("tenant-a", f))
			if !slices.Equal(got, c.quiero) {
				t.Fatalf("$4 = %v, quiero %v (pedí %v)", got, c.quiero, c.pide)
			}
		})
	}
}

// TestFilterArgs_UnSoloEstadoSigueIgual: cero regresión para el Plan 041. Un filtro
// de un estado tiene que producir EXACTAMENTE los mismos argumentos que antes de
// que el campo fuera una lista.
func TestFilterArgs_UnSoloEstadoSigueIgual(t *testing.T) {
	f := Filter{Statuses: []string{StatusConfirmed}}.Normalized()
	got := argStatuses(t, filterArgs("tenant-a", f))
	if !slices.Equal(got, []string{StatusClosedLegacy, StatusConfirmed}) {
		t.Fatalf("$4 = %v, quiero [closed confirmed]", got)
	}
}

// TestFilterArgs_SinEstadosViajaNULL: la rama «$4::text[] IS NULL» del predicado
// tiene que quedar DESACTIVADA, y para eso el argumento debe ser nil de verdad.
//
// No es un detalle: un `[]string{}` en su lugar llegaría a Postgres como un array
// vacío, y `status = ANY('{}')` no casa con NINGUNA fila — la bandeja sin filtro
// saldría en blanco. Es un fallo que la mutación de arriba no produce y que este
// test sí vigila.
func TestFilterArgs_SinEstadosViajaNULL(t *testing.T) {
	for _, f := range []Filter{
		Filter{}.Normalized(),
		Filter{Statuses: []string{}}.Normalized(),
		Filter{Statuses: []string{"", ""}}.Normalized(),
	} {
		args := filterArgs("tenant-a", f)
		if args[3] != nil {
			t.Fatalf("$4 = %#v con Statuses=%v, quiero nil (la rama IS NULL desactiva el filtro)", args[3], f.Statuses)
		}
	}
}

// TestFilterArgs_ElOrdenDeEntradaNoCambiaLaConsulta: dos filtros con los mismos
// estados en distinto orden tienen que producir el MISMO `$4`. El plan de Postgres
// se cachea por texto de consulta y argumentos; dos formas del mismo filtro serían
// dos planes para nada.
func TestFilterArgs_ElOrdenDeEntradaNoCambiaLaConsulta(t *testing.T) {
	unSentido := argStatuses(t, filterArgs("t", Filter{
		Statuses: []string{StatusConfirmed, StatusNeedsInfo}}.Normalized()))
	elOtro := argStatuses(t, filterArgs("t", Filter{
		Statuses: []string{StatusNeedsInfo, StatusConfirmed}}.Normalized()))
	if !slices.Equal(unSentido, elOtro) {
		t.Fatalf("el orden de entrada cambió $4: %v vs %v", unSentido, elOtro)
	}
}

// ==================== el filtro de HUÉRFANAS (T4.8, REQ-21c) ====================

// argOrphan saca el 6.º argumento del predicado —el `$6::boolean` de
// intakeFilterWhere—, que es o `true` o nil y nunca otra cosa.
//
// Va por separado de argStatuses porque comprueba una propiedad distinta: que el
// filtro APAGADO viaje como NULL de verdad. Un `false` en su lugar compilaría, y el
// `$6::boolean IS NULL OR …` dejaría de desactivarse.
func argOrphan(t *testing.T, args []any) any {
	t.Helper()
	if len(args) != 6 {
		t.Fatalf("filterArgs devolvió %d argumentos, quiero 6 (tenant, from, to, statuses, session, orphan)", len(args))
	}
	return args[5]
}

// TestFilterArgs_OrphanEnciendeElSextoArgumento es el cableado del filtro de T4.8:
// pedir huérfanas tiene que llegar al `$6` del store real como un `true`.
//
// 💥 MUTACIÓN EJECUTADA, y COMPILA: en filterArgs, borrar el
//
//	if f.Orphan { orphan = true }
//
// deja `orphan` en su cero-valor (any(nil)) y el filtro no filtra NADA — el 200
// sale con la bandeja entera y el dueño creyendo que mira huérfanas. Con esta
// mutación este test se pone rojo; sin él, la suite entera seguía verde porque los
// TestPostgres_List_* se SALTAN sin WAPP_TEST_DB_DSN.
func TestFilterArgs_OrphanEnciendeElSextoArgumento(t *testing.T) {
	got := argOrphan(t, filterArgs("tenant-a", Filter{Orphan: true}.Normalized()))
	if got != true {
		t.Fatalf("$6 = %#v con Orphan=true, quiero true (el NOT EXISTS tiene que evaluarse)", got)
	}
}

// TestFilterArgs_SinOrphanViajaNULL: la rama «$6::boolean IS NULL» tiene que quedar
// DESACTIVADA, y para eso el argumento debe ser nil y no `false`.
//
// No es cosmético. Con `false` el predicado pasa a ser «FALSE IS NULL OR NOT
// EXISTS(…)», que es FALSE OR NOT EXISTS: el filtro se quedaría encendido SIEMPRE y
// la bandeja de todos los días —la que no pide huérfanas— perdería toda solicitud
// con conversación viva. Es el mismo fallo que TestFilterArgs_SinEstadosViajaNULL
// vigila para `$4`, y aquí es peor: esconde justo lo que está pasando ahora mismo.
func TestFilterArgs_SinOrphanViajaNULL(t *testing.T) {
	for _, f := range []Filter{
		Filter{}.Normalized(),
		Filter{Orphan: false}.Normalized(),
		Filter{Statuses: []string{StatusOpen}}.Normalized(),
	} {
		if got := argOrphan(t, filterArgs("tenant-a", f)); got != nil {
			t.Fatalf("$6 = %#v con Orphan=false, quiero nil (la rama IS NULL desactiva el filtro)", got)
		}
	}
}

// TestFilterArgs_OrphanNoPisaLosOtrosFiltros: el sexto argumento se AÑADE, no
// desplaza. El filtro que la bandeja del 044 pide de verdad —huérfanas, en DOS
// estados, de una sesión— tiene que seguir llevando su lista entera en el `$4`.
//
// Existe por dos razones y ninguna es cosmética:
//
//   - Este cambio RENUMERÓ los placeholders del LIMIT/OFFSET ($6/$7 → $7/$8). Un
//     descuadre de posiciones no lo detecta el compilador —todo es `any`— y se
//     manifiesta como una consulta que filtra por lo que no es.
//   - El `orphan` y la LISTA de estados de T4.1 se componen, y componerse es
//     justo donde una implementación se rompe: `StatusNeedsInfo` va delante y
//     `StatusConfirmed` detrás, así que el `$4` esperado incluye el `closed`
//     legado — la expansión de CADA elemento (D-044.47 §2) tiene que seguir
//     ocurriendo con el orphan encendido, no solo sin él.
func TestFilterArgs_OrphanNoPisaLosOtrosFiltros(t *testing.T) {
	f := Filter{
		Orphan:    true,
		Statuses:  []string{StatusNeedsInfo, StatusConfirmed},
		SessionID: "sess-a",
	}.Normalized()
	args := filterArgs("tenant-a", f)

	quiero := []string{StatusClosedLegacy, StatusConfirmed, StatusNeedsInfo}
	if got := argStatuses(t, args); !slices.Equal(got, quiero) {
		t.Fatalf("$4 = %v, quiero %v: con el orphan encendido los DOS estados siguen viajando, y `confirmed` sigue alcanzando su `closed` legado", got, quiero)
	}
	if args[4] != "sess-a" {
		t.Fatalf("$5 = %#v, quiero \"sess-a\"", args[4])
	}
	if argOrphan(t, args) != true {
		t.Fatalf("$6 = %#v, quiero true", args[5])
	}
}
