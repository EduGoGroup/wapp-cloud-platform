package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
)

// pipeline_test.go — LA SUITE DE ROBUSTEZ del Plan 044 · Ola 2 · T2.5.
//
// Los cuatro escenarios del enunciado —provider caído, calidad, job envenenado y
// reanudación— más la observabilidad y el leak test. El criterio INV-10 (el flujo
// estático intacto con el worker corriendo) NO está aquí: necesita el runtime de
// flujos entero y vive en `internal/flujos/runtime/inv10_worker_test.go`.
//
// 🔴 CADA TEST DE ESTE FICHERO TRAE ESCRITA SU MUTACIÓN, y las mutaciones se
// ejecutaron de verdad —no se dedujeron— porque un docstring que promete un rojo puede
// mentir. Todas COMPILAN: una mutación que no compila prueba el sistema de tipos.

// ---------------------------------------------------------------------------
// (1) PROVIDER CAÍDO ⇒ `pending` CON BACKOFF
// ---------------------------------------------------------------------------

// TestWorker_ProviderCaido_ElJobVuelveAPendingConLaMarcaEmpujada es el primer
// escenario: con el proveedor caído DE VERDAD (puerto muerto, `*url.Error` real), el
// job no muere ni se atasca: vuelve a `pending`, se le cobra el intento y su marca
// queda EN EL FUTURO.
//
// Las tres afirmaciones son distintas y ninguna sobra:
//
//   - `pending` sin más lo daría también un `Release` (la arista SIN castigo);
//   - `attempts == 1` lo daría también un backoff de cero;
//   - la marca en el futuro es la única que prueba que el job NO se puede volver a
//     reclamar en el acto, que es lo que la 0078 existe para impedir.
//
// Y la cuarta —volver a drenar y que NO pase nada— es la que convierte «la marca está
// puesta» en «la marca SIRVE».
//
// 🔬 MUTACIÓN EJECUTADA (M1): en `tropiezo`, cambiar `w.store.Retry(cerrar, job.ID,
// marca)` por `w.store.Release(cerrar, job.ID)` (compila: mismo `(bool, error)`).
// RESULTADO: rojo, y el CÓMO es la mejor descripción del defecto que existe — el test
// no llega a evaluar una sola aserción: `Drenar` no vuelve NUNCA, porque el job
// devuelto sin castigo es reclamable en el acto, vuelve a fallar y vuelve a ser
// reclamable. Es la tormenta de la 0078 reproducida en vivo, y el rojo lo da el
// `-timeout` de `go test`. (Por eso las mutaciones de esta suite se corren con
// `-timeout 25s` y no con el default de 10 minutos.)
func TestWorker_ProviderCaido_ElJobVuelveAPendingConLaMarcaEmpujada(t *testing.T) {
	b := nuevoBanco(t, Config{})
	caida := errorDeRedReal(t)
	b.p2.guion = []guionEtapa{{err: caida}}
	id := b.sembrarSano("")
	t0 := b.rel.ahora()

	if n := b.w.Drenar(context.Background()); n != 1 {
		t.Fatalf("el worker debía procesar 1 job, procesó %d", n)
	}

	f := b.ver(t, id)
	if f.Status != intake.StatusPending {
		t.Fatalf("con el proveedor caído el job debe quedar PENDING, quedó %q", f.Status)
	}
	if f.Attempts != 1 {
		t.Fatalf("el intento debe quedar COBRADO (attempts=1), quedó %d — ¿se usó Release en vez de Retry?", f.Attempts)
	}
	minimo := t0.Add(time.Duration(float64(BackoffBasePorDefecto) * 0.8))
	if !f.NextAttemptAt.After(minimo) {
		t.Fatalf("la marca no se empujó lo suficiente: next_attempt_at=%s, se esperaba > %s (base %s ± 20%%)",
			f.NextAttemptAt, minimo, BackoffBasePorDefecto)
	}
	maximo := t0.Add(time.Duration(float64(BackoffBasePorDefecto) * 1.2))
	if f.NextAttemptAt.After(maximo) {
		t.Fatalf("la marca se empujó DE MÁS: next_attempt_at=%s, se esperaba <= %s", f.NextAttemptAt, maximo)
	}

	// La marca SIRVE: mientras no venza, el job no es reclamable.
	if n := b.w.Drenar(context.Background()); n != 0 {
		t.Fatalf("el job castigado NO debe ser reclamable antes de su marca; se reclamaron %d", n)
	}
	if got := b.p2.count(); got != 1 {
		t.Fatalf("P2 debía llamarse UNA vez, se llamó %d — el backoff no está conteniendo nada", got)
	}

	// Y al vencer, vuelve solo.
	b.rel.avanzar(BackoffBasePorDefecto * 2)
	if n := b.w.Drenar(context.Background()); n != 1 {
		t.Fatalf("vencida la marca el job debe volver a reclamarse, se reclamaron %d", n)
	}
}

// TestWorker_ProviderCaido_JamasLlegaADone es la mitad negativa del criterio INV-10 (1)
// medida en la unidad: por muchas vueltas que dé el worker con el proveedor caído, el
// job NO termina en `done`. Recorre la curva entera hasta agotar el techo de infra.
//
// 🔬 MUTACIÓN EJECUTADA (M2): en `ideas`, devolver `&llm.MainIdeas{Version:
// llm.ArtifactVersion}, nil` cuando `w.p2.Run` falla — o sea, tragarse la caída SIN
// pasar por `tropiezo`. COMPILA. RESULTADO: rojo, el job llega a `done`.
//
// 🔴 LA MUTACIÓN OBVIA NO SIRVE, Y ES UN HALLAZGO. Ignorar el error en `cadena` dejando
// que `desenlace` llame igualmente a `tropiezo` sale VERDE: para cuando la cadena
// seguiría, `tropiezo` ya devolvió el job a `pending`, y tanto el `SaveStage` de P3
// como el `Finish` final tienen el guard `status = 'processing'` y afectan 0 filas. O
// sea que tras un tropiezo el `done` es inalcanzable por la MÁQUINA, no solo por el
// `if err != nil` del worker. Se descubrió ejecutando la mutación, no leyendo el código.
func TestWorker_ProviderCaido_JamasLlegaADone(t *testing.T) {
	b := nuevoBanco(t, Config{})
	b.p2.guion = []guionEtapa{{err: errorDeRedReal(t)}}
	id := b.sembrarSano("")

	for vuelta := 1; vuelta <= MaxIntentosInfraPorDefecto+2; vuelta++ {
		b.w.Drenar(context.Background())
		if f := b.ver(t, id); f.Status == intake.StatusDone {
			t.Fatalf("vuelta %d: el job llegó a DONE con el proveedor caído (INV-10 (1) roto)", vuelta)
		}
		b.rel.avanzar(BackoffTopePorDefecto * 2)
	}

	f := b.ver(t, id)
	if f.Status != intake.StatusFailed {
		t.Fatalf("agotados los %d intentos de infra el job debe quedar FAILED, quedó %q (attempts=%d)",
			MaxIntentosInfraPorDefecto, f.Status, f.Attempts)
	}
	if !strings.Contains(f.Error, "causa="+CausaInfra) {
		t.Fatalf("la causa de muerte debe decir CUÁL era: %q", f.Error)
	}
	if got := b.p2.count(); got != MaxIntentosInfraPorDefecto {
		t.Fatalf("se esperaban %d llamadas a P2 (el techo de infra), hubo %d",
			MaxIntentosInfraPorDefecto, got)
	}
}

// ---------------------------------------------------------------------------
// (2) `ErrLLMQuality` EN CADA ETAPA ⇒ RETRY CON **SU** TECHO
// ---------------------------------------------------------------------------

// TestWorker_CalidadEnCadaEtapa_ReintentaConElTechoDeCalidad recorre las TRES etapas
// con el mismo fallo de calidad y afirma dos cosas a la vez: que se reintenta, y que el
// techo que se aplica es el de CALIDAD (3) y no el de infra (10).
//
// Lo segundo es lo que hace que el test no sea tautológico: si el worker usara un solo
// techo, el job moriría en el intento 10 y este test caería en el conteo de llamadas.
//
// 🔴 ESTA ES LA POLÍTICA DEL **JOB**, NO LA DEL ÍTEM. La del ítem la tiene P3 desde
// T2.3 (un reintento a temperatura 0.3 y aislamiento) y no llega hasta aquí: cuando P3
// aísla un ítem, devuelve el job SANO. Lo que se prueba aquí es el otro caso — la etapa
// entera no pudo producir artefacto.
//
// 🔬 MUTACIÓN EJECUTADA: en `causaDe`, devolver siempre `CausaInfra` (borrar el
// `errors.Is`). RESULTADO: rojo en las tres etapas — el job sobrevive al tercer intento
// y el conteo de llamadas se va a 10.
func TestWorker_CalidadEnCadaEtapa_ReintentaConElTechoDeCalidad(t *testing.T) {
	basura := fmt.Errorf("la salida del modelo no es un artefacto legible: %w", llm.ErrLLMQuality)
	casos := []struct {
		etapa  string
		romper func(*banco)
	}{
		{intake.StageP2, func(b *banco) { b.p2.guion = []guionEtapa{{err: basura}} }},
		{intake.StageP3, func(b *banco) { b.p3.guion = []guionEtapa{{err: basura}} }},
		{intake.StageP4, func(b *banco) { b.p4.guion = []guionEtapa{{err: basura}} }},
	}
	for _, c := range casos {
		t.Run(c.etapa, func(t *testing.T) {
			b := nuevoBanco(t, Config{})
			c.romper(b)
			id := b.sembrarSano("")

			f := drenarHasta(t, b, id, MaxIntentosCalidadPorDefecto+3)

			if f.Status != intake.StatusFailed {
				t.Fatalf("tras el techo de calidad el job debe quedar FAILED, quedó %q", f.Status)
			}
			if f.Attempts != MaxIntentosCalidadPorDefecto-1 {
				t.Fatalf("se esperaban %d reintentos COBRADOS antes de morir, hubo %d",
					MaxIntentosCalidadPorDefecto-1, f.Attempts)
			}
			if !strings.Contains(f.Error, "causa="+CausaCalidad) {
				t.Fatalf("la causa debe ser `calidad` y decirlo: %q", f.Error)
			}
			if !strings.Contains(f.Error, "stage="+c.etapa) {
				t.Fatalf("la causa debe decir DÓNDE murió: %q", f.Error)
			}
		})
	}
}

// drenarHasta drena hasta que el job sea terminal o se agoten las vueltas, avanzando el
// reloj entre una y otra para que el backoff venza. Devuelve la fila final.
func drenarHasta(t *testing.T, b *banco, id string, vueltas int) Fila {
	t.Helper()
	for i := 0; i < vueltas; i++ {
		b.w.Drenar(context.Background())
		f := b.ver(t, id)
		if intake.IsTerminal(f.Status) {
			return f
		}
		b.rel.avanzar(BackoffTopePorDefecto * 2)
	}
	return b.ver(t, id)
}

// TestWorker_CalidadQueSeArregla_ElJobTerminaBien es el control del anterior: si el
// segundo intento sale bien, el job llega a `done` y NO arrastra el fallo.
//
// Sin este control, el test de arriba pasaría igual con un worker que matara el job al
// primer error de calidad y contara mal — este exige que el reintento SIRVA para algo.
//
// 🔬 MUTACIÓN EJECUTADA: en `Config.topeDe`, devolver 1 para `CausaCalidad`.
// RESULTADO: rojo — el job muere en el primer intento y nunca llega a `done`.
func TestWorker_CalidadQueSeArregla_ElJobTerminaBien(t *testing.T) {
	b := nuevoBanco(t, Config{})
	b.p2.guion = []guionEtapa{
		{err: fmt.Errorf("basura: %w", llm.ErrLLMQuality)},
		{}, // el segundo intento sale bien
	}
	id := b.sembrarSano("")

	f := drenarHasta(t, b, id, 4)
	if f.Status != intake.StatusDone {
		t.Fatalf("el segundo intento salió bien: el job debe quedar DONE, quedó %q (error=%q)", f.Status, f.Error)
	}
	if f.Attempts != 1 {
		t.Fatalf("debe quedar constancia del intento fallido (attempts=1), hay %d", f.Attempts)
	}
	if f.SourceText.Complete() {
		t.Fatal("INV-13: un job terminal no puede conservar el sobre del literal")
	}
}

// ---------------------------------------------------------------------------
// (3) JOB ENVENENADO ⇒ `failed` CON `error`, Y NUNCA BLOQUEA A OTROS
// ---------------------------------------------------------------------------

// TestWorker_JobEnvenenado_MuereYNoBloqueaAlSiguienteDelMismoTenant es el criterio
// entero en una escena: el job sin sobre —el que el compositor del flush no llegó a
// escribir— muere con su causa, y el siguiente job DEL MISMO TENANT sale adelante EN LA
// MISMA PASADA.
//
// 🔴 EL BLOQUEO QUE SE VIGILA ES REAL Y TIENE MECANISMO. El claim ordena por
// `next_attempt_at, created_at`: un job envenenado que volviera a `pending` sin castigo
// sería SIEMPRE el primero de la cola (es el más viejo) y se llevaría todas las vueltas,
// dejando al de detrás sin ejecutarse jamás. No hace falta un candado para bloquear una
// cola: basta con no salir nunca de ella.
//
// El worker NO llama al modelo para el job envenenado, y eso también se afirma: un
// prompt sin texto del cliente es el accidente de D-044.24 y además tira 22–32 s de la
// plaza única.
//
// 🔬 MUTACIÓN EJECUTADA: en `matar`, cambiar `w.store.Fail(cerrar, job.ID, motivo)` por
// `w.store.Release(cerrar, job.ID)` (compila). RESULTADO: rojo — el envenenado se queda
// `pending` y acapara las cinco vueltas; el job sano nunca llega a `done`.
func TestWorker_JobEnvenenado_MuereYNoBloqueaAlSiguienteDelMismoTenant(t *testing.T) {
	b := nuevoBanco(t, Config{})
	tenant := intake.WindowKey{TenantID: "tenant-1", SessionID: "sess-1",
		ContactID: "contacto-1", EventID: "11111111-1111-1111-1111-111111111111"}

	// El envenenado es el MÁS VIEJO: es el que ganaría el ORDER BY para siempre.
	veneno := b.store.Sembrar(Fila{
		ID: "envenenado", Key: tenant,
		CreatedAt: b.rel.ahora().Add(-time.Hour),
		// Sin SourceText: el sobre que el compositor no escribió.
	})
	tenant.EventID = "22222222-2222-2222-2222-222222222222"
	sano := b.store.Sembrar(Fila{
		ID: "sano", Key: tenant, CreatedAt: b.rel.ahora(),
		SourceText: intake.SourceText{Enc: []byte("cifrado"), DEK: []byte("dek"), KEKID: "kek-1"},
	})

	// CINCO vueltas acotadas, no un bucle abierto: con el bloqueo puesto, un `for`
	// sin techo colgaría el test en vez de ponerlo rojo.
	for i := 0; i < 5; i++ {
		if _, err := b.w.UnaVuelta(context.Background()); err != nil {
			t.Fatalf("vuelta %d: %v", i, err)
		}
	}

	fv := b.ver(t, veneno)
	if fv.Status != intake.StatusFailed {
		t.Fatalf("el job envenenado debe quedar FAILED, quedó %q", fv.Status)
	}
	if fv.Error == "" {
		t.Fatal("un job muerto sin causa no se puede diagnosticar: `error` está vacío")
	}
	if !strings.Contains(fv.Error, "causa="+CausaJobInvalido) {
		t.Fatalf("la causa debe distinguir el job inválido de un fallo transitorio: %q", fv.Error)
	}
	if fv.Attempts != 0 {
		t.Fatalf("un job inválido no se reintenta ni una vez; attempts=%d", fv.Attempts)
	}

	fs := b.ver(t, sano)
	if fs.Status != intake.StatusDone {
		t.Fatalf("el job SANO del mismo tenant debe llegar a DONE pese al envenenado, quedó %q", fs.Status)
	}
	if got := b.p2.count(); got != 1 {
		t.Fatalf("solo el job sano debe llamar al modelo: se esperaba 1 llamada a P2, hubo %d", got)
	}
}

// TestWorker_ElDescifradoQueFallaSeTrataComoInfraestructura separa el otro modo de «sin
// literal»: el sobre está ENTERO pero la KEK no lo desenvuelve. Eso SÍ puede ser
// transitorio (KMS caído) y no debe matar el pedido de un cliente al primer intento.
//
// 🔬 MUTACIÓN EJECUTADA: en `literalDe`, envolver el error de `Decrypt` con
// `stages.ErrSinLiteral` (`fmt.Errorf("%w: %w", stages.ErrSinLiteral, err)`).
// RESULTADO: rojo — el job muere en la primera vuelta con `causa=job_invalido`.
func TestWorker_ElDescifradoQueFallaSeTrataComoInfraestructura(t *testing.T) {
	b := nuevoBanco(t, Config{})
	w, err := NewWorker(b.log, b.store, b.p2, b.p3, b.p4, b.match, b.draft, b.catalogos,
		cifraRota{err: errors.New("la KEK kek-9 no está en el keyring")}, Config{})
	if err != nil {
		t.Fatalf("cablear: %v", err)
	}
	w.ahora = b.rel.ahora
	b.w = w
	id := b.sembrarSano("")

	b.w.Drenar(context.Background())

	f := b.ver(t, id)
	if f.Status != intake.StatusPending {
		t.Fatalf("un descifrado fallido es transitorio: el job debe volver a PENDING, quedó %q", f.Status)
	}
	if f.Attempts != 1 {
		t.Fatalf("el intento debe cobrarse; attempts=%d", f.Attempts)
	}
	if got := b.p2.count(); got != 0 {
		t.Fatalf("sin literal NO se llama al modelo; hubo %d llamadas", got)
	}
}

// ---------------------------------------------------------------------------
// (4) REANUDACIÓN POR ESTADO
// ---------------------------------------------------------------------------

// TestWorker_Redelivery_SaltaLasEtapasConArtefactoPersistido es el criterio de la
// reanudación: un job que vuelve a la cola con `p2` y `p3` ya escritos NO los repite.
//
// Es lo que hace que una caída a mitad de un pedido de 10 ítems no cueste otra vez las
// 22–32 s por ítem que ya se pagaron.
//
// Los artefactos NO se escriben a mano: los deja la PRIMERA corrida, que falla en P4.
// Un test que fabricara el `artifacts` a mano estaría afirmando algo sobre un estado
// que la máquina podría no producir nunca.
//
// 🔬 MUTACIÓN EJECUTADA: en `reanudar`, devolver `nil, false` justo antes del
// `json.Unmarshal`. RESULTADO: rojo — P2 y P3 se llaman dos veces cada uno.
func TestWorker_Redelivery_SaltaLasEtapasConArtefactoPersistido(t *testing.T) {
	b := nuevoBanco(t, Config{})
	b.p4.guion = []guionEtapa{{err: errorDeRedReal(t)}, {}}
	id := b.sembrarSano("")

	b.w.Drenar(context.Background()) // 1.ª corrida: P2 y P3 salen, P4 cae
	if f := b.ver(t, id); f.Status != intake.StatusPending {
		t.Fatalf("tras caer P4 el job debe quedar pending, quedó %q", f.Status)
	}
	if _, ok := b.ver(t, id).Artifacts[intake.StageP3]; !ok {
		t.Fatal("la 1.ª corrida debía dejar el artefacto de P3 persistido y no lo dejó: el test no mira nada")
	}

	b.rel.avanzar(BackoffTopePorDefecto * 2)
	b.w.Drenar(context.Background()) // 2.ª corrida: solo P4

	if f := b.ver(t, id); f.Status != intake.StatusDone {
		t.Fatalf("la 2.ª corrida debe terminar el job, quedó %q", f.Status)
	}
	if got := b.p2.count(); got != 1 {
		t.Fatalf("P2 NO debe repetirse en la redelivery: se llamó %d veces", got)
	}
	if got := b.p3.count(); got != 1 {
		t.Fatalf("P3 NO debe repetirse en la redelivery: se llamó %d veces", got)
	}
	if got := b.p4.count(); got != 2 {
		t.Fatalf("P4 sí debe repetirse (no llegó a persistir): se llamó %d veces", got)
	}
}

// TestWorker_ArtefactoPersistidoIlegible_RehaceLaEtapaEnVezDeMatarElJob es el borde de
// la reanudación: un `artifacts.p2` que este código no sabe decodificar NO mata el
// pedido — se rehace la etapa y se avisa.
//
// 🔬 MUTACIÓN EJECUTADA: en `reanudar`, devolver `&art, true` tras el error de
// `Unmarshal` en vez de `nil, false`. RESULTADO: rojo — P2 no se llama y el job termina
// con un artefacto vacío que nadie produjo.
func TestWorker_ArtefactoPersistidoIlegible_RehaceLaEtapaEnVezDeMatarElJob(t *testing.T) {
	b := nuevoBanco(t, Config{})
	id := b.store.Sembrar(Fila{
		Key: intake.WindowKey{TenantID: "tenant-1", SessionID: "sess-1",
			ContactID: "contacto-1", EventID: "11111111-1111-1111-1111-111111111111"},
		SourceText: intake.SourceText{Enc: []byte("cifrado"), DEK: []byte("dek"), KEKID: "kek-1"},
		Stage:      intake.StageP2,
		Artifacts: map[string]json.RawMessage{
			intake.StageP2: json.RawMessage(`{"version":1,"wants":"esto-no-es-una-lista"}`),
		},
	})

	b.w.Drenar(context.Background())

	if f := b.ver(t, id); f.Status != intake.StatusDone {
		t.Fatalf("un artefacto ilegible no debe matar el job, quedó %q (%s)", f.Status, f.Error)
	}
	if got := b.p2.count(); got != 1 {
		t.Fatalf("la etapa debía REHACERSE: P2 se llamó %d veces", got)
	}
	b.log.unica(t, "no se pudo decodificar").exigeCampos(t, "job_id", "stage")
}

// ---------------------------------------------------------------------------
// (5) OBSERVABILIDAD MÍNIMA — Y EL AVISO DE §5.2·bis
// ---------------------------------------------------------------------------

// TestWorker_CadaEtapaDejaSuLineaConJobIdStageYElapsed es el criterio de observabilidad
// por el camino FELIZ: una línea por etapa con los tres campos del enunciado.
//
// 🔬 MUTACIÓN EJECUTADA: en `desenlace`, quitar `"elapsed_ms"` del `log.Info`.
// RESULTADO: rojo en las tres etapas.
func TestWorker_CadaEtapaDejaSuLineaConJobIdStageYElapsed(t *testing.T) {
	b := nuevoBanco(t, Config{})
	b.p2.guion = []guionEtapa{{dura: 21 * time.Second}}
	b.p3.guion = []guionEtapa{{dura: 27 * time.Second}}
	b.p4.guion = []guionEtapa{{dura: 3 * time.Second}}
	// 🔄 LAS DOS ÚLTIMAS SON DE T3.8. Llevan duración propia —milisegundos, no
	// segundos— porque no llaman al modelo: `match` cruza el catálogo en memoria y
	// `draft` escribe cuatro filas. Lo que este test exige de ellas es lo mismo que de
	// las tres primeras: que publiquen su línea con `elapsed_ms` mayor que cero.
	b.match.guion = []guionEtapa{{dura: 4 * time.Millisecond}}
	b.draft.guion = []guionEtapa{{dura: 9 * time.Millisecond}}
	id := b.sembrarSano("")

	b.w.Drenar(context.Background())
	if f := b.ver(t, id); f.Status != intake.StatusDone {
		t.Fatalf("el job debía terminar bien, quedó %q", f.Status)
	}

	lineas := b.log.buscar("etapa completada")
	if len(lineas) != 5 {
		t.Fatalf("se esperaba UNA línea por etapa (5: p2, p3, p4, match, draft), hay %d: %s", len(lineas), b.log.volcado())
	}
	vistas := map[string]bool{}
	for _, l := range lineas {
		l.exigeCampos(t, "job_id", "stage", "elapsed_ms")
		etapa, esCadena := l.campos["stage"].(string)
		if !esCadena {
			t.Fatalf("el campo `stage` no es una cadena: %v", l.campos["stage"])
		}
		vistas[etapa] = true
		ms, ok := l.campos["elapsed_ms"].(int64)
		if !ok || ms <= 0 {
			t.Fatalf("la etapa %q publicó un elapsed_ms inservible: %v", etapa, l.campos["elapsed_ms"])
		}
	}
	for _, e := range []string{intake.StageP2, intake.StageP3, intake.StageP4, intake.StageMatch, intake.StageDraft} {
		if !vistas[e] {
			t.Fatalf("falta la línea de la etapa %q: %s", e, b.log.volcado())
		}
	}
}

// TestWorker_LaEtapaQueFALLA_TambienPublicaSuElapsed es §5.2·bis hecho un test, y es la
// mitad que se pierde sola.
//
// 🔴 EL AVISO, LITERAL: «no cuelgues una señal de un desenlace FELIZ de una operación
// que, en el caso que te importa, FRACASA». Colgar el cronómetro del `log.Info` del
// final de la etapa es exactamente eso: la etapa que muere por timeout —la única que
// dice cuánto tardó el plazo en morder, y la que hay que medir para recalibrarlo— no
// pasaría por ahí y no publicaría nada. El fallo borraría su propia evidencia, que es
// lo que le pasó a DEUDA-044.10.
//
// 🔬 MUTACIÓN EJECUTADA: en `desenlace`, mover `transcurrido := w.ahora().Sub(inicio)`
// dentro de la rama de éxito y pasarle `0` a `w.tropiezo`. COMPILA y deja el camino
// feliz intacto —el test de arriba sigue verde—. RESULTADO: rojo aquí, `elapsed_ms=0`.
func TestWorker_LaEtapaQueFALLA_TambienPublicaSuElapsed(t *testing.T) {
	b := nuevoBanco(t, Config{})
	b.p3.guion = []guionEtapa{{err: errorDeRedReal(t), dura: 48 * time.Second}}
	b.sembrarSano("")

	b.w.Drenar(context.Background())

	l := b.log.unica(t, "vuelve a la cola con backoff")
	l.exigeCampos(t, "job_id", "stage", "causa", "elapsed_ms", "intento", "tope", "next_attempt_at")
	ms, ok := l.campos["elapsed_ms"].(int64)
	if !ok || ms <= 0 {
		t.Fatalf("el tropiezo debe publicar cuánto tardó ANTES de fallar; publicó %v — el cronómetro está colgado del camino feliz",
			l.campos["elapsed_ms"])
	}
	if got := l.campos["causa"]; got != CausaInfra {
		t.Fatalf("la causa debe viajar como CAMPO y decir cuál es; viajó %v", got)
	}
	if etapa := l.campos["stage"]; etapa != intake.StageP3 {
		t.Fatalf("el tropiezo debe decir en qué etapa ocurrió; dijo %v", etapa)
	}
}

// TestWorker_ElDesenlaceQueNoAPLICA_DejaLinea es la otra mitad del mismo aviso: cuando
// otro worker termina el job mientras esta cadena corre, `Retry` afecta 0 filas y
// devuelve `(false, nil)` — un NO-ERROR. Sin línea propia, ese camino sería
// completamente invisible: ni error, ni aviso, ni rastro de un job que se movió.
//
// 🔬 MUTACIÓN EJECUTADA: en `tropiezo`, borrar la rama `if !aplicado { ... }`.
// RESULTADO: rojo — no hay ninguna línea y el `unica` falla.
func TestWorker_ElDesenlaceQueNoAPLICA_DejaLinea(t *testing.T) {
	b := nuevoBanco(t, Config{})
	id := b.sembrarSano("")
	// Otro worker lo termina justo cuando P2 está corriendo, y luego P2 falla.
	b.p2.alLlamar = func() {
		if _, err := b.store.Finish(context.Background(), id, ""); err != nil {
			t.Errorf("simular al otro worker: %v", err)
		}
	}
	b.p2.guion = []guionEtapa{{err: errorDeRedReal(t)}}

	b.w.Drenar(context.Background())

	b.log.unica(t, "el reencolado no aplicó").exigeCampos(t, "job_id", "stage", "causa")
	if f := b.ver(t, id); f.Status != intake.StatusDone {
		t.Fatalf("el job lo terminó el otro worker: debe quedar DONE, quedó %q", f.Status)
	}
	if f := b.ver(t, id); f.Attempts != 0 {
		t.Fatalf("un job que ya no estaba en processing no debe recibir castigo; attempts=%d", f.Attempts)
	}
}

// TestWorker_CeroIdeasVivas_SigueYLODICE es la decisión sobre los `wants` vacíos que
// T2.2 dejó sin dueño: `llm.ParseMainIdeas` declara válida una lista vacía, así que un
// job al que el anclaje le tire TODAS las ideas persiste `{"version":1,"wants":[]}` y
// sigue camino.
//
// LA DECISIÓN ES **NO CORTAR LA CADENA** (ver el docstring de `sinIdeas`: design §3.2
// declara «cero resultados válidos» no fatal, es un caso legítimo —un «hola» abre
// ventana— y no cuesta plaza porque P3 y P4 no llaman al modelo sin ítems). Lo que sí
// se exige es que quede DICHO, con sus dos orígenes nombrados y ninguno afirmado.
//
// 🔬 MUTACIÓN EJECUTADA: en `cadena`, borrar el `if len(ideas.Wants) == 0 {
// w.sinIdeas(job) }`. RESULTADO: rojo — el job sigue terminando en `done` (por eso el
// test no se conforma con el estado) pero no queda una sola línea.
func TestWorker_CeroIdeasVivas_SigueYLODICE(t *testing.T) {
	b := nuevoBanco(t, Config{})
	b.p2.wants = nil // el anclaje se las llevó todas
	id := b.sembrarSano("")

	b.w.Drenar(context.Background())

	if f := b.ver(t, id); f.Status != intake.StatusDone {
		t.Fatalf("cero ideas NO es fatal (design §3.2): el job debe terminar, quedó %q", f.Status)
	}
	if got := b.p3.count(); got != 1 {
		t.Fatalf("P3 debe ejecutarse igual (persiste su artefacto vacío sin llamar al modelo): %d", got)
	}
	l := b.log.unica(t, "no dejó ni una idea viva")
	l.exigeCampos(t, "job_id", "stage", "causa", "donde_mirar")
	donde, esCadena := l.campos["donde_mirar"].(string)
	if !esCadena {
		t.Fatalf("el campo `donde_mirar` no es una cadena: %v", l.campos["donde_mirar"])
	}
	if !strings.Contains(donde, "ideas_descartadas") {
		t.Fatalf("el aviso debe mandar a donde SÍ está la causa (el log de p2), dice: %q", donde)
	}
}

// ---------------------------------------------------------------------------
// (6) CERO GOROUTINES FILTRADAS
// ---------------------------------------------------------------------------

// TestWorker_Run_NoFiltraGoroutines es el leak test del criterio.
//
// 🔴 QUÉ CUSTODIA HOY, DICHO SIN ADORNO: el worker de T2.5 NO lanza ni una goroutine
// propia —el bucle corre en la del llamante y el ticker de `time` no gasta una—, así
// que hoy este test no puede caer por nada que haga `Run`. Es una RED PARA EL FUTURO:
// el momento en que alguien paralelice el fan-out o meta un `go` en el camino del
// desenlace, esto lo dice. Se escribe sabiendo eso, y no se le atribuye más.
//
// 🔴 NO SE USA `go.uber.org/goleak`: está en `go.sum` como dependencia transitiva pero
// NO en `go.mod` y NO lo usa un solo test del repo. Añadirlo es una decisión de
// dependencia y no se toma dentro de una tarea. El conteo a mano con reintento acotado
// hace el trabajo aquí.
//
// 🔬 MUTACIÓN EJECUTADA: al principio de `Run`, añadir
// `bloqueada := make(chan struct{}); go func() { <-bloqueada }()`. COMPILA.
// RESULTADO: rojo, «quedaron 1 goroutine(s) de más».
func TestWorker_Run_NoFiltraGoroutines(t *testing.T) {
	b := nuevoBanco(t, Config{Cadencia: time.Millisecond})
	b.sembrarSano("")

	antes := esperarGoroutines(t, runtime.NumGoroutine())
	ctx, cancel := context.WithCancel(context.Background())
	fin := make(chan struct{})
	go func() {
		defer close(fin)
		b.w.Run(ctx)
	}()

	// Se espera a que el worker haya hecho trabajo REAL antes de apagarlo: apagarlo
	// antes de que arranque probaría que una goroutine que no hizo nada no filtra
	// nada.
	esperarClaims(t, b, 2)
	cancel()
	select {
	case <-fin:
	case <-time.After(5 * time.Second):
		t.Fatal("Run no volvió tras cancelar el contexto: el apagado está roto")
	}

	if despues := esperarGoroutines(t, antes); despues > antes {
		t.Fatalf("quedaron %d goroutine(s) de más tras apagar el worker (antes %d, después %d)",
			despues-antes, antes, despues)
	}
}

// esperarGoroutines espera a que el conteo baje hasta `objetivo` (o a que se agote el
// plazo) y devuelve el último valor. El reintento no es cosmético: una goroutine que
// acaba de recibir su cancelación tarda un instante en desaparecer del conteo, y sin
// esta espera el test sería intermitente — que es peor que no tenerlo.
func esperarGoroutines(t *testing.T, objetivo int) int {
	t.Helper()
	n := runtime.NumGoroutine()
	limite := time.Now().Add(2 * time.Second)
	for n > objetivo && time.Now().Before(limite) {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	return n
}

// esperarClaims espera a que el worker haya reclamado al menos `n` veces.
func esperarClaims(t *testing.T, b *banco, n int) {
	t.Helper()
	limite := time.Now().Add(5 * time.Second)
	for b.store.Claims() < n {
		if time.Now().After(limite) {
			t.Fatalf("el worker no llegó a %d claims (hizo %d): el bucle no está corriendo", n, b.store.Claims())
		}
		time.Sleep(time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// LA POLÍTICA, MIRADA DE CERCA
// ---------------------------------------------------------------------------

// TestEspera_LaCurvaCreceSeTopaYLLEVAJITTER afirma las tres propiedades de la curva por
// separado, porque las tres se pueden romper sin romper a las otras dos.
//
// 🔬 MUTACIÓN EJECUTADA: en `espera`, quitar el `min(..., tope)`. RESULTADO: rojo en el
// tope. Segunda mutación: devolver `d` sin jitter ⇒ rojo en la dispersión.
//
// 🔧 CORREGIDO EL 2026-08-25 (desde T2.6): la primera propiedad era **FLAKY, 9 de cada
// 60 corridas** — medido, no supuesto. El comentario decía «se comparan las COTAS del
// jitter (±20 %) y no dos muestras sueltas, que podrían cruzarse por azar» y eso era
// FALSO: se tomaba UNA muestra de cada intento —ya jitteada— y encima se le volvía a
// aplicar el ±20 %, con lo que el margen se consumía dos veces. Con `base = 30 s`, una
// muestra alta del intento 1 (35,97 s ⇒ ×1,2 = 43,2 s) superaba a una baja del intento 2
// (48 s ⇒ ×0,8 = 38,4 s) y el test se ponía rojo sin que nadie hubiera tocado la curva.
//
// Lo que ahora se compara SÍ son cotas, y salen de MUESTREAR: el máximo observado del
// intento N contra el mínimo observado del N+1. Con el jitter acotado a [0,8; 1,2) y
// `d(N+1) = 2·d(N)`, el máximo del bajo tiende a 1,2·d y el mínimo del alto a 1,6·d, así
// que el verde es holgado; y si la curva dejara de crecer, las dos cotas saldrían del
// MISMO d (1,2·d contra 0,8·d) y el rojo es igual de fiable. Ninguna de las dos
// direcciones depende ya del azar.
func TestEspera_LaCurvaCreceSeTopaYLLEVAJITTER(t *testing.T) {
	const base = 30 * time.Second
	const tope = 5 * time.Minute

	// Crece: el intento N+1 castiga más que el N mientras no se llegue al tope.
	for intento := 1; intento <= 3; intento++ {
		_, techoDelBajo := cotasDeEspera(intento, base, tope)
		sueloDelAlto, _ := cotasDeEspera(intento+1, base, tope)
		if techoDelBajo >= sueloDelAlto {
			t.Fatalf("la curva no crece del intento %d al %d: el techo del %d (%s) alcanza al suelo del %d (%s)",
				intento, intento+1, intento, techoDelBajo, intento+1, sueloDelAlto)
		}
	}

	// Se topa, y el jitter no lo desborda: el máximo posible es tope × 1,2.
	for intento := 5; intento <= 20; intento++ {
		if d := espera(intento, base, tope); d > time.Duration(float64(tope)*1.2) {
			t.Fatalf("el intento %d se pasó del tope %s: %s", intento, tope, d)
		}
	}

	// Lleva jitter: 40 muestras del mismo intento no pueden ser todas iguales, o dos
	// jobs castigados por la misma caída volverían exactamente a la vez.
	vistos := map[time.Duration]bool{}
	for i := 0; i < 40; i++ {
		vistos[espera(2, base, tope)] = true
	}
	if len(vistos) < 5 {
		t.Fatalf("el jitter no está dispersando nada: 40 muestras dieron %d valores distintos", len(vistos))
	}
}

// cotasDeEspera muestrea la curva de UN intento y devuelve el mínimo y el máximo vistos.
// Es lo que convierte una comparación entre dos tiradas de dado en una comparación entre
// cotas: 64 muestras bastan porque el jitter es uniforme en [0,8; 1,2) y el margen que
// hay que distinguir es un factor 2.
func cotasDeEspera(intento int, base, tope time.Duration) (suelo, techo time.Duration) {
	suelo, techo = espera(intento, base, tope), time.Duration(0)
	for i := 0; i < 64; i++ {
		d := espera(intento, base, tope)
		suelo, techo = min(suelo, d), max(techo, d)
	}
	return suelo, techo
}

// TestCausaDe_LaCalidadVAPRIMERO custodia el orden de las ramas, que es lo único que
// tiene `causaDe` y lo único que se puede romper en silencio: los adaptadores envuelven
// `llm.ErrLLMQuality` dentro de errores más gordos, así que una rama más ancha delante
// se lo tragaría y la calidad se reintentaría diez veces en vez de tres.
//
// 🔬 MUTACIÓN EJECUTADA: cambiar el `errors.Is` por `err == llm.ErrLLMQuality`.
// COMPILA. RESULTADO: rojo en el caso envuelto.
func TestCausaDe_LaCalidadVAPRIMERO(t *testing.T) {
	envuelto := fmt.Errorf("p3: la salida del modelo no es legible: %w",
		fmt.Errorf("campo `product` vacío: %w", llm.ErrLLMQuality))
	if got := causaDe(envuelto); got != CausaCalidad {
		t.Fatalf("un ErrLLMQuality envuelto dos veces sigue siendo calidad; se clasificó %q", got)
	}
	if got := causaDe(errors.New("dial tcp 127.0.0.1:1: connect: connection refused")); got != CausaInfra {
		t.Fatalf("un fallo de transporte es infra; se clasificó %q", got)
	}
	if got := causaDe(context.DeadlineExceeded); got != CausaInfra {
		t.Fatalf("un plazo agotado es infra; se clasificó %q", got)
	}
}

// TestNewWorker_NoNaceAMedias: como las etapas, un worker sin una pieza se niega a
// nacer. Un worker sin descifrador no podría abrir un solo sobre y lo descubriríamos en
// producción, un job por vez.
func TestNewWorker_NoNaceAMedias(t *testing.T) {
	b := nuevoBanco(t, Config{})
	casos := map[string]func() (*Worker, error){
		"sin log": func() (*Worker, error) {
			return NewWorker(nil, b.store, b.p2, b.p3, b.p4, b.match, b.draft, b.catalogos, cifraFalsa{}, Config{})
		},
		"sin store": func() (*Worker, error) {
			return NewWorker(b.log, nil, b.p2, b.p3, b.p4, b.match, b.draft, b.catalogos, cifraFalsa{}, Config{})
		},
		"sin p2": func() (*Worker, error) {
			return NewWorker(b.log, b.store, nil, b.p3, b.p4, b.match, b.draft, b.catalogos, cifraFalsa{}, Config{})
		},
		"sin p3": func() (*Worker, error) {
			return NewWorker(b.log, b.store, b.p2, nil, b.p4, b.match, b.draft, b.catalogos, cifraFalsa{}, Config{})
		},
		"sin p4": func() (*Worker, error) {
			return NewWorker(b.log, b.store, b.p2, b.p3, nil, b.match, b.draft, b.catalogos, cifraFalsa{}, Config{})
		},
		// 🔴 LOS TRES DE LA OLA 3 (T3.8). Sin ellos el worker NACERÍA —cablearlos como
		// opción era la alternativa— y terminaría cada job en `done` sin `intake_id`, que
		// es exactamente el estado del que T3.8 saca al pipeline. Que el constructor los
		// exija es lo que impide que vuelvan a quedarse apagados sin que nada falle.
		"sin match": func() (*Worker, error) {
			return NewWorker(b.log, b.store, b.p2, b.p3, b.p4, nil, b.draft, b.catalogos, cifraFalsa{}, Config{})
		},
		"sin draft": func() (*Worker, error) {
			return NewWorker(b.log, b.store, b.p2, b.p3, b.p4, b.match, nil, b.catalogos, cifraFalsa{}, Config{})
		},
		"sin catálogo": func() (*Worker, error) {
			return NewWorker(b.log, b.store, b.p2, b.p3, b.p4, b.match, b.draft, nil, cifraFalsa{}, Config{})
		},
		"sin descifrador": func() (*Worker, error) {
			return NewWorker(b.log, b.store, b.p2, b.p3, b.p4, b.match, b.draft, b.catalogos, nil, Config{})
		},
	}
	for nombre, construir := range casos {
		t.Run(nombre, func(t *testing.T) {
			w, err := construir()
			if !errors.Is(err, ErrSinCablear) || w != nil {
				t.Fatalf("se esperaba ErrSinCablear y worker nil, salió (%v, %v)", w, err)
			}
		})
	}
}

// TestConfig_LosDefaultsSonLosDeProduccion es la red del «nunca a cero»: una `Cadencia`
// a cero haría un bucle de CPU al 100 % y un `MaxIntentos` a cero mataría el primer job
// que tropezara — las dos son averías silenciosas que un `<= 0` mal escrito produce.
//
// 🔴 Los valores se escriben LITERALES y no contra las constantes que quieren proteger:
// un test que comparase `cfg.BackoffBase` con `BackoffBasePorDefecto` pasaría con
// cualquier valor, y esa tautología ya mordió antes en esta casa.
func TestConfig_LosDefaultsSonLosDeProduccion(t *testing.T) {
	c := Config{}.conDefaults()
	if c.Cadencia != 5*time.Second {
		t.Fatalf("cadencia = %s, se esperaban 5s", c.Cadencia)
	}
	if c.MaxIntentosCalidad != 3 {
		t.Fatalf("max intentos calidad = %d, se esperaban 3", c.MaxIntentosCalidad)
	}
	if c.MaxIntentosInfra != 10 {
		t.Fatalf("max intentos infra = %d, se esperaban 10", c.MaxIntentosInfra)
	}
	if c.BackoffBase != 30*time.Second {
		t.Fatalf("backoff base = %s, se esperaban 30s", c.BackoffBase)
	}
	if c.BackoffTope != 5*time.Minute {
		t.Fatalf("backoff tope = %s, se esperaban 5m", c.BackoffTope)
	}
	if c.MaxIntentosCalidad >= c.MaxIntentosInfra {
		t.Fatal("el techo de calidad tiene que ser MENOR que el de infra: repetir un prompt a temperatura 0 repite la respuesta")
	}
}

// TestPlazoPorLlamada_ElSueloSATISFACELAS_DOS_CONDICIONES_DE_T23 comprueba que el
// número que el worker le pone a cada llamada cumple lo que T2.3 dejó escrito, con los
// números que T2.3 dejó medidos. NO es una tautología contra la constante: las dos
// desigualdades se escriben con sus valores literales.
//
// 🔴 LO QUE ESTE TEST **NO** DICE: que 48 s sea el número correcto. Dice que es el
// SUELO de lo que hoy está medido. `p99(P3)` no existe —hay dos observaciones— y por eso
// la segunda desigualdad se evalúa contra el máximo observado, que casi con seguridad
// subestima el p99. Ver `PlazoPorLlamadaSuelo`.
func TestPlazoPorLlamada_ElSueloSATISFACELAS_DOS_CONDICIONES_DE_T23(t *testing.T) {
	const margenVeredicto = 7 * time.Second // local.MargenVeredicto
	const maxObservadoP3 = 32 * time.Second // veredicto §1.4, la mayor de DOS observaciones
	const umbralDeLento = 0.8               // T1.7-2: el breaker llama lento a plazo × 0,8

	util := PlazoPorLlamadaSuelo - margenVeredicto
	if util <= maxObservadoP3 {
		t.Fatalf("una P3 del máximo observado (%s) moriría por timeout: al Edge le llegan %s",
			maxObservadoP3, util)
	}
	if float64(util)*umbralDeLento <= float64(maxObservadoP3) {
		t.Fatalf("una P3 sana del máximo observado (%s) contaría como LENTA: el umbral queda en %s",
			maxObservadoP3, time.Duration(float64(util)*umbralDeLento))
	}
	if PlazoPorLlamadaSuelo >= 60*time.Second {
		t.Fatalf("el suelo se fue por encima del minuto (%s): con 10 ítems (T2.6) el pipeline se va muy por encima de «< 5 min»",
			PlazoPorLlamadaSuelo)
	}
}

// TestElWorkerPasaSuPlazoPorLlamadaALasEtapas es la costura entre las dos mitades: no
// basta con que la constante exista y que las etapas sepan acotar — alguien tiene que
// CABLEARLAS. Sin esto, `PlazoPorLlamadaSuelo` sería una constante que nadie usa y el
// Edge seguiría recibiendo el default de 30 s.
//
// Se prueba por CONDUCTA: se construye una P3 real con la opción, se le da un provider
// que mira el deadline del ctx que recibe, y se afirma que el ctx llega acotado.
//
// 🔬 MUTACIÓN EJECUTADA: en `stages.ConPlazoPorLlamada`, cambiar el `if d > 0` por
// `if d < 0`. COMPILA. RESULTADO: rojo, el ctx llega sin deadline.
func TestElWorkerPasaSuPlazoPorLlamadaALasEtapas(t *testing.T) {
	espia := &proveedorEspia{}
	p3, err := stages.NewP3(&captor{}, selectorFijo{prov: espia}, NuevoStoreEnMemoria(nil),
		stages.ConPlazoPorLlamada(PlazoPorLlamadaSuelo))
	if err != nil {
		t.Fatalf("construir P3: %v", err)
	}
	store := NuevoStoreEnMemoria(nil)
	id := store.Sembrar(Fila{Status: intake.StatusProcessing})
	// El Run FALLA a propósito —el espía no contesta— y ese error no se comprueba: lo
	// que este test mira es el ctx que llegó a la llamada, no la salida.
	if _, rerr := p3.Run(context.Background(), intake.ClaimedJob{ID: id}, "quiero una torta",
		[]llm.Want{{Idea: "torta", Evidence: "torta"}}); rerr == nil {
		t.Fatal("el espía no contesta: P3 tenía que fallar, y si no falló el ctx que se mide no es el de la llamada")
	}

	if !espia.tuvoDeadline {
		t.Fatal("la llamada al modelo llegó SIN deadline: el Edge recibiría timeout_ms=30s y el breaker mentiría (T2.3)")
	}
	if espia.restante > PlazoPorLlamadaSuelo || espia.restante < PlazoPorLlamadaSuelo-5*time.Second {
		t.Fatalf("el plazo que llegó a la llamada es %s, se esperaba ≈ %s", espia.restante, PlazoPorLlamadaSuelo)
	}
}

// proveedorEspia mira el ctx que recibe y no contesta nada útil: lo que se prueba es el
// plazo, no la salida.
type proveedorEspia struct {
	llm.LLMProvider
	tuvoDeadline bool
	restante     time.Duration
}

func (p *proveedorEspia) ExtractItemSpecs(ctx context.Context, _ llm.ExtractItemSpecsInput,
	_ llm.Options) (json.RawMessage, error) {
	dl, ok := ctx.Deadline()
	p.tuvoDeadline = ok
	if ok {
		p.restante = time.Until(dl)
	}
	return nil, errors.New("el espía no contesta")
}

// selectorFijo devuelve siempre el mismo provider.
type selectorFijo struct{ prov llm.LLMProvider }

func (s selectorFijo) For(_ context.Context, _, _ string) (llm.LLMProvider, error) {
	return s.prov, nil
}
