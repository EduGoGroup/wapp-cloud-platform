// Package pipeline es EL WORKER del pipeline de presupuestos (Plan 044 · Ola 2 ·
// T2.5): el llamante que ninguna etapa tenía. Reclama un job `pending`, descifra su
// literal, encadena P2 → P3 → P4 → match → draft y lo termina — o lo devuelve a la
// cola castigado, o lo mata con su causa escrita.
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
//   - 🔄 YA TIENE AFORO POR EDGE (T2.7, 2026-08-25). Esta línea decía que no, y era
//     verdad hasta hoy. Dos cadenas de lote del MISMO Edge ya NO pueden solaparse: el
//     worker toma la plaza `(tenant, Edge)` antes de la cadena y la suelta al acabar
//     (ver plaza.go). Lo que sigue siendo cierto es el resto del párrafo: el aforo
//     acota la espera de un turno interactivo a UNA llamada de lote, no a cero.
//     🔄 El tope de ítems (T2.6) SÍ está puesto desde el 2026-08-25, y no aquí: lo
//     aplica `stages.acotarAlTope` a la entrada de P3, que es donde se gasta la plaza
//     (ver `stages/tope.go`). Un pedido de 40 ítems ya no hace 40 llamadas: hace 10 y
//     deja las otras 30 MARCADAS. Lo que sigue siendo verdad es la cuenta de tiempo:
//     esos 10 ítems son 320–410 s, por encima de «< 5 min».
//   - 🔄 YA CREA EL BORRADOR (T3.8, 2026-08-26). Esta línea decía que no —«`match` y
//     `draft` son de la Ola 3; hoy la cadena acaba en P4 y el job pasa a `done` sin
//     `intake_id`»— y era verdad hasta hoy: la Ola 3 escribió las dos etapas con sus
//     tests y NADIE las construía ni las llamaba, exactamente el mismo defecto que la
//     Ola 2 tuvo que cerrar con T2.9. La cadena llega ahora hasta `draft` y el job
//     termina CON su `intake_id` (ver terminar).
//   - NO ANCLA LOS ADJUNTOS. `EntradaDraft.Media` viaja en CERO: la heurística de
//     T3.3 (`internal/intake/anclaje`) sigue sin llamante de producción, y cablearla
//     pide un lector que hoy no existe —`events.ThreadEntry` no trae ni los `media
//     refs` ni el instante de cada turno, que son las DOS entradas de `Repartir`—.
//     Es un hueco NOMBRADO, no un olvido: sin él el borrador sale entero y sin
//     adjuntos, con él saldría con las fotos colgadas de su línea.
//   - NO RELLENA LA VÍA DEL ANÁLISIS (`Analisis.Provider`, D-044.15). Quien sabe si
//     el job corrió por `local` o por `api` es `llmvia.Selector`, y no lo publica por
//     ningún puerto: se resuelve DENTRO de cada etapa, por llamada. El borrador sale
//     igual y `draft` lo dice en un Warn; sacarlo de ahí es cambiar el contrato del
//     selector, que es una decisión de otra tarea.
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
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/anclaje"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
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

// EtapaMatch es `match`, la primera etapa que NO habla con un modelo en su camino
// normal. Lo satisface `*stages.Match`.
type EtapaMatch interface {
	Run(ctx context.Context, job intake.ClaimedJob, in stages.EntradaMatch) (*stages.ArtefactoMatch, error)
}

// EtapaDraft es `draft`, la última: la que escribe FUERA de `intake_jobs` y devuelve
// el `intake_id` con el que se cierra el job. Lo satisface `*stages.Draft`.
type EtapaDraft interface {
	Run(ctx context.Context, job intake.ClaimedJob, in stages.EntradaDraft) (*stages.ArtefactoDraft, error)
}

// Catalogos es de dónde sale el índice del catálogo del tenant. Lo satisface
// `*catalogo.Cache`.
//
// 🔴 ES UN PUERTO DEL WORKER Y NO DE LA ETAPA, Y ESA FRONTERA ES EL CRITERIO (a) DE
// T3.7. El worker lo consulta UNA VEZ POR JOB y le pasa el `*Indice` ya construido a
// `match`, que busca por cada ítem; el `*Indice` no tiene con qué leer nada, así que
// no hay forma de que el ítem número 7 de un pedido dispare un SELECT. Si este puerto
// bajara a la etapa, esa garantía dejaría de estar en los tipos y pasaría a depender
// de que nadie llame a `Obtener` dentro del bucle.
type Catalogos interface {
	Obtener(ctx context.Context, tenantID string) (*catalogo.Indice, error)
}

// ZonasDeEnvio son las zonas de `tenant_settings.shipping_zones` del tenant. Lo
// satisfacen `*intakes.Postgres` y `*intakes.MemoryStore`.
//
// Se lee también UNA VEZ POR JOB y por el mismo motivo que el catálogo: la etapa
// `match` construye la línea de envío con `intakes.DesiredShippingLine` y no debe
// poder consultar la base.
type ZonasDeEnvio interface {
	ShippingZones(ctx context.Context, tenantID string) ([]intakes.ShippingZone, error)
}

// Las CINCO etapas de producción y las dos fuentes por job satisfacen sus puertos,
// comprobado en compilación.
var (
	_ EtapaIdeas            = (*stages.P2)(nil)
	_ EtapaEspecificaciones = (*stages.P3)(nil)
	_ EtapaNormalizacion    = (*stages.P4)(nil)
	_ EtapaMatch            = (*stages.Match)(nil)
	_ EtapaDraft            = (*stages.Draft)(nil)
	_ Catalogos             = (*catalogo.Cache)(nil)
	_ ZonasDeEnvio          = (*intakes.Postgres)(nil)
	_ ZonasDeEnvio          = (*intakes.MemoryStore)(nil)
)

// ErrSinCablear es el worker al que le falta una pieza. Como las etapas, no nace a
// medias: un worker sin store reclamaría nil y un worker sin descifrador no podría
// abrir un solo sobre, y las dos cosas se descubrirían en producción.
//
// 🔴 LAS CINCO ETAPAS Y EL CATÁLOGO SON OBLIGATORIOS, Y `match`/`draft` LO SON DESDE
// T3.8 A PROPÓSITO. La alternativa —cablearlas como `Opcion`, que es como entró el
// aforo— haría que un worker sin borrador siguiera compilando, siguiera pasando los
// tests y siguiera terminando jobs en `done` sin `intake_id`: exactamente el estado
// del que esta tarea saca al pipeline. Lo que no puede omitirse sin error no vuelve
// a quedarse apagado ocho tareas.
var ErrSinCablear = errors.New("pipeline: el worker necesita log, store, las CINCO etapas, el catálogo y el descifrador")

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
	log   logger.Logger
	store intake.PipelineStore
	p2    EtapaIdeas
	p3    EtapaEspecificaciones
	p4    EtapaNormalizacion
	match EtapaMatch
	draft EtapaDraft
	// catalogos y zonas son las DOS lecturas por job de la Ola 3: lo que `match`
	// necesita del tenant y no puede ir a buscar por sí misma. El catálogo es
	// obligatorio —sin índice ningún ítem casa y la etapa devuelve ErrSinCatalogo—;
	// las zonas no lo son: un tenant sin zonas configuradas es el caso normal y su
	// borrador sale con «Envío por confirmar», que es lo que D-041.11 manda.
	catalogos Catalogos
	zonas     ZonasDeEnvio
	cifra     Descifrador
	cfg       Config
	ahora     func() time.Time
	numero    func(int, time.Duration, time.Duration) time.Duration

	// aforo y plazas son EL ENTERO de T2.7 y quien sabe a qué plaza apunta un job.
	// Van en pareja y nacen juntos (ConAforo): un aforo sin quien resuelva la
	// dirección no podría indexar nada, y un resolutor sin aforo no serviría a nadie.
	// Los DOS nil = worker sin aforo, que es el estado de T2.5 y sigue siendo legal
	// (lo grita el arranque, ver Run).
	aforo  *Aforo
	plazas Plazas

	// despertares es el DISPARADOR POR EVENTO (D-044.43): por aquí entra «el Edge de
	// este tenant acaba de decir que puede». Tiene buffer y el envío es NO
	// BLOQUEANTE (ver Despertar) porque quien lo llena es el bucle Recv del gateway,
	// que no puede esperar a nadie.
	despertares chan Plaza
}

// Opcion es una perilla del worker que NO es un número: un colaborador. Se separan de
// Config a propósito —Config son las perillas que un operador puede querer mover desde
// el arranque; esto son piezas que se cablean una vez— y son variádicas para que el
// worker de T2.5 siga construyéndose igual.
type Opcion func(*Worker)

// ConAforo le da al worker el entero de T2.7 y quien resuelve la dirección de la
// plaza. Los dos o ninguno: con uno solo, la opción no hace nada y el worker sigue sin
// aforo (y lo grita al arrancar).
//
// `plazas` lo satisface *llmvia.Selector, que es quien sabe la vía del tenant — y por
// tanto quien sabe que por vía API NO HAY PLAZA que tomar. El worker nunca pregunta
// eso: recibe un `ok` y ya.
func ConAforo(a *Aforo, plazas Plazas) Opcion {
	return func(w *Worker) {
		if a == nil || plazas == nil {
			return
		}
		w.aforo, w.plazas = a, plazas
	}
}

// ConZonasDeEnvio le da al worker de dónde leer `tenant_settings.shipping_zones`
// (T3.8). Es OPCIÓN y no parámetro obligatorio, y la razón es que su ausencia no
// inventa nada: sin lector, `match` recibe cero zonas y la línea de envío sale
// «Envío por confirmar» a precio vacío — que es EXACTAMENTE la misma línea que sale
// para un tenant que tiene 0 zonas o más de una (`intakes.DesiredShippingLine`), o
// sea el caso mayoritario.
//
// 🔴 Y POR ESO MISMO ES OMISIBLE SIN SÍNTOMA, ASÍ QUE LLEVA DOS REDES. Lo único que
// se pierde al olvidarla es la tarifa plana del tenant con UNA zona configurada, y
// eso no da error: da un borrador con un renglón de envío sin precio que el dueño
// rellena a mano sin saber que su configuración existía. Las redes son el Warn del
// arranque (ver Run) y el criterio (e) de TestPipelineCaptacionCableado, que exige
// verla en `bootstrap.go`.
func ConZonasDeEnvio(z ZonasDeEnvio) Opcion {
	return func(w *Worker) {
		if z == nil {
			return
		}
		w.zonas = z
	}
}

// NewWorker construye el worker. Devuelve ErrSinCablear si le falta cualquier pieza.
//
// Las CINCO etapas van POSICIONALES y EN EL ORDEN DE LA CADENA (`p2 → p3 → p4 →
// match → draft`), detrás el catálogo que `match` consume. No hay riesgo de
// equivocar el orden: los cinco puertos tienen firmas distintas, así que una
// permutación no compila.
//
// Las OPCIONES son de T2.7 y T3.8, y las dos comparten forma: cablean algo cuya
// ausencia NO da error, solo sirve peor. `ConAforo` (dos cadenas del mismo Edge
// pueden solaparse) y `ConZonasDeEnvio` (el tenant con tarifa plana pierde su
// precio de envío). Las dos lo gritan en el Warn del arranque, porque es el único
// sitio donde pueden gritarlo.
func NewWorker(log logger.Logger, store intake.PipelineStore,
	p2 EtapaIdeas, p3 EtapaEspecificaciones, p4 EtapaNormalizacion,
	match EtapaMatch, draft EtapaDraft, catalogos Catalogos,
	cifra Descifrador, cfg Config, opts ...Opcion) (*Worker, error) {
	if log == nil || store == nil || p2 == nil || p3 == nil || p4 == nil ||
		match == nil || draft == nil || catalogos == nil || cifra == nil {
		return nil, ErrSinCablear
	}
	w := &Worker{
		log: log, store: store, p2: p2, p3: p3, p4: p4,
		match: match, draft: draft, catalogos: catalogos, cifra: cifra,
		cfg: cfg.conDefaults(), ahora: time.Now, numero: espera,
		despertares: make(chan Plaza, capacidadDespertares),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w, nil
}

// capacidadDespertares es cuántos flancos a READY caben esperando a que el worker
// vuelva al select. Treinta y dos y no uno: el flanco es raro (un Edge que arranca o
// que recupera su Ollama), pero llegan en RÁFAGA cuando el Cloud se reinicia y toda la
// flota vuelve a latir a la vez. Lleno ⇒ se descarta el aviso, y descartarlo es seguro:
// el ticker sigue barriendo y el backoff sigue venciendo. Ver Despertar.
const capacidadDespertares = 32

// Despertar es EL DISPARADOR POR EVENTO de la cadena de lote (D-044.43): el Edge
// `edgeID` del tenant `tenantID` acaba de pasar a READY, así que sus jobs `pending`
// se reanudan EN EL ACTO, sin esperar a que venza su backoff.
//
// 🔴 NO BLOQUEA NUNCA, y esa es su única exigencia de contrato. Lo llama el hook
// `OnEdgeReady` del gateway, que corre INLINE en la goroutine del bucle Recv del
// stream: bloquear ahí pararía la recepción de TODOS los frames de ese Edge. Si el
// buffer está lleno se descarta el aviso y se dice en Debug —el barrendero sigue
// siendo el backoff, que es justo el reparto de papeles que D-044.43 escribe.
//
// Es seguro llamarlo desde cualquier goroutine y antes de que Run arranque: el canal
// existe desde NewWorker.
func (w *Worker) Despertar(tenantID, edgeID string) {
	p := Plaza{TenantID: tenantID, EdgeID: edgeID}
	if !p.Valida() {
		return
	}
	select {
	case w.despertares <- p:
	default:
		w.log.Debug("pipeline: aviso de Edge READY descartado (buzón lleno); lo recogerá el backoff",
			"tenant_id", tenantID, "edge_id", edgeID)
	}
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
		"backoff_tope", w.cfg.BackoffTope.String(),
		"aforo_por_edge", w.aforo != nil)

	// 🔴 EL AVISO VA EN EL ARRANQUE Y EN Warn PORQUE UN AFORO AUSENTE NO DA NINGÚN
	// OTRO SÍNTOMA. Sin él, el sistema no falla: sirve, y de vez en cuando dos
	// cadenas del mismo Edge se pisan y un turno interactivo espera el doble. Eso no
	// deja rastro en ningún log, así que el rastro se pone aquí.
	if w.aforo == nil {
		w.log.Warn("pipeline: worker SIN aforo por Edge (T2.7); dos cadenas de lote del mismo Edge pueden solaparse",
			"consecuencia", "la espera de un turno interactivo deja de estar acotada a UNA llamada de lote")
	}
	// 🔴 MISMO MOTIVO QUE EL DE ARRIBA: un lector de zonas ausente no falla, sirve
	// peor y en silencio. La línea de envío sale «Envío por confirmar» sin precio,
	// que es indistinguible del borrador de un tenant que no configuró zonas.
	if w.zonas == nil {
		w.log.Warn("pipeline: worker SIN lector de zonas de envío (T3.8); todo borrador saldrá con la línea de envío SIN precio",
			"consecuencia", "el tenant con UNA zona configurada pierde su tarifa plana y el dueño la precifica a mano sin saber que ya estaba puesta")
	}

	w.Drenar(ctx)
	for {
		select {
		case <-ctx.Done():
			w.log.Info("pipeline: worker apagando (contexto cancelado)")
			return
		case p := <-w.despertares:
			// El flanco a READY: se atiende ANTES de que venza ningún backoff, que es
			// todo el propósito. Ver DrenarDespierto.
			w.DrenarDespierto(ctx, p)
		case <-tic.C:
			w.Drenar(ctx)
		}
	}
}

// DrenarDespierto atiende un flanco a READY: procesa los jobs `pending` de ese tenant
// SIN MIRAR SU BACKOFF, y devuelve cuántos tocó.
//
// # POR QUÉ EL CLAIM ES POR TENANT SI LA PLAZA ES POR EDGE — Y NO ES UNA INCOHERENCIA
//
// 🔴 `intake_jobs` NO TIENE COLUMNA `edge_id`, y no la tiene porque el job no nace de
// un Edge: nace de una VENTANA de conversación (`tenant_id`, `session_id`,
// `contact_id`, `event_id`, migración 0072). El enunciado de D-044.43 —«los jobs
// pending de ESE EDGE se reanudan»— no es escribible tal cual, y esto es lo más
// cercano que sí lo es. No es una aproximación floja: el enrutado de una inferencia
// (`gatewaygrpc.inferenceSession`) manda el job de un tenant al Edge que esté VIVO —la
// sesión de origen si respira, y si no la primera alfabética—, así que «los jobs que
// este Edge acaba de desbloquear» y «los jobs pendientes de este tenant» coinciden
// siempre que el tenant tenga un Edge, y ese es el caso que dispara el flanco. Donde
// no coincidan, el que sobra se encuentra la plaza ocupada y espera: el aforo sigue
// protegiendo la máquina.
//
// El `edge_id` del flanco NO se tira: viaja al log, que es donde un operador necesita
// leer qué máquina desatascó qué cola.
//
// # POR QUÉ HAY UN CONJUNTO DE VISTOS Y NO SE PUEDE QUITAR
//
// 🔴 PORQUE ESTE BUCLE NO TIENE OTRO FRENO. `Drenar` termina porque un job que
// tropieza sale con su `next_attempt_at` en el futuro y deja de ser reclamable; aquí
// el claim IGNORA esa marca a propósito, así que ese freno no existe: un job que falle
// se reencolaría castigado y el claim volvería a llevárselo EN EL ACTO, para siempre y
// a la velocidad del error. El conjunto de vistos es lo que convierte el flanco en UNA
// pasada por job, que es lo que un evento significa.
func (w *Worker) DrenarDespierto(ctx context.Context, p Plaza) int {
	w.log.Info("pipeline: el Edge acaba de poder servir inferencia; se reanudan sus jobs sin esperar al backoff",
		"tenant_id", p.TenantID, "edge_id", p.EdgeID)

	vistos := make(map[string]struct{})
	n := 0
	for ctx.Err() == nil {
		job, hubo, err := w.store.ClaimNextIgnorandoBackoff(ctx, p.TenantID)
		if err != nil {
			w.log.Error("pipeline: no se pudo reclamar trabajo tras el flanco a READY",
				"tenant_id", p.TenantID, "edge_id", p.EdgeID, "error", err)
			return n
		}
		if !hubo {
			return n
		}
		if _, repetido := vistos[job.ID]; repetido {
			// Ya se procesó en ESTE flanco y volvió a la cola: el flanco terminó su
			// trabajo. Se suelta sin castigo —no ha fallado nada nuevo— y el backoff
			// que le puso su propio tropiezo sigue mandando.
			w.soltarSinCastigo(ctx, job, "ya procesado en este mismo flanco a READY")
			return n
		}
		vistos[job.ID] = struct{}{}
		w.procesar(ctx, job)
		n++
	}
	return n
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
	// 🔴 LA PLAZA SE TOMA AQUÍ: DESPUÉS DE ABRIR EL SOBRE Y ANTES DE LA CADENA.
	//
	// Después del sobre porque descifrar es CPU local y no toca el Ollama de nadie:
	// tomar la plaza antes la retendría durante un descifrado que, si falla, ni
	// siquiera va a llamar al modelo.
	//
	// Antes de la cadena —y no por llamada— porque el entero del ADR-0046 cuenta
	// CADENAS DE LOTE, no peticiones. Un aforo por llamada dejaría a N cadenas
	// intercalando sus peticiones sobre la misma plaza, y entonces un turno
	// interactivo volvería a poder quedar detrás de N llamadas: exactamente el
	// multiplicador que el Mecanismo 1 existe para quitar.
	soltar, err := w.tomarPlaza(ctx, job)
	if err != nil {
		w.soltarSinCastigo(ctx, job, "el worker se apagó esperando plaza")
		return
	}
	defer soltar()

	borrador, err := w.cadena(ctx, job, literal)
	if err != nil {
		return // la cadena ya escribió el desenlace
	}
	w.terminar(ctx, job, borrador)
}

// tomarPlaza resuelve la dirección de la plaza de este job y la ocupa. Devuelve
// SIEMPRE una función de soltar utilizable —vacía cuando no había plaza que tomar— y
// error SOLO si el ctx murió esperando turno.
//
// # LOS TRES CAMINOS SIN PLAZA, Y POR QUÉ NINGUNO DE LOS TRES PARA EL JOB
//
//  1. **Worker sin aforo** (no se cableó ConAforo). Es el worker de T2.5, y sigue
//     siendo legal; el Warn del arranque es su señal.
//  2. **El tenant no ocupa plaza**: está en vía API (allí el tope es de precio, no de
//     capacidad) o no tiene ningún Edge conectado. Lo distingue `llmvia.Selector`, que
//     es el único que puede: aquí las dos cosas se ven igual y se tratan igual.
//  3. **No se pudo preguntar** (la config del tenant no se leyó). 🔴 NO se convierte
//     en tropiezo AQUÍ, y es deliberado: si `tenant_llm` no se puede leer, la primera
//     etapa va a fallar por lo mismo dos líneas más abajo y `tropiezo` lo clasificará
//     con el resto. Clasificarlo también aquí sería la segunda clasificación del mismo
//     error, que es como una política se acaba aplicando a medias.
func (w *Worker) tomarPlaza(ctx context.Context, job intake.ClaimedJob) (func(), error) {
	nada := func() {}
	if w.aforo == nil || w.plazas == nil {
		return nada, nil
	}
	edgeID, hay, err := w.plazas.PlazaDe(ctx, job.Key.TenantID, job.Key.SessionID)
	if err != nil {
		w.log.Warn("pipeline: no se pudo resolver la plaza del job; la cadena sigue SIN aforo",
			"job_id", job.ID, "tenant_id", job.Key.TenantID, "error", err.Error())
		return nada, nil
	}
	p := Plaza{TenantID: job.Key.TenantID, EdgeID: edgeID}
	if !hay || !p.Valida() {
		w.log.Debug("pipeline: el job no ocupa plaza (vía sin plaza, o el tenant no tiene Edge vivo)",
			"job_id", job.ID, "tenant_id", job.Key.TenantID)
		return nada, nil
	}

	// El log de espera SOLO cuando de verdad hay cola: emitirlo siempre lo volvería
	// ruido y dejaría de significar nada el día que haya que buscarlo.
	if w.aforo.Esperando() > 0 {
		w.log.Info("pipeline: la plaza del Edge está ocupada; este job ESPERA (no falla, no se castiga)",
			"job_id", job.ID, "plaza", p.String(), "esperando", w.aforo.Esperando())
	}
	soltar, err := w.aforo.Tomar(ctx, p)
	if err != nil {
		return nil, err
	}
	return soltar, nil
}

// soltarSinCastigo devuelve el job a `pending` TAL COMO ESTABA: sin consumirle el
// intento y sin empujarle la marca. Es el desenlace de lo que no es culpa del job —el
// worker se apagó esperando plaza, o el flanco a READY ya lo había procesado— y por
// eso usa `Release` y no `Retry`.
//
// 🔴 Y POR ESO MISMO NO SE PUEDE USAR EN UN TROPIEZO. `Release` no toca
// `next_attempt_at`, así que el job es reclamable EN EL ACTO: en un camino que vuelve
// a fallar, eso es la tormenta de la 0078 (medida en la mutación M1 de T2.5: cuelga el
// proceso). Aquí es correcto porque los dos llamantes SALEN del bucle inmediatamente
// después.
func (w *Worker) soltarSinCastigo(ctx context.Context, job intake.ClaimedJob, motivo string) {
	cerrar, cancel := w.cierre(ctx)
	defer cancel()
	aplicado, err := w.store.Release(cerrar, job.ID)
	if err != nil {
		w.log.Error("pipeline: no se pudo devolver el job a la cola; queda en processing",
			"job_id", job.ID, "motivo", motivo, "error", err.Error())
		return
	}
	if !aplicado {
		w.log.Info("pipeline: la devolución no aplicó (el job ya no estaba en processing)",
			"job_id", job.ID, "motivo", motivo)
		return
	}
	w.log.Info("pipeline: job devuelto a la cola SIN castigo",
		"job_id", job.ID, "motivo", motivo)
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

// cadena encadena P2 → P3 → P4 → match → draft y devuelve el artefacto de la última
// etapa —el que lleva el `intake_id` con el que se cierra el job— o error si alguna
// no pudo producir el suyo. Cuando devuelve error, el desenlace YA está escrito.
//
// 🔴 EL CTX QUE VIAJA AQUÍ NO LLEVA DEADLINE, Y ES DELIBERADO. El plazo se acota POR
// LLAMADA dentro de cada etapa (`stages.ConPlazoPorLlamada`), no por job ni por etapa:
// un deadline de job se REPARTE entre las N llamadas del fan-out de P3 —la primera se
// lleva casi todo y la última casi nada— y el `timeout_ms` que llega al Edge deja de
// describir una llamada de lote, con lo que el breaker juzga contra un umbral que se
// mueve. Ver PlazoPorLlamadaSuelo y stages/plazo.go.
func (w *Worker) cadena(ctx context.Context, job intake.ClaimedJob, literal string) (*stages.ArtefactoDraft, error) {
	ideas, err := w.ideas(ctx, job, literal)
	if err != nil {
		return nil, err
	}
	if len(ideas.Wants) == 0 {
		w.sinIdeas(job)
	}
	specs, err := w.especificaciones(ctx, job, literal, ideas.Wants)
	if err != nil {
		return nil, err
	}
	cantidades, err := w.cantidades(ctx, job, literal, specs.Items, ideas.DeliveryHint)
	if err != nil {
		return nil, err
	}
	presupuesto, err := w.lineas(ctx, job, cantidades)
	if err != nil {
		return nil, err
	}
	return w.borrador(ctx, job, literal, presupuesto, cantidades.DeliveryDate)
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

// lineas ejecuta `match` — o se la salta si el artefacto ya está persistido.
//
// 🔴 EL CRONÓMETRO ARRANCA ANTES DE LEER EL CATÁLOGO, Y NO ES UN DESCUIDO. Las dos
// lecturas por job (índice y zonas) son parte del coste de esta etapa: si un día el
// `SELECT` del documento se vuelve el cuello, el `elapsed_ms` de `match` tiene que
// enseñarlo. Medir solo `Run` diría que la etapa tarda 2 ms mientras el job espera
// 900 en la base.
func (w *Worker) lineas(ctx context.Context, job intake.ClaimedJob,
	cantidades *llm.Quantities) (*stages.ArtefactoMatch, error) {
	if art, ok := reanudar[stages.ArtefactoMatch](w, job, intake.StageMatch); ok {
		return art, nil
	}
	inicio := w.ahora()
	out, err := w.cruzarConElCatalogo(ctx, job, cantidades)
	return desenlace(ctx, w, job, intake.StageMatch, inicio, out, err)
}

// cruzarConElCatalogo hace las DOS lecturas por job y llama a la etapa.
//
// # POR QUÉ EL CATÁLOGO MATA EL INTENTO Y LAS ZONAS NO
//
// No es simetría rota: son dos daños distintos. Sin índice NINGÚN ítem puede casar y
// el borrador entero saldría `unmatched`, o sea afirmando que el tenant no vende nada
// de lo que el cliente pidió — una mentira sobre el catálogo, y la propia etapa lo
// rechaza con ErrSinCatalogo. Sin zonas se pierde UNA línea de precio, la del envío,
// y el borrador sale con «Envío por confirmar», que es la línea legítima de la
// inmensa mayoría de tenants (D-041.11: v1, el dueño precifica).
//
// Por eso el catálogo se propaga como error —tropiezo, backoff, y el job vuelve— y
// las zonas se degradan con un Warn. Tirar el pedido de un cliente porque
// `tenant_settings` no contestó sería exactamente lo que DEUDA-044.16 prohíbe: que
// la unidad de daño sea el pedido y no el dato que falló.
func (w *Worker) cruzarConElCatalogo(ctx context.Context, job intake.ClaimedJob,
	cantidades *llm.Quantities) (*stages.ArtefactoMatch, error) {
	indice, err := w.catalogos.Obtener(ctx, job.Key.TenantID)
	if err != nil {
		// El error NO cita el documento: dentro van los nombres y precios del tenant.
		return nil, fmt.Errorf("match: leer el catálogo del tenant: %w", err)
	}
	return w.match.Run(ctx, job, stages.EntradaMatch{
		Cantidades: cantidades,
		Indice:     indice,
		Zonas:      w.zonasDe(ctx, job),
		// 🔴 HOY NADIE PRODUCE LA NOTA DEL PEDIDO ENTERO, y el tipo propio de
		// `stages.NotaDePedido` existe para que el hueco se vea en ESTA línea. No es
		// un default cómodo: ninguna etapa anterior la emite y llenarla pide una
		// decisión de producto (ver NotaDePedido en stages/match.go).
		Nota: stages.SinNotaDePedido,
	})
}

// zonasDe lee las zonas de envío del tenant, o devuelve ninguna. NUNCA falla: ver el
// bloque de cruzarConElCatalogo.
func (w *Worker) zonasDe(ctx context.Context, job intake.ClaimedJob) []intakes.ShippingZone {
	if w.zonas == nil {
		return nil // el worker sin lector; ya lo gritó el arranque (ver Run).
	}
	zonas, err := w.zonas.ShippingZones(ctx, job.Key.TenantID)
	if err != nil {
		w.log.Warn("pipeline: no se pudieron leer las zonas de envío; el borrador sale con la línea de envío SIN precio",
			"job_id", job.ID, "stage", intake.StageMatch,
			"tenant_id", job.Key.TenantID, "error", err.Error())
		return nil
	}
	return zonas
}

// borrador ejecuta `draft` — o se la salta si el artefacto ya está persistido.
//
// 🔴 `Media` VA EN CERO Y `Analisis` TAMBIÉN, y las dos ausencias están explicadas en
// la cabecera del paquete: el anclaje de adjuntos (T3.3) no tiene de dónde leer los
// `media refs` con sus instantes, y la vía del análisis (D-044.15) no sale de ningún
// puerto del selector. Se pasan explícitas y no por omisión del struct para que las
// dos se vean aquí el día que alguien vaya a cablearlas.
func (w *Worker) borrador(ctx context.Context, job intake.ClaimedJob, literal string,
	presupuesto *stages.ArtefactoMatch, fechaEntrega string) (*stages.ArtefactoDraft, error) {
	if art, ok := reanudar[stages.ArtefactoDraft](w, job, intake.StageDraft); ok {
		return art, nil
	}
	inicio := w.ahora()
	out, err := w.draft.Run(ctx, job, stages.EntradaDraft{
		Match:        presupuesto,
		SourceText:   literal,
		FechaEntrega: fechaEntrega,
		Media:        anclaje.Reparto{},
		Analisis:     stages.Analisis{},
	})
	return desenlace(ctx, w, job, intake.StageDraft, inicio, out, err)
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

// terminar cierra el job en `done` CON el id del borrador que acaba de nacer.
//
// 🔴 ES EL ÚNICO SITIO DONDE EL `intake_id` PUEDE ENTRAR, y por eso viaja hasta aquí
// desde `draft` en vez de escribirlo la etapa: `done` es absorbente, así que un
// UPDATE posterior afectaría 0 filas y el job quedaría terminado sin apuntar a su
// solicitud — sin error, y sin nada que lo dijera.
//
// 🔄 HASTA T3.8 ESTE PARÁMETRO IBA VACÍO. El comentario que estaba aquí decía «el
// borrador es de la Ola 3; hoy la cadena acaba en P4 y `Finish` deja `intake_id` como
// estaba (su `COALESCE`)», y dejó de ser verdad al cablearse `match` y `draft`.
//
// ⚠️ INV-13 SIGUE MORDIENDO IGUAL: `Finish` vacía el sobre del literal en la misma
// sentencia, así que un job `done` ya no tiene texto que reanalizar. Lo que le queda
// son sus cinco artefactos y su solicitud, que es lo que un `/reanalyze` (T4.6)
// necesita para escribir una revisión más sobre el MISMO borrador. Rehacer P2 sobre
// un job terminado sigue sin poder hacerse, y eso es INV-13 funcionando.
func (w *Worker) terminar(ctx context.Context, job intake.ClaimedJob, borrador *stages.ArtefactoDraft) {
	intakeID := ""
	if borrador != nil {
		intakeID = borrador.IntakeID
	}
	if intakeID == "" {
		// 🔴 A PARTIR DE T3.8 ESTO ES UNA ANOMALÍA, no el caso normal. Se dice porque
		// `Finish` hace `COALESCE`: con la cadena entera corrida, un id vacío cierra el
		// job igual y en silencio, y la bandeja se quedaría sin la solicitud sin que
		// nada fallara. La causa más probable es un artefacto `draft` persistido por
		// una versión anterior y recuperado por `reanudar`.
		w.log.Warn("pipeline: el job termina SIN intake_id; su solicitud no quedará enlazada al job",
			"job_id", job.ID, "tenant_id", job.Key.TenantID,
			"donde_mirar", "artifacts.draft de este job_id: si no trae `intake_id`, lo escribió una versión anterior a T3.8")
	}
	cerrar, cancel := w.cierre(ctx)
	defer cancel()
	aplicado, err := w.store.Finish(cerrar, job.ID, intakeID)
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
		"job_id", job.ID, "tenant_id", job.Key.TenantID,
		"intake_id", intakeID, "intento", job.Attempts+1)
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
// 48 s + P4 ≈ 9 min de peor caso). 🔄 Y el fleco que este párrafo dejaba abierto —«lo
// que sigue faltando es el aforo (T2.7): sin él, N cadenas del mismo Edge se estorban
// y ese techo se multiplica por N»— lo CIERRA T2.7: con el aforo, N cadenas del mismo
// Edge se ponen en fila y el techo por cadena vuelve a ser el aritmético. Lo que el
// aforo NO acota es cuánto puede esperar la que va la última, que es N × ese techo:
// quien decida el `claimed_at` tiene que contar la espera, no solo la cadena.
func (w *Worker) cierre(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), plazoDeCierre)
}
