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
	if len(args) != 5 {
		t.Fatalf("filterArgs devolvió %d argumentos, quiero 5 (tenant, from, to, statuses, session)", len(args))
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
