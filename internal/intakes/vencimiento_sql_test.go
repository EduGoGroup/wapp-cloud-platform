package intakes

// vencimiento_sql_test.go — LAS GUARDAS DEL COMPARE-AND-SWAP, AFIRMADAS SOBRE LA
// SENTENCIA (Plan 044 · Ola 4 · T4.5).
//
// 🔴 EL HUECO QUE ESTE FICHERO TAPA, Y CÓMO SE ENCONTRÓ. Se quitó del SQL de
// producción la cláusula `AND expiry_reminded_at IS NULL` —literalmente lo que hace
// idempotente el recordatorio— y `vet` y `go test` siguieron en VERDE, cero FAIL.
// Ni un test se movió.
//
// El motivo es que los 24 tests de conducta corren sobre MemoryStore, y MemoryStore
// implementa el compare-and-swap POR SU CUENTA (memory.go, con Overdue + yaAvisado).
// Las dos implementaciones pueden divergir y hoy nadie lo notaría hasta producción,
// donde el efecto es el GOTEO de avisos que deposit.go documenta como «el pecado que
// no se puede cometer»: sin esa cláusula, CADA lectura del dueño vuelve a ganar el
// CAS.
//
// Lo único que ejercita la sentencia real son tests de integración, que se SALTAN sin
// DATABASE_URL. Este fichero corre SIEMPRE y sin base de datos: afirma sobre el TEXTO
// de la consulta, que es donde vive la decisión.
//
// Molde: internal/intake/reanalisis_internal_test.go (T4.6), que afirma sobre la
// cadena SQL que el job del re-análisis nace `pending`. Y su complemento —la misma
// tabla de casos corrida contra los DOS stores— vive en vencimiento_cas_test.go.
//
// Es un test INTERNO (package intakes) porque la sentencia es privada, y exportarla
// para poder mirarla sería empeorar el diseño para poder probarlo.

import (
	"strings"
	"testing"
	"time"
)

// casDelRecordatorio describe uno de los dos compare-and-swap de recordatorio que
// tiene este paquete. Se barren LOS DOS con las mismas reglas a propósito: lo que se
// afirma abajo no es una peculiaridad del recordatorio del plazo, es la FORMA que
// hace idempotente a cualquier recordatorio de aquí, y una regla vale más que dos
// tests que dicen lo mismo por separado.
type casDelRecordatorio struct {
	nombre string
	sql    string
	// marca es la columna que ESE compare-and-swap se gana. Es lo único que puede
	// aparecer en su SET.
	marca string
}

func losDosCAS() []casDelRecordatorio {
	return []casDelRecordatorio{
		{"recordatorio del PLAZO (T4.5)", markExpiryRemindedQuery, "expiry_reminded_at"},
		{"recordatorio de la SEÑA (T4.4)", markDepositRemindedQuery, "deposit_reminded_at"},
	}
}

// TestCAS_LaIdempotenciaViveEnElWHEREYNoEnUnIfDeGo es el candado del hueco descrito
// arriba: el `<marca> IS NULL` tiene que estar en la sentencia.
//
// Entre un `if` de Go y el UPDATE caben otro toque y otra pestaña del dueño; la
// garantía de «un solo recordatorio» solo la puede dar la BASE, y solo la da si la
// condición viaja en el WHERE.
func TestCAS_LaIdempotenciaViveEnElWHEREYNoEnUnIfDeGo(t *testing.T) {
	t.Parallel()
	for _, cas := range losDosCAS() {
		t.Run(cas.nombre, func(t *testing.T) {
			t.Parallel()
			guarda := cas.marca + " IS NULL"
			if !strings.Contains(whereDe(t, cas), guarda) {
				t.Fatalf("el WHERE de %s ya no lleva %q.\n\n"+
					"Es LA cláusula que hace idempotente el recordatorio: sin ella, cada lectura del "+
					"dueño vuelve a ganar el compare-and-swap y el aviso se convierte en un goteo. Y "+
					"NO lo cazaría ningún test de conducta: todos corren sobre MemoryStore, que "+
					"implementa el CAS por su cuenta.\nsql=%s", cas.nombre, guarda, cas.sql)
			}
		})
	}
}

// TestCAS_NoTocaUpdatedAtNiElEstado. Un recordatorio no cambia la solicitud: no la
// mueve de estado y no la marca como recién tocada.
//
// 🔴 EN EL DEL PLAZO ES ADEMÁS LA DECISIÓN nº 1 DE LA TAREA, y su fallo es mudo:
// `updated_at` es la BASE del plazo (quoteDeadlineOf), así que escribirlo aquí
// reiniciaría el plazo que este mismo UPDATE acaba de constatar como vencido — la
// marca «vencido» de la bandeja se apagaría en el mismo instante en que sale el aviso,
// y el dueño vería desaparecer justo lo que se le acaba de reportar.
//
// En el de la seña la razón es la de su propio comentario: marcar updated_at pondría
// toda la bandeja como recién tocada por mensajes que el dueño no mandó.
func TestCAS_NoTocaUpdatedAtNiElEstado(t *testing.T) {
	t.Parallel()
	for _, cas := range losDosCAS() {
		t.Run(cas.nombre, func(t *testing.T) {
			t.Parallel()
			set := setDe(t, cas)

			// UNA sola asignación. La coma es la señal: en un SET separa columnas.
			if strings.Contains(set, ",") {
				t.Fatalf("el SET de %s asigna más de una columna: %q.\n"+
					"Un compare-and-swap de recordatorio escribe SU marca y nada más", cas.nombre, set)
			}
			if !strings.Contains(set, cas.marca+" =") {
				t.Fatalf("el SET de %s ya no escribe %q: %q", cas.nombre, cas.marca, set)
			}
			for _, prohibida := range []string{"updated_at", "status"} {
				if strings.Contains(set, prohibida) {
					t.Fatalf("el SET de %s escribe %q: %q.\n\n"+
						"updated_at significa «la solicitud cambió» —y en el recordatorio del plazo es "+
						"además la BASE del plazo, así que tocarlo apaga la marca justo al encenderla—. "+
						"El estado no se mueve NUNCA por tiempo (D-041.16)", cas.nombre, prohibida, set)
				}
			}
		})
	}
}

// TestCAS_ElFiltroDeEstadoYElAislamientoPorTenantSiguenEnElWHERE.
//
// El de ESTADO no es decorativo: si el dueño ya decidió —aprobó, rechazó, pidió
// información— la fila deja de casar y no se le recuerda algo que ya hizo.
// El de TENANT es INV-8, y no puede faltar en ninguna sentencia de este repo.
func TestCAS_ElFiltroDeEstadoYElAislamientoPorTenantSiguenEnElWHERE(t *testing.T) {
	t.Parallel()
	for _, cas := range losDosCAS() {
		t.Run(cas.nombre, func(t *testing.T) {
			t.Parallel()
			where := whereDe(t, cas)
			for _, guarda := range []string{"status = ANY(", "tenant_id = $1"} {
				if !strings.Contains(where, guarda) {
					t.Fatalf("el WHERE de %s ya no lleva %q.\nsql=%s", cas.nombre, guarda, cas.sql)
				}
			}
		})
	}
}

// TestCASDelPlazo_ComparaElPlazoContraElCorteQueRecibe es lo específico del plazo: su
// WHERE no compara contra una columna de vencimiento —no existe, el plazo es una
// constante de plataforma— sino contra un CORTE que le manda el llamante.
func TestCASDelPlazo_ComparaElPlazoContraElCorteQueRecibe(t *testing.T) {
	t.Parallel()
	where := whereDe(t, losDosCAS()[0])
	if !strings.Contains(where, "updated_at <= $5") {
		t.Fatalf("el WHERE del recordatorio del plazo ya no compara updated_at contra el corte "+
			"($5).\n\nEl plazo NO es una columna (D-044.50 §1): es una constante de plataforma que "+
			"vive en Go, y el llamante manda ya restada la fecha a partir de la cual una solicitud "+
			"lleva demasiado esperando.\nwhere=%s", where)
	}
	// Y no puede haberse colado una columna de vencimiento por la puerta de atrás.
	if strings.Contains(where, "expiry_due_at") || strings.Contains(where, "order_ttl") {
		t.Fatalf("el WHERE del plazo consulta una columna de vencimiento: %s.\n"+
			"El plazo es una CONSTANTE (D-044.50 §1); no nace columna y no se reusa la derogada", where)
	}
}

// TestPlazo_ElPreFiltroDeGoYElCorteDeSQLSonLaMISMADesigualdad es el test que ata las
// dos mitades, y el único que puede.
//
// El pre-filtro pregunta HACIA DELANTE (`updated_at + plazo <= at`, quoteDeadlineOf) y
// el WHERE pregunta HACIA ATRÁS (`updated_at <= at - plazo`, cutoffDelPlazo). Son la
// misma desigualdad despejada de dos formas, así que **pueden divergir en el signo sin
// que nada se queje** — y un signo invertido en el corte aceptaría casi cualquier fila
// en el CAS, sin que se notara en el camino normal porque el pre-filtro ya habría
// descartado lo que no toca.
//
// Aquí se comprueban las dos a la vez sobre el MISMO instante, barriendo el borde
// minuto a minuto: si una se mueve y la otra no, esto se pone rojo.
func TestPlazo_ElPreFiltroDeGoYElCorteDeSQLSonLaMISMADesigualdad(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 6, 15, 0, 0, 0, time.UTC)
	corte := cutoffDelPlazo(at)

	desacuerdos := 0
	for minutos := -180; minutos <= 180; minutos++ {
		in := Intake{Status: StatusPendingApproval, UpdatedAt: corte.Add(time.Duration(minutos) * time.Minute)}

		// Lo que decide el pre-filtro de Go…
		segúnGo := Overdue(in, at)
		// …y lo que decidiría el WHERE con el corte que se le manda (`updated_at <= $5`).
		segúnSQL := !in.UpdatedAt.After(corte)

		if segúnGo != segúnSQL {
			desacuerdos++
			if desacuerdos == 1 {
				t.Errorf("🔴 el pre-filtro de Go y el corte que viaja al SQL discrepan en "+
					"updated_at = corte%+d min: Go dice vencido=%v y el WHERE diría %v.\n\n"+
					"Son la MISMA desigualdad despejada de dos maneras y tienen que dar lo mismo "+
					"siempre. Si divergen, el compare-and-swap acepta filas que la bandeja no marca "+
					"(o al revés) y el recordatorio deja de corresponderse con lo que el dueño ve",
					minutos, segúnGo, segúnSQL)
			}
		}
	}
	if desacuerdos > 0 {
		t.Fatalf("hay %d instantes en los que las dos mitades del plazo no dicen lo mismo", desacuerdos)
	}

	// Control anti-hueco: el barrido tiene que haber visto los DOS lados del borde.
	// Sin esto, un Overdue que devolviera siempre lo mismo que la otra expresión
	// —porque las dos estuvieran rotas igual— pasaría, y también pasaría un barrido
	// que no probara ni un caso `true`.
	if !Overdue(Intake{Status: StatusPendingApproval, UpdatedAt: corte.Add(-time.Minute)}, at) {
		t.Fatal("el barrido no llegó a ver un caso VENCIDO: no está probando el borde")
	}
	if Overdue(Intake{Status: StatusPendingApproval, UpdatedAt: corte.Add(time.Minute)}, at) {
		t.Fatal("el barrido no llegó a ver un caso EN PLAZO: no está probando el borde")
	}
}

// setDe recorta la cláusula SET de la sentencia: lo que hay entre `SET ` y `WHERE `.
// hasta / desde se validan a propósito — un recorte que falla en silencio devolvería
// la cadena vacía, y entonces todas las comprobaciones de «no contiene» pasarían por
// no mirar nada. Ése es el modo de fallo de este fichero entero.
func setDe(t *testing.T, cas casDelRecordatorio) string {
	t.Helper()
	return recortar(t, cas, "SET ", "WHERE ")
}

// whereDe recorta la cláusula WHERE: entre `WHERE ` y `RETURNING`.
func whereDe(t *testing.T, cas casDelRecordatorio) string {
	t.Helper()
	return recortar(t, cas, "WHERE ", "RETURNING")
}

// recortar devuelve el trozo de sentencia entre dos marcas, o CORTA el test si no
// están donde se cree. Es el anti-hueco: sin él, una consulta reescrita —o un UPDATE
// convertido en otra cosa— dejaría este fichero verde vigilando una cadena vacía.
func recortar(t *testing.T, cas casDelRecordatorio, desde, hasta string) string {
	t.Helper()
	if !strings.Contains(cas.sql, "UPDATE public.intakes") {
		t.Fatalf("%s ya no es un UPDATE sobre public.intakes: este candado está mirando otra "+
			"cosa y su verde no vale nada.\nsql=%s", cas.nombre, cas.sql)
	}
	i, j := strings.Index(cas.sql, desde), strings.Index(cas.sql, hasta)
	if i < 0 || j < 0 || j <= i {
		t.Fatalf("no se pudo recortar %q…%q de %s (índices %d y %d). La sentencia se reescribió: "+
			"arregla este recorte ANTES de fiarte de su verde.\nsql=%s",
			desde, hasta, cas.nombre, i, j, cas.sql)
	}
	trozo := strings.TrimSpace(cas.sql[i+len(desde) : j])
	if trozo == "" {
		t.Fatalf("el recorte %q…%q de %s salió VACÍO", desde, hasta, cas.nombre)
	}
	return trozo
}
