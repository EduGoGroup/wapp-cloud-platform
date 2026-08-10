package events_test

// Integración de la Ola 4.5 (T4.5.4 y T4.5.7, lado store) contra Postgres REAL.
//
// Aquí se prueba lo que la FK invertida y la vista cambian de verdad: el paquete
// events ya no conoce a los hijos, y aun así responde por su contenido — el
// predicado y el filtro `content` van sobre public.event_content (D-043.22), y la
// ligadura la declara el HIJO con intakes.event_id (D-043.21). Los datos llevan el
// prefijo de sesión `t45e-` y el andamiaje limpia sus intakes con t.Cleanup
// (dentro de insertarIntake).
//
// Reusa los helpers de store_integration_test.go (mismo paquete): openTestDB,
// seedTenant, nuevoStore, mustCrear, insertarIntake, mustRescatables, leerEntrada,
// leerSeqs y contarEntradas.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
)

// TestIntegration_T45E_ContentFiltrosSobreLaVista recorre los tres valores del
// filtro `content` contra los CUATRO estados que la base puede tener: contenido
// open (vivo), abandoned (muerto), settled (cuajado) y sin contenido. La vista
// hace el mapeo open→alive / abandoned→discarded / resto→settled del lado del
// hijo; aquí solo se afirma sobre el vocabulario genérico.
//
// El criterio de T4.5.4 en una frase: `content=alive` devuelve el evento con
// pedido vivo y `content=none` deja de mentir — ninguno de los dos trae al
// descartado ni al cuajado, tampoco `any` (INV-17).
func TestIntegration_T45E_ContentFiltrosSobreLaVista(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	store, reloj := nuevoStore(t, db, time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC))
	const sesion = "t45e-content"

	// Cuatro eventos vivos de tipos distintos (el único parcial E-2 es por tipo),
	// cada uno con su contenido en un estado distinto — y uno sin contenido.
	conVivo := mustCrear(ctx, t, store, nuevoEvento(tenantID, sesion, contactoA, "cart"))
	vivoRef := insertarIntake(ctx, t, db, tenantID, sesion, contactoA, "open", conVivo.ID)
	reloj.avanzar(time.Minute)
	conMuerto := mustCrear(ctx, t, store, nuevoEvento(tenantID, sesion, contactoA, "taller"))
	insertarIntake(ctx, t, db, tenantID, sesion, contactoA, "abandoned", conMuerto.ID)
	reloj.avanzar(time.Minute)
	conCuajado := mustCrear(ctx, t, store, nuevoEvento(tenantID, sesion, contactoA, "reserva"))
	insertarIntake(ctx, t, db, tenantID, sesion, contactoA, "settled", conCuajado.ID)
	reloj.avanzar(time.Minute)
	sinContenido := mustCrear(ctx, t, store, nuevoEvento(tenantID, sesion, contactoA, "survey"))

	casos := []struct {
		content events.ContentFilter
		quiero  []string
	}{
		{events.ContentNone, []string{sinContenido.ID}},
		{events.ContentAlive, []string{conVivo.ID}},
		// `any` son las dos mitades VIVAS, no «todo»: ni discarded ni settled.
		{events.ContentAny, []string{sinContenido.ID, conVivo.ID}},
	}
	for _, c := range casos {
		got := mustListarEventos(ctx, t, store, tenantID, events.ListFilter{Content: c.content})
		ids := idsPagina(got)
		slices.Sort(ids)
		quiero := slices.Clone(c.quiero)
		slices.Sort(quiero)
		if !slices.Equal(ids, quiero) {
			t.Fatalf("content=%s devolvió %v; quiero %v", c.content, ids, quiero)
		}
		if got.Total != len(quiero) {
			t.Fatalf("content=%s: total=%d, quiero %d (el conteo reusa la misma pieza)",
				c.content, got.Total, len(quiero))
		}
	}

	// El listado expone el contenido DERIVADO del join: estado y ref de la vista
	// para quien lo tiene, y las dos cadenas vacías para quien no.
	pagina := mustListarEventos(ctx, t, store, tenantID, events.ListFilter{})
	porID := map[string]events.Rescuable{}
	for _, ev := range pagina.Events {
		porID[ev.ID] = ev
	}
	if got := porID[conVivo.ID]; got.ContentState != "alive" || got.ContentRef != vivoRef {
		t.Fatalf("el evento con pedido vivo debe derivar (alive, %s); got (%q, %q)",
			vivoRef, got.ContentState, got.ContentRef)
	}
	if got := porID[sinContenido.ID]; got.ContentState != "" || got.ContentRef != "" {
		t.Fatalf("el evento sin contenido deriva cadenas vacías; got (%q, %q)",
			got.ContentState, got.ContentRef)
	}
}

// TestIntegration_T45E_RescatablesConContenidoMuertoFuera es INV-17 medido sobre
// la vista: los eventos cuyo contenido está `discarded` o `settled` siguen VIVOS
// en conversation_events (nadie los mató) y aun así no se listan, no se rescatan
// y no se mencionan. Lo que los tapa es el predicado sobre event_content, sin que
// este paquete sepa qué tabla hay detrás.
func TestIntegration_T45E_RescatablesConContenidoMuertoFuera(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	store, reloj := nuevoStore(t, db, time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC))
	const sesion = "t45e-rescate"

	conVivo := mustCrear(ctx, t, store, nuevoEvento(tenantID, sesion, contactoA, "cart"))
	insertarIntake(ctx, t, db, tenantID, sesion, contactoA, "open", conVivo.ID)
	reloj.avanzar(time.Minute)
	conMuerto := mustCrear(ctx, t, store, nuevoEvento(tenantID, sesion, contactoA, "taller"))
	insertarIntake(ctx, t, db, tenantID, sesion, contactoA, "abandoned", conMuerto.ID)
	reloj.avanzar(time.Minute)
	conCuajado := mustCrear(ctx, t, store, nuevoEvento(tenantID, sesion, contactoA, "reserva"))
	insertarIntake(ctx, t, db, tenantID, sesion, contactoA, "settled", conCuajado.ID)
	reloj.avanzar(time.Minute)
	sinContenido := mustCrear(ctx, t, store, nuevoEvento(tenantID, sesion, contactoA, "survey"))

	quiero := sinContenido.ID + "," + conVivo.ID // última actividad DESC
	if got := idsRescatables(mustRescatables(ctx, t, store, tenantID, sesion, contactoA, 0)); got != quiero {
		t.Fatalf("rescatables = %s; quiero %s (contenido muerto o cuajado FUERA, INV-17)", got, quiero)
	}

	// Y los tapados siguen vivos en la tabla: los tapa el predicado, no un UPDATE.
	for _, id := range []string{conMuerto.ID, conCuajado.ID} {
		if leerCruda(ctx, t, db, id).status != "open" {
			t.Fatalf("el evento %s debía seguir open: la consulta no mata nada", id)
		}
	}
}

// TestIntegration_T45E_AppendDecision es el lado store de T4.5.7: la decisión del
// cliente entra al hilo como nivel 1 —estructura EN CLARO, `entry_kind='decision'`,
// `role='client'`— numerada por appendEntry en el MISMO seq que las demás
// entradas, y la prosa no tiene por dónde entrar (JSON inválido se rechaza sin
// escribir nada).
func TestIntegration_T45E_AppendDecision(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	store, _ := nuevoStore(t, db, time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC))

	ev := mustCrear(ctx, t, store, nuevoEvento(tenantID, "t45e-decision", contactoA, "cart"))

	// Un resumen primero: la numeración es del EVENTO, no del tipo de entrada.
	if _, err := store.AppendSummary(ctx, ev.ID, []byte(`{"lines":[]}`)); err != nil {
		t.Fatalf("AppendSummary: %v", err)
	}
	decisiones := []string{
		`{"action":"add_line","sku":"TORTA-CHOCO","qty":1}`,
		`{"action":"remove_line","sku":"TORTA-CHOCO"}`,
	}
	for i, d := range decisiones {
		if err := store.AppendDecision(ctx, ev.ID, []byte(d)); err != nil {
			t.Fatalf("AppendDecision #%d: %v", i, err)
		}
	}

	// El orden: seq 1..3 contiguos, con las decisiones en 2 y 3.
	if seqs := leerSeqs(ctx, t, db, ev.ID); !slices.Equal(seqs, []int{1, 2, 3}) {
		t.Fatalf("seqs del hilo = %v; quiero [1 2 3] (la decisión se numera con los demás)", seqs)
	}
	for i, d := range decisiones {
		quieroDecisionEnClaro(ctx, t, db, ev.ID, i+2, d)
	}

	// La prosa no entra: JSON inválido se rechaza con el sentinela del nivel 1 y
	// sin escribir ni una fila.
	if err := store.AppendDecision(ctx, ev.ID, []byte("quiero una torta")); !errors.Is(err, events.ErrSummaryNotJSON) {
		t.Fatalf("AppendDecision con prosa = %v; quiero ErrSummaryNotJSON", err)
	}
	if n := contarEntradas(ctx, t, db, ev.ID); n != 3 {
		t.Fatalf("el rechazo escribió filas: hay %d entradas, quiero 3", n)
	}
}

// quieroDecisionEnClaro afirma de una vez el grado completo de una fila
// `decision`: la voz es del CLIENTE (la decisión es suya), el payload es JSON en
// claro con la MISMA estructura sembrada, y NINGÚN cuerpo cifrado — INV-11: nada
// nuestro entra como decision.
func quieroDecisionEnClaro(ctx context.Context, t *testing.T, db *sql.DB,
	eventID string, seq int, sembrado string) {
	t.Helper()
	fila := leerEntrada(ctx, t, db, eventID, seq)
	if fila.entryKind != "decision" || fila.role != "client" {
		t.Fatalf("seq %d quedó como (%s, %s); quiero (decision, client)", seq, fila.entryKind, fila.role)
	}
	if !fila.payload.Valid || !json.Valid([]byte(fila.payload.String)) {
		t.Fatalf("la decisión debe llevar payload JSON en claro; got %+v", fila.payload)
	}
	// jsonb normaliza orden de claves y espacios: se compara la ESTRUCTURA, no el
	// string — que es justo lo que el nivel 1 persiste.
	var got, quiero any
	if err := json.Unmarshal([]byte(fila.payload.String), &got); err != nil {
		t.Fatalf("payload de seq %d no parsea: %v", seq, err)
	}
	if err := json.Unmarshal([]byte(sembrado), &quiero); err != nil {
		t.Fatalf("decisión sembrada no parsea: %v", err)
	}
	if !reflect.DeepEqual(got, quiero) {
		t.Fatalf("payload de seq %d = %s; quiero la estructura de %s", seq, fila.payload.String, sembrado)
	}
	if fila.bodyEnc != nil || fila.bodyDEK != nil || fila.bodyKEKID.Valid {
		t.Fatalf("una decisión JAMÁS lleva cuerpo cifrado (nivel 1): %+v", fila)
	}
}
