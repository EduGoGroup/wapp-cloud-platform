package pipeline

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
)

// plaza_test.go — EL ENTERO, MEDIDO (Plan 044 · Ola 2 · T2.7, ADR-0046 Mecanismo 1).
//
// # 🔴 AQUÍ NO SE MIDE NINGÚN TIEMPO, Y NO ES UNA PREFERENCIA DE ESTILO
//
// Un test de aforo escrito con `sleep` y cronómetro —«lanzo dos, duermo 50 ms, miro si
// se pisaron»— es FLAKY POR CONSTRUCCIÓN: pasa el 85 % de las veces y esta misma ola
// ya pagó uno (9 fallos de 60). Lo que se registra aquí son SOLAPAMIENTOS y CUENTAS:
// cada llamada falsa anota que ha entrado, y si en algún instante hay más de UNA
// dentro de la misma plaza, eso queda escrito con nombre y apellidos y el test falla
// al primero.
//
// El único test que mira el reloj es el del criterio (f), porque su criterio ES un
// plazo («< 100 ms»); y hasta ese está construido para que el reloj no sea lo que
// decide: el ticker y el backoff valen DIEZ MINUTOS, así que ninguna reanudación
// dentro del test puede venir de ellos.
//
// # LA SINCRONIZACIÓN, SIN DORMIR
//
// Todos los escenarios paran en el mismo sitio: `esperarHasta` gira hasta que TODAS
// las cadenas lanzadas están CONTADAS —dentro de una llamada, o bloqueadas pidiendo
// plaza—, y solo entonces se afirma. Es una condición POSITIVA (llega siempre, con
// aforo y sin él), así que una mutación produce un FALLO limpio y no un cuelgue.

// ---------------------------------------------------------------------------
// LOS DOBLES
// ---------------------------------------------------------------------------

// rutasFalsas es el doble de Plazas: dice a qué Edge apunta cada sesión. En
// producción lo satisface *llmvia.Selector, que además sabe que un tenant en vía API
// NO OCUPA PLAZA — aquí eso se modela con `sinPlaza`.
type rutasFalsas struct {
	mu        sync.Mutex
	porSesion map[string]string
	sinPlaza  map[string]bool
	err       error
}

func (r *rutasFalsas) PlazaDe(_ context.Context, tenantID, sessionID string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return "", false, r.err
	}
	if r.sinPlaza[tenantID] {
		return "", false, nil
	}
	edge, hay := r.porSesion[sessionID]
	return edge, hay, nil
}

// llamadaFalsa es UNA llamada al modelo detenida a mitad. El test la deja seguir
// cuando quiere, y así el escenario avanza por pasos y no por tiempo.
type llamadaFalsa struct {
	jobID  string
	etapa  string
	plaza  Plaza
	seguir chan struct{}
}

// vigía es el provider falso que REGISTRA SOLAPAMIENTOS. Las tres etapas pasan por
// él, así que «una llamada en vuelo» cubre la cadena entera y no solo P2.
//
// 🔴 P2 Y P4 FRENAN, P3 NO, y la elección tiene un motivo concreto: si el aforo
// soltara la plaza al acabar P2 en vez de al acabar la CADENA, el segundo job entraría
// en su P2 mientras el primero sigue dentro de su P4 — y eso es un solapamiento que
// este montaje ve. Con solo P2 frenando, esa mutación pasaría desapercibida.
type vigia struct {
	mu      sync.Mutex
	enVuelo map[Plaza]int
	dentro  int
	solapes []string

	rutas    map[string]string
	store    stages.StageStore
	llegadas chan *llamadaFalsa
}

func nuevaVigia(store stages.StageStore, rutas map[string]string) *vigia {
	return &vigia{
		enVuelo:  map[Plaza]int{},
		rutas:    rutas,
		store:    store,
		llegadas: make(chan *llamadaFalsa, 64),
	}
}

// entrar anota la llamada, registra el solapamiento si lo hay, y —si `frena`— la deja
// detenida hasta que el test la libere. Devuelve la función de salida.
//
// 🔴 EL UMBRAL ES EL LITERAL 1, NO `KPorPlaza`. Comparar contra la constante que este
// test existe para custodiar lo volvería tautológico: subir el entero a 2 relajaría
// también la comprobación y el test seguiría verde midiendo otra cosa. El 1 de aquí es
// el del enunciado del criterio (a) —«NUNCA una llamada en vuelo a la vez»—, no el del
// código.
func (v *vigia) entrar(job intake.ClaimedJob, etapa string, frena bool) func() {
	p := Plaza{TenantID: job.Key.TenantID, EdgeID: v.rutas[job.Key.SessionID]}

	v.mu.Lock()
	v.enVuelo[p]++
	v.dentro++
	if v.enVuelo[p] > 1 {
		v.solapes = append(v.solapes, fmt.Sprintf(
			"plaza %s: %d llamadas en vuelo a la vez (job %s, etapa %s)",
			p, v.enVuelo[p], job.ID, etapa))
	}
	v.mu.Unlock()

	if frena {
		c := &llamadaFalsa{jobID: job.ID, etapa: etapa, plaza: p, seguir: make(chan struct{})}
		v.llegadas <- c
		<-c.seguir
	}
	return func() {
		v.mu.Lock()
		v.enVuelo[p]--
		v.dentro--
		v.mu.Unlock()
	}
}

// enVueloAhora son las llamadas dentro de una etapa en este instante.
func (v *vigia) enVueloAhora() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.dentro
}

// exigeCeroSolapes falla listando TODOS los solapamientos registrados, no solo el
// primero: si el aforo está mal, saber cuántas veces se pisaron y en qué etapas es la
// mitad del diagnóstico.
func (v *vigia) exigeCeroSolapes(t *testing.T) {
	t.Helper()
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.solapes) > 0 {
		t.Fatalf("dos cadenas de lote del MISMO Edge se solaparon (%d veces):\n  %v",
			len(v.solapes), v.solapes)
	}
}

// liberar deja seguir a las siguientes `n` llamadas que lleguen.
func (v *vigia) liberar(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case c := <-v.llegadas:
			close(c.seguir)
		case <-time.After(5 * time.Second):
			t.Fatalf("se esperaban %d llamadas y solo llegaron %d", n, i)
		}
	}
}

// Las tres etapas del vigía. Persisten su artefacto de verdad (guardar), igual que las
// de dobles_test.go: así la máquina de estados corre entera y el job puede terminar.

type p2Vigilada struct{ v *vigia }

func (e *p2Vigilada) Run(ctx context.Context, job intake.ClaimedJob, _ string) (*llm.MainIdeas, error) {
	defer e.v.entrar(job, intake.StageP2, true)()
	art := &llm.MainIdeas{Version: llm.ArtifactVersion,
		Wants: []llm.Want{{Idea: "torta", Evidence: "torta"}}}
	return art, guardar(ctx, e.v.store, job.ID, intake.StageP2, art)
}

type p3Vigilada struct{ v *vigia }

func (e *p3Vigilada) Run(ctx context.Context, job intake.ClaimedJob, _ string, _ []llm.Want) (*stages.ArtefactoP3, error) {
	defer e.v.entrar(job, intake.StageP3, false)()
	art := &stages.ArtefactoP3{Version: llm.ArtifactVersion,
		Items: []llm.ItemSpec{{Product: "torta", Evidence: "torta"}}}
	return art, guardar(ctx, e.v.store, job.ID, intake.StageP3, art)
}

type p4Vigilada struct{ v *vigia }

func (e *p4Vigilada) Run(ctx context.Context, job intake.ClaimedJob, _ string,
	items []llm.ItemSpec, _ *llm.Hint) (*llm.Quantities, error) {
	defer e.v.entrar(job, intake.StageP4, true)()
	norm := make([]llm.NormalizedItem, 0, len(items))
	for range items {
		norm = append(norm, llm.NormalizedItem{Qty: 1})
	}
	art := &llm.Quantities{Version: llm.ArtifactVersion, Items: norm}
	return art, guardar(ctx, e.v.store, job.ID, intake.StageP4, art)
}

// ---------------------------------------------------------------------------
// EL MONTAJE
// ---------------------------------------------------------------------------

// plazoDelEscenario es el techo de paciencia de esperarHasta. No mide nada: es el
// límite tras el cual se declara que la condición NO va a llegar, para que una
// mutación produzca un fallo con diagnóstico en vez de un cuelgue hasta el -timeout.
const plazoDelEscenario = 5 * time.Second

// esperarHasta gira hasta que `cond` se cumple, cediendo el procesador en cada vuelta.
// No duerme: la condición que se le pasa siempre es POSITIVA y alcanzable —«las N
// cadenas ya están contadas»—, así que en la práctica termina en microsegundos.
func esperarHasta(t *testing.T, que string, cond func() bool) {
	t.Helper()
	limite := time.Now().Add(plazoDelEscenario)
	for !cond() {
		if time.Now().After(limite) {
			t.Fatalf("nunca se llegó a: %s", que)
		}
		runtime.Gosched()
	}
}

// banquillo es N cadenas de lote lanzadas a la vez contra un aforo compartido.
type banquillo struct {
	store *StoreEnMemoria
	aforo *Aforo
	vigia *vigia
	rutas *rutasFalsas
	// rel es el reloj compartido. Lo guarda el banquillo desde T3.8 porque las etapas
	// de la Ola 3 —que no vigilan la plaza pero sí tienen que existir— se construyen
	// con él, y dos relojes distintos en el mismo worker es un defecto documentado.
	rel    *reloj
	espera sync.WaitGroup
}

// montar arma el escenario: un job por entrada de `sesiones` (sesión -> Edge), un
// worker por job —que es lo que modela varias goroutines/réplicas del worker sobre la
// misma cola— y el aforo compartido entre todos.
func montar(t *testing.T, sesiones map[string]string) *banquillo {
	t.Helper()
	rel := nuevoReloj()
	store := NuevoStoreEnMemoria(rel.ahora)
	b := &banquillo{
		store: store,
		aforo: NuevoAforo(KPorPlaza),
		vigia: nuevaVigia(store, sesiones),
		rutas: &rutasFalsas{porSesion: sesiones},
		rel:   rel,
	}
	for sesion := range sesiones {
		store.Sembrar(Fila{
			ID: "job-" + sesion,
			Key: intake.WindowKey{TenantID: "tenant-1", SessionID: sesion,
				ContactID: "contacto-1", EventID: "11111111-1111-1111-1111-111111111111"},
			SourceText: intake.SourceText{Enc: []byte("cifrado"), DEK: []byte("dek"), KEKID: "kek-1"},
			MessageTS:  rel.ahora(),
			CreatedAt:  rel.ahora(),
		})
	}
	return b
}

// lanzar arranca una cadena por job sembrado. Cada una en su propio Worker, que es
// como conviven de verdad: el aforo es lo ÚNICO que comparten.
func (b *banquillo) lanzar(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		m, d, cat := olaTres(t, b.rel, b.store)
		w, err := NewWorker(&captor{}, b.store,
			&p2Vigilada{v: b.vigia}, &p3Vigilada{v: b.vigia}, &p4Vigilada{v: b.vigia},
			m, d, cat, cifraFalsa{}, Config{}, ConAforo(b.aforo, b.rutas))
		if err != nil {
			t.Fatalf("cablear el worker %d: %v", i, err)
		}
		b.espera.Add(1)
		go func() {
			defer b.espera.Done()
			if _, err := w.UnaVuelta(context.Background()); err != nil {
				t.Errorf("UnaVuelta: %v", err)
			}
		}()
	}
}

// todasContadas es la condición de sincronía: las `n` cadenas están o dentro de una
// llamada, o bloqueadas pidiendo plaza. Ninguna se ha quedado a medio camino.
func (b *banquillo) todasContadas(n int) func() bool {
	return func() bool { return b.vigia.enVueloAhora()+b.aforo.Esperando() >= n }
}

// ---------------------------------------------------------------------------
// (a) + (c) — DOS CADENAS DEL MISMO EDGE
// ---------------------------------------------------------------------------

// TestAforo_DosCadenasDelMismoEdgeNuncaSeSolapan es el criterio (a) de T2.7, y de
// paso el (c).
//
// (a) Dos jobs de Nivel C del MISMO Edge NUNCA tienen una llamada en vuelo a la vez.
// Se afirma en el instante en que las dos cadenas están contadas: una dentro de su
// llamada, la otra esperando plaza. Y al final se exige que el registro de
// solapamientos —que vigila las TRES etapas durante toda la corrida— esté vacío.
//
// (c) El segundo job NO queda `failed` por haber esperado, y ADEMÁS no se le cobra el
// intento: esperar no es fallar. `attempts` en 0 es lo que distingue «esperó» de «se
// reencoló castigado», y es lo que rompería un `TryTomar`+`Release` disfrazado de
// aforo.
//
// 🔬 MUTACIÓN EJECUTADA (roja): `KPorPlaza = 2` ⇒ las dos cadenas entran a la vez, el
// registro de solapamientos se llena y `enVueloAhora()` vale 2.
func TestAforo_DosCadenasDelMismoEdgeNuncaSeSolapan(t *testing.T) {
	t.Parallel()
	b := montar(t, map[string]string{"sess-1": "edge-1", "sess-2": "edge-1"})
	b.lanzar(t, 2)

	esperarHasta(t, "las dos cadenas contadas", b.todasContadas(2))
	if n := b.vigia.enVueloAhora(); n != 1 {
		t.Fatalf("hay %d llamadas en vuelo sobre la MISMA plaza; el enunciado dice UNA", n)
	}
	if e := b.aforo.Esperando(); e != 1 {
		t.Fatalf("cadenas esperando plaza = %d; la segunda tenía que estar esperando, no corriendo", e)
	}

	// Dos frenos por cadena (P2 y P4), dos cadenas.
	b.vigia.liberar(t, 4)
	b.espera.Wait()
	b.vigia.exigeCeroSolapes(t)

	for _, id := range []string{"job-sess-1", "job-sess-2"} {
		f, ok := b.store.Ver(id)
		if !ok {
			t.Fatalf("la fila %s no existe", id)
		}
		if f.Status != intake.StatusDone {
			t.Fatalf("%s quedó en %q (error=%q); esperar NO es fallar", id, f.Status, f.Error)
		}
		if f.Attempts != 0 {
			t.Fatalf("%s consumió %d intentos; haber esperado plaza no cuesta intentos", id, f.Attempts)
		}
	}
}

// ---------------------------------------------------------------------------
// (b) — DOS EDGES DISTINTOS **SÍ** SE SOLAPAN
// ---------------------------------------------------------------------------

// TestAforo_DosEdgesDistintosSiSeSolapan es el criterio (b), y es el que custodia la
// MITAD que el enunciado de la mañana no tenía: el entero es POR EDGE, no por proceso.
//
// 🔴 ESTE TEST EXIGE EL SOLAPAMIENTO, NO LO TOLERA. Si el segundo Edge espera al
// primero, el aforo está indexado por algo global y este test tiene que estar ROJO:
// un `K = 1` de proceso serializaría los presupuestos de todos los clientes detrás del
// más lento, que es exactamente lo que D7-b corrigió.
//
// La condición de sincronía es la misma que en (a) —las dos cadenas contadas— y por
// eso el fallo es limpio en los dos sentidos: con el aforo global, una queda dentro y
// la otra esperando, la cuenta llega igual a 2 y la afirmación de abajo falla diciendo
// exactamente qué pasó.
//
// 🔬 MUTACIÓN EJECUTADA (roja): quitar la clave por Edge —volver a un entero global,
// p. ej. `Plaza{}` para todos en `tomarPlaza`— ⇒ `enVueloAhora()` vale 1 y una cadena
// se queda esperando.
func TestAforo_DosEdgesDistintosSiSeSolapan(t *testing.T) {
	t.Parallel()
	b := montar(t, map[string]string{"sess-1": "edge-1", "sess-2": "edge-2"})
	b.lanzar(t, 2)

	esperarHasta(t, "las dos cadenas contadas", b.todasContadas(2))
	if n := b.vigia.enVueloAhora(); n != 2 {
		t.Fatalf("solo %d llamada(s) en vuelo con DOS Edges distintos: el entero está por proceso, no por plaza "+
			"(esperando plaza = %d)", n, b.aforo.Esperando())
	}
	if e := b.aforo.Esperando(); e != 0 {
		t.Fatalf("%d cadena(s) esperando plaza con dos Edges ociosos; ninguna debería esperar", e)
	}

	b.vigia.liberar(t, 4)
	b.espera.Wait()
	b.vigia.exigeCeroSolapes(t)
}

// ---------------------------------------------------------------------------
// (d) — CUÁNTAS LLAMADAS DE LOTE PUEDE TENER DELANTE UN TURNO INTERACTIVO
// ---------------------------------------------------------------------------

// TestAforo_UnTurnoInteractivoNuncaTieneMasDeUnaLlamadaDeLoteDelante es el criterio
// (d), Y HAY QUE LEER LO QUE MIDE Y LO QUE NO, porque el enunciado del plan invita a
// medir un tiempo y aquí no se mide ninguno.
//
// # QUÉ DICE EL CRITERIO Y QUÉ SE PUEDE AFIRMAR DESDE EL CLOUD
//
// «El peor caso de espera de un turno interactivo es ≤ UNA llamada de lote, no N.» Esa
// espera tiene dos mitades y viven en repos distintos:
//
//  1. **Cuántas llamadas de lote hay por delante.** Es una propiedad del CLOUD: es
//     literalmente cuántas peticiones de lote ha soltado hacia esa plaza y siguen sin
//     terminar. Es lo que este test mide, y se mide CONTANDO, con N = 4 cadenas para
//     que el «N» del enunciado sea visible: sin el entero valdría 4.
//  2. **Que el interactivo entre por delante de la petición k+1 del lote.** Eso es el
//     despertar FIFO del aforo DEL EDGE (`servidor.go`, otro repo) y el ADR-0046 ya
//     escribe que debe fijarse con un test ALLÍ —y que si se pone rojo, la alternativa
//     del planificador se reabre—. Desde aquí no se puede afirmar, y este test no
//     pretende hacerlo.
//
// ⚠️ ASÍ QUE (d) ES (a) CONTADO, NO UN HECHO NUEVO — dicho aquí porque el criterio
// suena a otra cosa. Lo que aporta sobre (a) es el NÚMERO: (a) responde «¿se pisan?»
// (sí/no) con dos cadenas; (d) responde «¿cuántas hay delante?» con cuatro, que es la
// forma del enunciado. No es una tautología: falla con el entero en 2, y falla sin
// aforo.
//
// 🔬 MUTACIÓN EJECUTADA (roja): `KPorPlaza = 2` ⇒ dos llamadas de lote por delante.
func TestAforo_UnTurnoInteractivoNuncaTieneMasDeUnaLlamadaDeLoteDelante(t *testing.T) {
	t.Parallel()
	b := montar(t, map[string]string{
		"sess-1": "edge-1", "sess-2": "edge-1", "sess-3": "edge-1", "sess-4": "edge-1",
	})
	b.lanzar(t, 4)

	esperarHasta(t, "las cuatro cadenas contadas", b.todasContadas(4))

	porDelante := b.vigia.enVueloAhora()
	if porDelante > 1 {
		t.Fatalf("un turno interactivo encontraría %d llamadas de lote por delante sobre la misma plaza; "+
			"el entero existe para que sean 1 (esperando = %d)", porDelante, b.aforo.Esperando())
	}
	if e := b.aforo.Esperando(); e != 3 {
		t.Fatalf("esperando plaza = %d; con 4 cadenas y una sola plaza tenían que quedar 3 en fila", e)
	}

	b.vigia.liberar(t, 8)
	b.espera.Wait()
	b.vigia.exigeCeroSolapes(t)
}

// ---------------------------------------------------------------------------
// SIN PLAZA QUE TOMAR — LA VÍA API Y EL TENANT SIN EDGE
// ---------------------------------------------------------------------------

// TestAforo_SinPlazaNoSeSerializaNada fija la otra mitad del enunciado: por vía API el
// entero NO APLICA (allí el tope es de precio, no de capacidad) y un tenant sin Edge
// vivo tampoco ocupa nada.
//
// El worker no distingue los dos casos —ni debe: quien sabe la vía es el selector— así
// que aquí se prueba lo único que el worker puede hacer con un `ok = false`: no
// serializar. Dos cadenas del mismo tenant corren a la vez.
//
// 🔬 MUTACIÓN EJECUTADA (roja): tratar `!hay` como plaza válida en `tomarPlaza` (usar
// `p` sin comprobar `hay`) ⇒ las dos cadenas comparten la plaza `tenant-1/` y una
// espera.
func TestAforo_SinPlazaNoSeSerializaNada(t *testing.T) {
	t.Parallel()
	b := montar(t, map[string]string{"sess-1": "edge-1", "sess-2": "edge-1"})
	b.rutas.sinPlaza = map[string]bool{"tenant-1": true}
	b.lanzar(t, 2)

	esperarHasta(t, "las dos cadenas contadas", b.todasContadas(2))
	if n := b.vigia.enVueloAhora(); n != 2 {
		t.Fatalf("solo %d cadena(s) en vuelo: un tenant SIN plaza no debe serializarse (esperando = %d)",
			n, b.aforo.Esperando())
	}

	b.vigia.liberar(t, 4)
	b.espera.Wait()
}

// ---------------------------------------------------------------------------
// (f) — EL FLANCO A READY REANUDA SIN ESPERAR AL BACKOFF
// ---------------------------------------------------------------------------

// gatewayFalso es la MITAD DEL GATEWAY QUE IMPORTA AQUÍ: aprende lo que cada Edge dice
// en su latido y avisa SOLO en la transición a READY.
//
// Reproduce a propósito las dos guardas de `gatewaygrpc.anotaReadiness`, porque son
// las que hacen que el aviso signifique algo: el flanco (no el estado, que viaja en
// TODOS los latidos) y el silencio que no se lee como DOWN ni borra lo aprendido.
//
// La otra mitad —que el gateway REAL invoque el hook en ese mismo flanco— la fija
// `readiness_edge_ready_internal_test.go`, en su paquete y entrando por `route`, que
// es la puerta de verdad del frame.
type gatewayFalso struct {
	mu     sync.Mutex
	dice   map[Plaza]string
	avisar func(tenantID, edgeID string)
}

func nuevoGatewayFalso(avisar func(string, string)) *gatewayFalso {
	return &gatewayFalso{dice: map[Plaza]string{}, avisar: avisar}
}

// late mete un latido. `readiness` vacío es «este Edge no lo dice» (el cero del enum):
// ni se anota ni dispara nada.
func (g *gatewayFalso) late(tenantID, edgeID, readiness string) {
	p := Plaza{TenantID: tenantID, EdgeID: edgeID}
	g.mu.Lock()
	anterior := g.dice[p]
	flanco := false
	if readiness != "" {
		g.dice[p] = readiness
		flanco = readiness == "ready" && anterior != "ready"
	}
	g.mu.Unlock()
	if flanco {
		g.avisar(tenantID, edgeID)
	}
}

// TestDespertar_ElFlancoAREADYReanudaSinEsperarAlBackoff es el criterio (f) de T2.7
// (D-044.43).
//
// # EL MONTAJE ESTÁ HECHO PARA QUE NADA MÁS PUEDA EXPLICAR LA REANUDACIÓN
//
// El job está `pending` con `next_attempt_at` a DIEZ MINUTOS, y la cadencia del worker
// vale otros DIEZ MINUTOS. Ni el backoff ni el ticker pueden haberlo reanudado dentro
// del test: si el job termina, lo terminó el evento. Ese encuadre es lo que hace que
// el «< 100 ms» del criterio sea una comprobación y no una carrera contra el reloj.
//
// 🔴 Y EL BACKOFF SE QUEDA. El evento no lo sustituye: sigue siendo el barrendero del
// Edge vivo-pero-atascado, que es el caso que ningún flanco anuncia.
//
// 🔬 MUTACIÓN EJECUTADA (roja): quitar el `case p := <-w.despertares` del select de
// Run ⇒ el job sigue `pending` y el test falla al agotar su paciencia (reanudaría al
// vencer el backoff, dentro de diez minutos).
func TestDespertar_ElFlancoAREADYReanudaSinEsperarAlBackoff(t *testing.T) {
	t.Parallel()
	rel := nuevoReloj()
	store := NuevoStoreEnMemoria(rel.ahora)
	log := &captor{}

	const lejos = 10 * time.Minute
	store.Sembrar(Fila{
		ID: "job-dormido",
		Key: intake.WindowKey{TenantID: "tenant-1", SessionID: "sess-1",
			ContactID: "contacto-1", EventID: "11111111-1111-1111-1111-111111111111"},
		SourceText:    intake.SourceText{Enc: []byte("cifrado"), DEK: []byte("dek"), KEKID: "kek-1"},
		MessageTS:     rel.ahora(),
		CreatedAt:     rel.ahora(),
		NextAttemptAt: rel.ahora().Add(lejos),
	})

	p2 := &p2Falsa{etapaBase: etapaBase{rel: rel}, store: store,
		wants: []llm.Want{{Idea: "torta", Evidence: "torta"}}}
	p3 := &p3Falsa{etapaBase: etapaBase{rel: rel}, store: store,
		items: []llm.ItemSpec{{Product: "torta", Evidence: "torta"}}}
	p4 := &p4Falsa{etapaBase: etapaBase{rel: rel}, store: store}

	m, d, cat := olaTres(t, rel, store)
	w, err := NewWorker(log, store, p2, p3, p4, m, d, cat, cifraFalsa{}, Config{Cadencia: lejos})
	if err != nil {
		t.Fatalf("cablear el worker: %v", err)
	}
	w.ahora = rel.ahora

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// El primer Drenar de Run ya pasó: a partir de aquí, el worker está en su select y
	// el único ticker que tiene vale diez minutos.
	esperarHasta(t, "el worker preguntó por trabajo al arrancar", func() bool { return store.Claims() >= 1 })
	if f, _ := store.Ver("job-dormido"); f.Status != intake.StatusPending {
		t.Fatalf("el job arrancó en %q; su backoff no ha vencido, tenía que seguir pending", f.Status)
	}

	gw := nuevoGatewayFalso(w.Despertar)
	gw.late("tenant-1", "edge-1", "down")
	if f, _ := store.Ver("job-dormido"); f.Status != intake.StatusPending {
		t.Fatalf("un latido DOWN movió el job a %q; solo el flanco a READY reanuda", f.Status)
	}

	arranque := time.Now()
	gw.late("tenant-1", "edge-1", "ready")
	esperarHasta(t, "el job reanudado por el flanco a READY", func() bool {
		f, _ := store.Ver("job-dormido")
		return f.Status == intake.StatusDone
	})
	transcurrido := time.Since(arranque)

	if transcurrido > 100*time.Millisecond {
		t.Fatalf("la reanudación tardó %s; el criterio (f) pide < 100 ms", transcurrido)
	}
	if f, _ := store.Ver("job-dormido"); !f.NextAttemptAt.IsZero() && f.NextAttemptAt.Before(rel.ahora()) {
		t.Fatalf("el flanco ADELANTÓ la marca del backoff (%s); tiene que ignorarla, no borrarla", f.NextAttemptAt)
	}
}

// TestDespertar_NoReclamaDosVecesElMismoJobEnUnFlanco custodia el freno del bucle de
// DrenarDespierto, que es lo único que impide una tormenta.
//
// El claim del flanco IGNORA `next_attempt_at`. Un job que falla vuelve a `pending`
// castigado… y ese castigo no lo frena. Sin el conjunto de vistos, el mismo job se
// reclamaría, fallaría y se reclamaría a la velocidad del error, dentro de un bucle sin
// techo: la tormenta de la 0078, la misma que la mutación M1 de T2.5 dejó medida.
//
// 🔬 MUTACIÓN EJECUTADA (roja): quitar la comprobación de `vistos` ⇒ **10 pasadas en
// un solo flanco**, o sea el techo de intentos entero consumido en milisegundos y el
// job MUERTO. ⚠️ Y ese número corrige lo que este docstring decía antes: la mutación
// NO cuelga el proceso. Lo que la frena es `MaxIntentosInfra`, que estaba puesto para
// otra cosa —matar un job envenenado tras diez intentos ESPACIADOS— y aquí actúa como
// freno accidental. Un freno accidental no es un freno: con `MaxIntentosInfra` más
// alto, o con un job que fallara y volviera sin consumir intento, el bucle sí giraría
// sin fin. El conjunto de vistos es el freno de verdad.
func TestDespertar_NoReclamaDosVecesElMismoJobEnUnFlanco(t *testing.T) {
	t.Parallel()
	rel := nuevoReloj()
	store := NuevoStoreEnMemoria(rel.ahora)
	log := &captor{}

	store.Sembrar(Fila{
		ID: "job-envenenado",
		Key: intake.WindowKey{TenantID: "tenant-1", SessionID: "sess-1",
			ContactID: "contacto-1", EventID: "11111111-1111-1111-1111-111111111111"},
		SourceText:    intake.SourceText{Enc: []byte("cifrado"), DEK: []byte("dek"), KEKID: "kek-1"},
		MessageTS:     rel.ahora(),
		CreatedAt:     rel.ahora(),
		NextAttemptAt: rel.ahora().Add(10 * time.Minute),
	})

	// P2 falla SIEMPRE con un error de infraestructura: cada pasada devuelve el job a
	// la cola castigado, que es justo lo que el claim del flanco ignora.
	p2 := &p2Falsa{etapaBase: etapaBase{rel: rel, guion: []guionEtapa{{err: errorDeRedReal(t)}}}, store: store}
	p3 := &p3Falsa{etapaBase: etapaBase{rel: rel}, store: store}
	p4 := &p4Falsa{etapaBase: etapaBase{rel: rel}, store: store}

	m, d, cat := olaTres(t, rel, store)
	w, err := NewWorker(log, store, p2, p3, p4, m, d, cat, cifraFalsa{}, Config{Cadencia: time.Hour})
	if err != nil {
		t.Fatalf("cablear el worker: %v", err)
	}
	w.ahora = rel.ahora

	n := w.DrenarDespierto(context.Background(), Plaza{TenantID: "tenant-1", EdgeID: "edge-1"})
	if n != 1 {
		t.Fatalf("el flanco procesó %d jobs; con uno solo en la cola tenía que ser 1", n)
	}
	if c := p2.count(); c != 1 {
		t.Fatalf("P2 se llamó %d veces en UN flanco; el evento vale una pasada por job", c)
	}
	f, _ := store.Ver("job-envenenado")
	if f.Status != intake.StatusPending {
		t.Fatalf("el job quedó en %q; un tropiezo de infra vuelve a la cola", f.Status)
	}
	if f.Attempts != 1 {
		t.Fatalf("el job consumió %d intentos en un flanco; tenía que ser 1", f.Attempts)
	}
}

// TestDespertar_NoBloqueaNuncaAlLlamante fija el contrato que el gateway necesita: el
// hook corre INLINE en el bucle Recv del stream, así que un `Despertar` que bloqueara
// pararía la recepción de TODOS los frames de ese Edge.
//
// Se llena el buzón muchas veces por encima de su capacidad SIN nadie consumiéndolo:
// si el envío bloqueara, esto no volvería.
//
// 🔬 MUTACIÓN EJECUTADA (roja, y CUELGA): quitar el `default` del select de Despertar.
func TestDespertar_NoBloqueaNuncaAlLlamante(t *testing.T) {
	t.Parallel()
	b := montar(t, map[string]string{"sess-1": "edge-1"})
	m, d, cat := olaTres(t, b.rel, b.store)
	w, err := NewWorker(&captor{}, b.store,
		&p2Vigilada{v: b.vigia}, &p3Vigilada{v: b.vigia}, &p4Vigilada{v: b.vigia},
		m, d, cat, cifraFalsa{}, Config{})
	if err != nil {
		t.Fatalf("cablear el worker: %v", err)
	}
	for i := 0; i < capacidadDespertares*3; i++ {
		w.Despertar("tenant-1", "edge-1")
	}
	// Una plaza a medias tampoco bloquea: se descarta antes de tocar el canal.
	w.Despertar("tenant-1", "")
	w.Despertar("", "edge-1")
}
