// Package pipeline es EL WORKER del pipeline de presupuestos (Plan 044 · Ola 2 ·
// T2.5): el llamante que ninguna etapa tenía. Reclama un job `pending`, descifra su
// literal, encadena P2 → P3 → P4 y lo termina — o lo devuelve a la cola castigado, o
// lo mata con su causa escrita.
//
// # POR QUÉ ESTE PAQUETE Y NO `internal/intake/pipeline.go`
//
// 🔴 Porque `internal/intake/pipeline.go` NO ES ESCRIBIBLE, y no es una preferencia.
// El design §9 y la cabecera de `llm_caida_best_effort_test.go` citan esa ruta, pero
// `internal/intake/stages` YA importa `internal/intake` (necesita `ClaimedJob`,
// `Artifact` y `StageP2..P4`, desde T2.2). Un worker dentro del paquete `intake` que
// llame a las etapas cierra el ciclo `intake → stages → intake` y no compila. La
// alternativa —mover las etapas dentro de `intake`— desharía la separación que T2.2
// eligió y metería los prompts, el anclaje y el fan-out en el paquete de la cola. Así
// que el worker baja un nivel: mismo sitio del árbol, un paquete propio.
//
// # LO QUE ESTE WORKER NO ES
//
//   - NO es un planificador ni una cola con prioridades: el ADR-0046 los descarta por
//     escrito. Reclama de uno en uno, en el orden que fija el `ORDER BY` del claim.
//   - NO tiene aforo por Edge (T2.7): dos cadenas de lote del MISMO Edge pueden
//     solaparse hoy y hambrear igual a los turnos interactivos. Sigue sin hacerse.
//     🔄 El tope de ítems (T2.6) SÍ está puesto desde el 2026-08-25, y no aquí: lo
//     aplica `stages.acotarAlTope` a la entrada de P3, que es donde se gasta la plaza
//     (ver `stages/tope.go`). Un pedido de 40 ítems ya no hace 40 llamadas: hace 10 y
//     deja las otras 30 MARCADAS. Lo que sigue siendo verdad es la cuenta de tiempo:
//     esos 10 ítems son 320–410 s, por encima de «< 5 min».
//   - NO crea el borrador: `match` y `draft` son de la Ola 3. Hoy la cadena acaba en
//     P4 y el job pasa a `done` sin `intake_id` (ver terminar).
//   - NO escribe el aviso de degradación al dueño (REQ-38). Ya lo escribe el decorador
//     `avisador` de `llmvia` (T1.6-6), envolviendo al provider que el selector
//     devuelve: cablearlo también aquí sería un segundo mecanismo con el mismo
//     síntoma, y de los dos el de allí ve TODAS las vías, no solo el pipeline.
//
// # 🔴 EL AVISO QUE ESTE FICHERO TIENE PRESENTE EN CADA LOG (§5.2·bis del plan)
//
// «No cuelgues una señal, una métrica o una guarda del DESENLACE FELIZ de una
// operación que, en el caso que te importa, FRACASA.» DEUDA-044.10 colgó su aviso del
// régimen de una inferencia SERVIDA y dio cero avisos, porque la inferencia fría muere
// por timeout sin emitir régimen — el fallo borraba su propia evidencia. Aquí el caso
// que importa ES el infeliz: el `elapsed` de una etapa se mide ANTES de saber si salió
// bien y se emite por los DOS caminos (ver etapa), y cada desenlace silencioso de la
// máquina —un Retry que afecta 0 filas, un Fail que no aplica— tiene su propio log,
// porque si no, un job atascado en `processing` no dejaría ni una línea.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
)

// ---------------------------------------------------------------------------
// LOS PUERTOS
// ---------------------------------------------------------------------------

// Descifrador es lo ÚNICO que el worker necesita del stack de claves: abrir el sobre
// de tres piezas. Lo satisface `*crypto.FieldCipher`, el MISMO que lo cerró en el
// compositor del flush (T1.4).
//
// Es una interfaz local y estrecha (ISP, el mismo patrón que `ThreadReader` en el
// compositor) y no el tipo concreto: así el worker no arrastra el keyring entero a sus
// tests, y sobre todo así se puede probar QUÉ HACE con un descifrado que falla — que
// es un caso real (KEK retirada del keyring, KMS caído) y con el tipo concreto sería
// irreproducible.
type Descifrador interface {
	Decrypt(valueEnc, valueDEK []byte, keyID string) (string, error)
}

// EtapaIdeas es P2. Lo satisface `*stages.P2`.
type EtapaIdeas interface {
	Run(ctx context.Context, job intake.ClaimedJob, literal string) (*llm.MainIdeas, error)
}

// EtapaEspecificaciones es P3. Lo satisface `*stages.P3`.
type EtapaEspecificaciones interface {
	Run(ctx context.Context, job intake.ClaimedJob, literal string, ideas []llm.Want) (*stages.ArtefactoP3, error)
}

// EtapaNormalizacion es P4. Lo satisface `*stages.P4`.
type EtapaNormalizacion interface {
	Run(ctx context.Context, job intake.ClaimedJob, literal string,
		items []llm.ItemSpec, entrega *llm.Hint) (*llm.Quantities, error)
}

// Las tres etapas de producción satisfacen sus puertos, comprobado en compilación.
var (
	_ EtapaIdeas            = (*stages.P2)(nil)
	_ EtapaEspecificaciones = (*stages.P3)(nil)
	_ EtapaNormalizacion    = (*stages.P4)(nil)
)

// ErrSinCablear es el worker al que le falta una pieza. Como las etapas, no nace a
// medias: un worker sin store reclamaría nil y un worker sin descifrador no podría
// abrir un solo sobre, y las dos cosas se descubrirían en producción.
var ErrSinCablear = errors.New("pipeline: el worker necesita log, store, las tres etapas y el descifrador")

// ---------------------------------------------------------------------------
// EL PLAZO POR LLAMADA — EL HUECO QUE T2.3 SEÑALÓ, CON EL NÚMERO QUE HAY
// ---------------------------------------------------------------------------

// PlazoPorLlamadaSuelo es el plazo que el worker le pone a CADA llamada al modelo.
//
// 🔴 ES UN SUELO CALCULADO, NO UN p99, Y LA DIFERENCIA IMPORTA. La condición que un
// plazo `D` por llamada tiene que cumplir la dejó escrita T2.3 (bloque de cabecera de
// p3.go), y son dos desigualdades:
//
//	D − MargenVeredicto > max(P3)          (que la llamada no muera antes de contestar)
//	(D − MargenVeredicto) × 0,8 > p99(P3)  (que una P3 sana no cuente como lenta)
//
// Con `MargenVeredicto` = 7 s y el máximo OBSERVADO de 32 s, la primera pide D > 39 s
// y la segunda —usando ese mismo 32 s en lugar del p99 que no existe— pide D > 47 s.
// De ahí sale 48 s: es el número más pequeño que satisface lo que HAY MEDIDO.
//
// LO QUE FALTA, DICHO SIN ADORNO: `p99(P3)` NO ESTÁ MEDIDO. Lo que hay son DOS
// observaciones (170 y 293 tokens de salida, 22–32 s) y una cifra de planificación
// (≈ 25 s/ítem, D-044.39). El máximo de una muestra de dos NO es un p99 — casi con
// seguridad lo subestima—, así que 48 s es un suelo y el número honesto es ≥ 48 s.
// Cerrarlo pide medir en campo, no decidir en una revisión.
//
// LA CONSECUENCIA QUE HAY QUE SABER ANTES DE SUBIRLO: con D = 48 s un ítem puede
// retener la plaza única 41 s, así que 10 ítems (el tope que traerá T2.6) son ≈ 6:50
// — POR ENCIMA de la métrica reina de «< 5 min». Subir D empeora esa cuenta y bajarlo
// mata llamadas sanas. Es una decisión de producto con coste, y roza T2.6 y T2.7.
//
// POR QUÉ NO SE DEJA A CERO «porque el Edge tiene default»: porque el default es la
// avería. Sin deadline, `local.plazo` (local.go:443) manda `timeout_ms = 30 s`, el
// breaker llama lento a todo lo que pase de 24 s ⇒ una P3 CALIENTE de 27 s ya cuenta
// como lenta y una FRÍA de 32 s muere sin generar un token.
const PlazoPorLlamadaSuelo = 48 * time.Second

// plazoDeCierre es cuánto se le da a la escritura del DESENLACE (Finish/Fail/Retry)
// cuando el worker se está apagando. Ver cierre.
const plazoDeCierre = 5 * time.Second

// ---------------------------------------------------------------------------
// CONFIGURACIÓN
// ---------------------------------------------------------------------------

// Config son las perillas del worker. Todas caen a su default con un valor <= 0,
// nunca a cero: un `MaxIntentos` a cero mataría el primer job que tropezara y una
// `Cadencia` a cero haría un bucle de CPU al 100 %.
type Config struct {
	// Cadencia es cada cuánto se pregunta por trabajo nuevo.
	Cadencia time.Duration
	// MaxIntentosCalidad es el techo de intentos cuando la causa es `calidad`.
	MaxIntentosCalidad int
	// MaxIntentosInfra es el techo de intentos cuando la causa es `infra`.
	MaxIntentosInfra int
	// BackoffBase es el primer castigo de la curva exponencial.
	BackoffBase time.Duration
	// BackoffTope es el techo de la curva.
	BackoffTope time.Duration
}

func (c Config) conDefaults() Config {
	if c.Cadencia <= 0 {
		c.Cadencia = CadenciaPorDefecto
	}
	if c.MaxIntentosCalidad <= 0 {
		c.MaxIntentosCalidad = MaxIntentosCalidadPorDefecto
	}
	if c.MaxIntentosInfra <= 0 {
		c.MaxIntentosInfra = MaxIntentosInfraPorDefecto
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = BackoffBasePorDefecto
	}
	if c.BackoffTope <= 0 {
		c.BackoffTope = BackoffTopePorDefecto
	}
	return c
}

// topeDe devuelve el techo de intentos que le toca a una causa. `CausaJobInvalido` no
// aparece: ése no se reintenta nunca y no llega hasta aquí.
func (c Config) topeDe(causa string) int {
	if causa == CausaCalidad {
		return c.MaxIntentosCalidad
	}
	return c.MaxIntentosInfra
}

// ---------------------------------------------------------------------------
// EL WORKER
// ---------------------------------------------------------------------------

// Worker recorre `pending` y encadena las etapas. Una instancia procesa UN job a la
// vez; varias instancias (o varias réplicas del proceso) conviven sin pisarse porque
// el claim usa `FOR UPDATE SKIP LOCKED`.
type Worker struct {
	log    logger.Logger
	store  intake.PipelineStore
	p2     EtapaIdeas
	p3     EtapaEspecificaciones
	p4     EtapaNormalizacion
	cifra  Descifrador
	cfg    Config
	ahora  func() time.Time
	numero func(int, time.Duration, time.Duration) time.Duration
}

// NewWorker construye el worker. Devuelve ErrSinCablear si le falta cualquier pieza.
func NewWorker(log logger.Logger, store intake.PipelineStore,
	p2 EtapaIdeas, p3 EtapaEspecificaciones, p4 EtapaNormalizacion,
	cifra Descifrador, cfg Config) (*Worker, error) {
	if log == nil || store == nil || p2 == nil || p3 == nil || p4 == nil || cifra == nil {
		return nil, ErrSinCablear
	}
	return &Worker{
		log: log, store: store, p2: p2, p3: p3, p4: p4, cifra: cifra,
		cfg: cfg.conDefaults(), ahora: time.Now, numero: espera,
	}, nil
}

// Run bloquea hasta que `ctx` se cancele. Se arranca con `go w.Run(ctx)` sobre el
// MISMO ctx derivado de `signal.NotifyContext` que cierra el resto del proceso: un
// solo Ctrl+C también para el worker, sin un segundo mecanismo de apagado. Es el mismo
// patrón que `integrations.Worker.Run`.
//
// 🔴 EL TICKER NO ES EL BACKOFF. Marca cada cuánto se PREGUNTA si hay trabajo; el
// castigo de un job concreto vive en su `next_attempt_at`, en la base. Un worker que
// durmiera el castigo aquí dejaría parados también a los jobs sanos.
func (w *Worker) Run(ctx context.Context) {
	tic := time.NewTicker(w.cfg.Cadencia)
	defer tic.Stop()

	w.log.Info("pipeline: worker arrancado",
		"cadencia", w.cfg.Cadencia.String(),
		"max_intentos_calidad", w.cfg.MaxIntentosCalidad,
		"max_intentos_infra", w.cfg.MaxIntentosInfra,
		"backoff_base", w.cfg.BackoffBase.String(),
		"backoff_tope", w.cfg.BackoffTope.String())

	w.Drenar(ctx)
	for {
		select {
		case <-ctx.Done():
			w.log.Info("pipeline: worker apagando (contexto cancelado)")
			return
		case <-tic.C:
			w.Drenar(ctx)
		}
	}
}

// Drenar procesa jobs hasta que la cola se queda sin nada reclamable, y devuelve
// cuántos tocó. Es lo que hace que un backlog no tarde `n × Cadencia` en salir.
//
// 🔴 PARA EN CUANTO EL CTX MUERE, y no es un adorno: sin esa comprobación un apagado
// con la cola llena se quedaría atrapado aquí procesando jobs cuyo desenlace ya no se
// puede escribir con el ctx del llamante.
//
// ⚠️ NO HAY TECHO DE VUELTAS, Y ESO SIGNIFICA QUE **LA TERMINACIÓN DE ESTE BUCLE
// DESCANSA ENTERA EN EL BACKOFF**. Un job que tropieza sale de aquí con su
// `next_attempt_at` en el futuro y deja de ser reclamable; si alguien rompiera esa
// mitad —devolver el job con `Release` en vez de con `Retry`, o empujar la marca a
// `now()`—, `Drenar` giraría para siempre reclamando y fallando el mismo job: la
// tormenta que describe la 0078, ahora dentro de un bucle. Está MEDIDO, no supuesto:
// la mutación M1 de T2.5 (cambiar `Retry` por `Release`) cuelga el test hasta que salta
// el `-timeout` de `go test`. No se pone un techo aquí porque sería una guarda sobre
// un camino que el backoff ya cierra —y que un test de integración custodia—, y porque
// un techo convertiría un backlog legítimo de N jobs en N/techo pasadas.
func (w *Worker) Drenar(ctx context.Context) int {
	n := 0
	for ctx.Err() == nil {
		hubo, err := w.UnaVuelta(ctx)
		if err != nil {
			w.log.Error("pipeline: no se pudo reclamar trabajo", "error", err)
			return n
		}
		if !hubo {
			return n
		}
		n++
	}
	return n
}

// UnaVuelta reclama UN job y lo lleva hasta su desenlace. Devuelve `false` sin error
// cuando no había nada que reclamar, que es el estado normal del worker la mayor parte
// del tiempo.
//
// El error que devuelve es SOLO el del claim: los fallos del job en sí NO salen por
// aquí, porque no son fallos del worker — se resuelven dentro (backoff o `failed`) y
// el bucle sigue. Devolverlos haría que un job envenenado parase el drenaje, que es
// justo lo que el criterio «NUNCA bloquea otros jobs» prohíbe.
func (w *Worker) UnaVuelta(ctx context.Context) (bool, error) {
	job, hubo, err := w.store.ClaimNext(ctx)
	if err != nil {
		return false, err
	}
	if !hubo {
		return false, nil
	}
	w.procesar(ctx, job)
	return true, nil
}

// procesar lleva un job YA RECLAMADO hasta un desenlace. Siempre acaba en uno de
// cuatro sitios, y ninguno deja el job en `processing`:
//
//	done    — la cadena entera salió (terminar)
//	pending — tropiezo reintentable, con la marca empujada (tropiezo → Retry)
//	failed  — job inválido, o techo de intentos agotado (tropiezo → Fail)
//	nada    — el job se movió bajo los pies (ErrJobFueraDeProcessing): ya lo terminó otro
func (w *Worker) procesar(ctx context.Context, job intake.ClaimedJob) {
	literal, err := w.literalDe(job)
	if err != nil {
		// 🔴 SE CORTA ANTES DE LLAMAR AL MODELO, sea cual sea el motivo: un prompt sin
		// texto del cliente es el accidente que D-044.24 describe —lo único concreto
		// dentro serían los productos que listamos NOSOTROS— y además tira 22–32 s de
		// la plaza única para nada.
		//
		// Qué desenlace le toca lo decide `tropiezo`, que ya sabe distinguir el job
		// inválido (sin sobre, no mejora con reintentos) del descifrado que falló
		// (KEK/KMS, transitorio). Se pasa por ahí y no se llama a `matar` directo
		// para que la clasificación viva en UN solo sitio: dos clasificaciones del
		// mismo error es la forma clásica de que una política se aplique a medias.
		w.tropiezo(ctx, job, "", 0, err)
		return
	}
	if err := w.cadena(ctx, job, literal); err != nil {
		return // la cadena ya escribió el desenlace
	}
	w.terminar(ctx, job)
}

// literalDe abre el sobre de tres piezas. Distingue los DOS motivos por los que puede
// no haber texto, porque tienen desenlaces distintos:
//
//   - el sobre viene INCOMPLETO ⇒ el compositor del flush no llegó a escribirlo (T1.4
//     devolvió false, o la ventana cerró sin una línea del hilo). El job es inválido y
//     no hay reintento que lo arregle;
//   - el sobre está entero pero NO DESCIFRA ⇒ la KEK no desenvuelve. Eso SÍ puede ser
//     transitorio (KMS caído) y lo trata la cadena como infraestructura.
//
// 🔴 Un job terminal nunca se reclama, así que un sobre vacío aquí NO es INV-13 en
// acción: es exactamente el primer caso, tal como avisa `intake.ClaimedJob.SourceText`.
func (w *Worker) literalDe(job intake.ClaimedJob) (string, error) {
	if !job.SourceText.Complete() {
		return "", fmt.Errorf("%w (el compositor del flush no llegó a escribir el sobre)", stages.ErrSinLiteral)
	}
	texto, err := w.cifra.Decrypt(job.SourceText.Enc, job.SourceText.DEK, job.SourceText.KEKID)
	if err != nil {
		// El error NO se enriquece con nada del texto: lo que falló es el sobre.
		return "", fmt.Errorf("descifrar el literal del job: %w", err)
	}
	if texto == "" {
		return "", fmt.Errorf("%w (el sobre descifró a cadena vacía)", stages.ErrSinLiteral)
	}
	return texto, nil
}

// cadena encadena P2 → P3 → P4 y devuelve error si alguna etapa no pudo producir su
// artefacto. Cuando lo devuelve, el desenlace YA está escrito.
//
// 🔴 EL CTX QUE VIAJA AQUÍ NO LLEVA DEADLINE, Y ES DELIBERADO. El plazo se acota POR
// LLAMADA dentro de cada etapa (`stages.ConPlazoPorLlamada`), no por job ni por etapa:
// un deadline de job se REPARTE entre las N llamadas del fan-out de P3 —la primera se
// lleva casi todo y la última casi nada— y el `timeout_ms` que llega al Edge deja de
// describir una llamada de lote, con lo que el breaker juzga contra un umbral que se
// mueve. Ver PlazoPorLlamadaSuelo y stages/plazo.go.
func (w *Worker) cadena(ctx context.Context, job intake.ClaimedJob, literal string) error {
	ideas, err := w.ideas(ctx, job, literal)
	if err != nil {
		return err
	}
	if len(ideas.Wants) == 0 {
		w.sinIdeas(job)
	}
	specs, err := w.especificaciones(ctx, job, literal, ideas.Wants)
	if err != nil {
		return err
	}
	_, err = w.cantidades(ctx, job, literal, specs.Items, ideas.DeliveryHint)
	return err
}

// sinIdeas es el aviso del job que llega a P3 con CERO ideas vivas.
//
// # LA DECISIÓN, ESCRITA AQUÍ PORQUE NO TENÍA DUEÑO
//
// `llm.ParseMainIdeas` declara VÁLIDA una lista de `wants` vacía (`validarWants` es un
// bucle sin comprobar la longitud), así que un job al que el anclaje le tire TODAS las
// ideas persiste `{"version":1,"wants":[]}` y sigue camino. T2.2 lo dejó apuntado sin
// dueño. **La cadena NO se corta, y este aviso es el precio.**
//
// POR QUÉ NO SE CORTA:
//
//  1. design §3.2 lo declara explícitamente no fatal («cero resultados válidos tampoco
//     es fatal») y el plan manda CONSERVADOR: una salida mala nunca descarta la
//     solicitud del cliente;
//  2. es un caso LEGÍTIMO y frecuente, no solo la patología: la ventana de agregación
//     se abre con cualquier mensaje, así que un «hola» produce un job cuyo P2 honesto
//     no tiene nada que extraer. Matarlo llenaría la bandeja del dueño de `failed`
//     que no son fallos de nadie;
//  3. no cuesta plaza: con cero ideas, P3 no llama al modelo (design §3.2) y P4
//     tampoco (`len(items) > 0`). Lo que sigue son dos escrituras, no dos inferencias.
//
// 🔴 EL AVISO NOMBRA LOS DOS ORÍGENES Y NO AFIRMA NINGUNO, que es lo honesto desde
// aquí: el worker recibe la lista YA anclada y no puede distinguir «el modelo no
// devolvió ideas» de «el anclaje las descartó todas». Quien SÍ lo sabe es P2, que
// registra `ideas` e `ideas_descartadas` para el mismo `job_id` — por eso el mensaje
// manda a buscar allí en vez de inventarse la causa. Un texto que afirmara una de las
// dos mentiría en la mitad de los casos.
func (w *Worker) sinIdeas(job intake.ClaimedJob) {
	w.log.Warn("pipeline: P2 no dejó ni una idea viva; el job sigue y terminará con un borrador VACÍO",
		"job_id", job.ID, "stage", intake.StageP2,
		"causa", "indeterminada_desde_aqui",
		"donde_mirar", "el log de p2 para este job_id trae `ideas` e `ideas_descartadas`: si `ideas_descartadas` es 0, el modelo no devolvió ninguna; si no, las descartó el anclaje")
}

// ---------------------------------------------------------------------------
// LAS TRES ETAPAS, CON SU REANUDACIÓN Y SU CRONÓMETRO
// ---------------------------------------------------------------------------

// ideas ejecuta P2 — o se la salta si el artefacto ya está persistido.
func (w *Worker) ideas(ctx context.Context, job intake.ClaimedJob, literal string) (*llm.MainIdeas, error) {
	if art, ok := reanudar[llm.MainIdeas](w, job, intake.StageP2); ok {
		return art, nil
	}
	inicio := w.ahora()
	out, err := w.p2.Run(ctx, job, literal)
	return desenlace(ctx, w, job, intake.StageP2, inicio, out, err)
}

// especificaciones ejecuta P3 — o se la salta si el artefacto ya está persistido.
func (w *Worker) especificaciones(ctx context.Context, job intake.ClaimedJob,
	literal string, wants []llm.Want) (*stages.ArtefactoP3, error) {
	if art, ok := reanudar[stages.ArtefactoP3](w, job, intake.StageP3); ok {
		return art, nil
	}
	inicio := w.ahora()
	out, err := w.p3.Run(ctx, job, literal, wants)
	return desenlace(ctx, w, job, intake.StageP3, inicio, out, err)
}

// cantidades ejecuta P4 — o se la salta si el artefacto ya está persistido.
func (w *Worker) cantidades(ctx context.Context, job intake.ClaimedJob, literal string,
	items []llm.ItemSpec, entrega *llm.Hint) (*llm.Quantities, error) {
	if art, ok := reanudar[llm.Quantities](w, job, intake.StageP4); ok {
		return art, nil
	}
	inicio := w.ahora()
	out, err := w.p4.Run(ctx, job, literal, items, entrega)
	return desenlace(ctx, w, job, intake.StageP4, inicio, out, err)
}

// desenlace es el punto ÚNICO por el que pasa el resultado de toda etapa ejecutada, y
// existe para que el `elapsed` no se pueda colgar solo del camino feliz.
//
// 🔴 §5.2·bis EN UNA FUNCIÓN. El cronómetro se para ANTES de mirar el error, y el
// número sale por los DOS caminos: en el Info de la etapa que salió y en el Warn/Error
// del tropiezo. Una etapa que muere por timeout es precisamente la que más falta hace
// medir —es la que dice cuánto tardó el plazo en morder— y es la que un `log.Info` al
// final del camino feliz nunca vería.
//
// Es función libre y no método porque lleva parámetro de tipo, y Go no admite métodos
// genéricos. El `ctx` va PRIMERO —antes incluso que el receptor de mentira— porque lo
// exigen a la vez `revive/context-as-argument` y `contextcheck`.
func desenlace[T any](ctx context.Context, w *Worker, job intake.ClaimedJob, etapa string,
	inicio time.Time, out *T, err error) (*T, error) {
	transcurrido := w.ahora().Sub(inicio)
	if err != nil {
		w.tropiezo(ctx, job, etapa, transcurrido, err)
		return nil, err
	}
	w.log.Info("pipeline: etapa completada",
		"job_id", job.ID, "stage", etapa, "elapsed_ms", transcurrido.Milliseconds(),
		"intento", job.Attempts+1)
	return out, nil
}

// reanudar es LA REANUDACIÓN POR ESTADO: si el artefacto de la etapa ya está en la
// fila, se decodifica y la etapa NO se ejecuta. Es lo que hace que una redelivery no
// vuelva a pagar los 22–32 s de una llamada que ya se pagó.
//
// # POR QUÉ `json.Unmarshal` Y NO EL `llm.Parse*` DE LA ETAPA
//
// Porque lo que hay en la fila NO es la salida cruda del modelo: es el artefacto que la
// etapa ya validó, ancló y volvió a serializar. Pasarlo otra vez por el parser de
// calidad haría que un artefacto legítimo —por ejemplo uno con `wants` vacío tras el
// anclaje, o uno cuyo `Parse*` se vuelva más estricto en una versión futura de
// `wapp-shared/llm`— se rechazara a sí mismo, y el job se quedaría reintentando para
// siempre una etapa que ya había salido bien.
//
// # UN ARTEFACTO ILEGIBLE NO MATA EL JOB: SE REHACE LA ETAPA
//
// Es lo conservador. La alternativa —fallar— tiraría un pedido real por un JSON que
// nosotros mismos escribimos mal, y rehacerla como mucho cuesta una llamada. El aviso
// va a Warn porque, si aparece, algo escribió en `artifacts` una forma que este código
// no entiende, y eso hay que mirarlo.
func reanudar[T any](w *Worker, job intake.ClaimedJob, etapa string) (*T, bool) {
	raw, ok := job.Artifacts[etapa]
	if !ok || len(raw) == 0 {
		return nil, false
	}
	var art T
	if err := json.Unmarshal(raw, &art); err != nil {
		// El error NO cita `raw`: el artefacto lleva frases del cliente (ADR-0034).
		w.log.Warn("pipeline: el artefacto persistido no se pudo decodificar; la etapa se rehace",
			"job_id", job.ID, "stage", etapa, "error", err.Error())
		return nil, false
	}
	w.log.Debug("pipeline: etapa saltada, su artefacto ya estaba persistido",
		"job_id", job.ID, "stage", etapa)
	return &art, true
}

// ---------------------------------------------------------------------------
// LOS DESENLACES
// ---------------------------------------------------------------------------

// tropiezo es la decisión backoff-vs-failed, y el ÚNICO sitio donde se toma.
//
// `ErrJobFueraDeProcessing` se aparta antes que nada: no es un tropiezo nuestro, es que
// otro worker terminó el job mientras esta cadena corría. Intentar Retry o Fail sobre
// él afectaría 0 filas —el guard de las dos es `status = 'processing'`— y el log diría
// «no aplicó» sin explicar por qué. Se dice lo que pasó y se suelta.
func (w *Worker) tropiezo(ctx context.Context, job intake.ClaimedJob, etapa string,
	transcurrido time.Duration, err error) {
	if errors.Is(err, stages.ErrJobFueraDeProcessing) {
		w.log.Info("pipeline: el job se movió bajo los pies (ya no estaba en processing); esta cadena lo suelta",
			"job_id", job.ID, "stage", etapa, "elapsed_ms", transcurrido.Milliseconds())
		return
	}
	if errors.Is(err, stages.ErrSinLiteral) {
		w.matar(ctx, job, etapa, CausaJobInvalido, err)
		return
	}

	causa := causaDe(err)
	intento := job.Attempts + 1
	tope := w.cfg.topeDe(causa)
	if intento >= tope {
		w.matar(ctx, job, etapa, causa, fmt.Errorf("agotados los %d intentos: %w", tope, err))
		return
	}

	marca := w.ahora().Add(w.numero(intento, w.cfg.BackoffBase, w.cfg.BackoffTope))
	cerrar, cancel := w.cierre(ctx)
	defer cancel()
	aplicado, rerr := w.store.Retry(cerrar, job.ID, marca)
	if rerr != nil {
		// 🔴 SE DICE, y en Error. Si esto falla, el job se queda en `processing` para
		// siempre y no lo rescata nadie (ver el bloque de `cierre`). Un fallo silencioso
		// aquí sería un job perdido sin una sola línea de log.
		w.log.Error("pipeline: no se pudo reencolar el job con backoff; queda en processing",
			"job_id", job.ID, "stage", etapa, "causa", causa, "error", rerr.Error())
		return
	}
	if !aplicado {
		w.log.Info("pipeline: el reencolado no aplicó (el job ya no estaba en processing)",
			"job_id", job.ID, "stage", etapa, "causa", causa)
		return
	}
	w.log.Warn("pipeline: la etapa falló; el job vuelve a la cola con backoff",
		"job_id", job.ID, "stage", etapa, "causa", causa,
		"elapsed_ms", transcurrido.Milliseconds(),
		"intento", intento, "tope", tope,
		"next_attempt_at", marca.UTC().Format(time.RFC3339),
		"error", err.Error())
}

// matar lleva el job a `failed` con su causa escrita.
//
// 🔴 EL `reason` ES TEXTO DE OPERADOR Y NO PUEDE LLEVAR LITERAL DEL CLIENTE (COMMENT de
// la columna en la 0072, ADR-0034). Lo que se compone son la causa, la etapa y el error
// de la cadena — y los errores de este pipeline están escritos para no citar ni el
// prompt ni la salida del modelo (P2/P3/P4 lo dicen en cada `fmt.Errorf`).
func (w *Worker) matar(ctx context.Context, job intake.ClaimedJob, etapa, causa string, err error) {
	motivo := fmt.Sprintf("causa=%s stage=%s: %v", causa, etapaOTodas(etapa), err)
	cerrar, cancel := w.cierre(ctx)
	defer cancel()
	aplicado, ferr := w.store.Fail(cerrar, job.ID, motivo)
	if ferr != nil {
		w.log.Error("pipeline: no se pudo marcar el job como failed; queda en processing",
			"job_id", job.ID, "stage", etapa, "causa", causa, "error", ferr.Error())
		return
	}
	if !aplicado {
		w.log.Info("pipeline: el fallo no aplicó (el job ya no estaba en processing)",
			"job_id", job.ID, "stage", etapa, "causa", causa)
		return
	}
	w.log.Error("pipeline: job FAILED",
		"job_id", job.ID, "stage", etapaOTodas(etapa), "causa", causa,
		"intento", job.Attempts+1, "error", err.Error())
}

// etapaOTodas nombra la etapa en un mensaje. La cadena vacía significa «murió antes de
// entrar en ninguna» (el job sin literal), y decir `stage=""` en un log obligaría a
// quien lo lea a adivinar si es eso o si alguien se dejó el campo.
func etapaOTodas(etapa string) string {
	if etapa == "" {
		return "ninguna"
	}
	return etapa
}

// terminar cierra el job en `done`.
//
// 🔴 EL `intakeID` VA VACÍO, Y NO ES UN OLVIDO: el borrador es de la Ola 3. Hoy la
// cadena acaba en P4 y `Finish` deja `intake_id` como estaba (su `COALESCE`). Cuando
// la Ola 3 añada `match` y `draft`, se insertan ANTES de esta llamada y el id del
// borrador entra por aquí — que es el único sitio donde puede entrar, porque `done` es
// absorbente y un UPDATE posterior afectaría 0 filas.
//
// ⚠️ CONSECUENCIA DE TERMINAR HOY EN P4: `Finish` vacía el sobre del literal en la
// misma sentencia (INV-13), así que un job `done` de la era Ola 2 ya no tiene texto que
// reanalizar. Lo que le queda son sus artefactos `p2`/`p3`/`p4`, que es exactamente lo
// que el match necesita. Un `/reanalyze` (T4.6) que quisiera rehacer P2 sobre un job ya
// terminado no puede, y eso es INV-13 funcionando, no un defecto.
func (w *Worker) terminar(ctx context.Context, job intake.ClaimedJob) {
	cerrar, cancel := w.cierre(ctx)
	defer cancel()
	aplicado, err := w.store.Finish(cerrar, job.ID, "")
	if err != nil {
		w.log.Error("pipeline: no se pudo terminar el job; queda en processing",
			"job_id", job.ID, "error", err.Error())
		return
	}
	if !aplicado {
		w.log.Info("pipeline: el cierre no aplicó (el job ya no estaba en processing)",
			"job_id", job.ID)
		return
	}
	w.log.Info("pipeline: job DONE",
		"job_id", job.ID, "tenant_id", job.Key.TenantID, "intento", job.Attempts+1)
}

// cierre da el ctx con el que se escriben los DESENLACES (Finish, Fail, Retry).
//
// 🔴 SOBREVIVE A LA CANCELACIÓN DEL WORKER, `context.WithoutCancel`, y ese detalle es
// lo que evita el modo de fallo peor de todo este fichero: con el ctx del llamante, un
// apagado (o un despliegue) durante una etapa cancelaría también la escritura del
// desenlace, y el job se quedaría en `processing` PARA SIEMPRE — `ClaimNext` solo mira
// `pending`, así que nadie lo volvería a tocar y el cliente se quedaría sin
// presupuesto sin que nada diera error. Con esto, el apagado ordenado deja el job
// reencolado. Es el mismo criterio del `context.WithoutCancel` de `intakeahead`.
//
// ⚠️ LO QUE ESTO NO CUBRE, DICHO CLARO: un SIGKILL o una caída dura del proceso sí deja
// el job en `processing` sin rescate. `intake_jobs` no tiene el `claimed_at` que
// `webhook_outbox` usa para recuperar huérfanos (0049) y añadirlo es una migración con
// una decisión dentro —cuánto puede durar legítimamente un `processing`—. 🔄 Esa
// decisión YA NO ES INCALCULABLE por la mitad de T2.6: con el tope en 10 ítems y
// `PlazoPorLlamadaSuelo` en 48 s, la cadena entera tiene un techo aritmético (P2 + 10 ×
// 48 s + P4 ≈ 9 min de peor caso). Lo que sigue faltando es el aforo (T2.7): sin él, N
// cadenas del mismo Edge se estorban y ese techo se multiplica por N. Queda fuera de
// T2.5 A PROPÓSITO y escrito aquí para que no se descubra en campo.
func (w *Worker) cierre(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), plazoDeCierre)
}
