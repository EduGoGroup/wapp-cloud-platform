// Porta internal/degradation/degradation.go @ ebf4eb7

// Package degradation es el PUNTO DE INYECCIÓN de la notificación al dueño
// cuando el LLM se degrada al Nivel A (Plan 044 · Ola 1.5 · T1.5-4, D-044.32,
// REQ-38, ADR-0044 §5; tabla public.owner_degradation_notices de la migración
// 0075).
//
// # Qué es esto
//
// Cuando el consumo del adaptador de la vía configurada FALLA —el Edge no
// responde el frame de inferencia, el breaker está abierto, el proveedor externo
// devuelve 5xx o la credencial dejó de valer— el sistema degrada al Nivel A (esa
// parte ya es la conducta: REQ-06/INV-10) y el dueño tiene que ENTERARSE. Este
// paquete es dónde se escribe ese enterarse y por dónde se lee.
//
// # Qué NO es, y es la mitad del contrato
//
// 🔴 NO ES UN LOG, Y NO ES UNA MÉTRICA. Un log lo escribe cualquiera con
// cualquier texto; una métrica cuenta todo lo que pasa. Esto avisa a UNA PERSONA,
// y un canal que avisa de más deja de leerse. De ahí las tres restricciones que
// definen el paquete entero:
//
//  1. El motivo es un ENUM CERRADO de OCHO valores (Reason). Los motivos SANOS de
//     alto volumen —atajo determinista, fastlane, «sin texto», umbral no
//     alcanzado— NO tienen constante aquí y el escritor los RECHAZA. Avisar el
//     funcionamiento correcto mata el canal (D-044.32).
//  2. Una degradación SOSTENIDA produce UN aviso, no uno por mensaje. El dedupe
//     lo garantiza la BASE (índice único sobre la ventana), no este código.
//  3. CERO texto libre del cliente (INV-6). Notice no tiene un solo campo donde
//     quepa una frase, y esa ausencia es el mecanismo: lo que no tiene campo no
//     se puede filtrar por descuido.
//
// La entrega push al teléfono NO es de aquí: es del Plan 045/047, que consume
// GET /api/v1/degradation-notices (contrato en design.md §8.2).
//
// Los ficheros: degradation.go (el vocabulario, Notice, el puerto Store y el
// escritor Notifier), postgres.go (el adaptador sobre database/sql) y
// degradationhelpertest (la suite de contrato del puerto y su doble en memoria,
// que solo importan tests).
package degradation

import (
	"context"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Reason es el MOTIVO de la degradación, y es un TIPO PROPIO y no un `string`
// suelto a propósito.
//
// 🔴 EL TIPO NO BASTA, Y ESE ES EL GOTCHA. Go convierte implícitamente una
// constante de cadena sin tipo, así que `Record(ctx, tenant, "fastlane", …)`
// COMPILA aunque "fastlane" no sea ninguna de las ocho constantes de abajo. El
// tipo evita mezclar un motivo con una vía o con un id; lo que evita que entre un
// motivo INVENTADO —o uno SANO— es la guarda en tiempo de ejecución (Valid,
// verificada por Notifier.Record antes de tocar el store) más el CHECK de la
// migración debajo. Tres redes, porque el llamante de mañana no ha leído este
// comentario.
type Reason string

// El vocabulario CERRADO de motivos: OCHO valores, ni uno más. Nació con SEIS
// (Ola 1.5 · T1.5-4) y T1.6-6 lo amplió a ocho el 2026-08-24 con `lease_invalid`
// y `edge_sin_capacidad`. Los literales viajan al wire y a la base: no se
// renombran ni se «normalizan».
//
// Es el MISMO conjunto que acota `owner_degradation_notices_reason_check` en la
// migración 0075 (bloque (b.1)), y crecer el dominio significa editar LOS DOS
// sitios en el mismo commit.
// ⚠️ EL LADO SQL SE EDITA EN LA 0075, NO EN UNA MIGRACIÓN NUEVA, y no es
// preferencia de estilo: bajo full-replay la 0075 vuelve a correr ANTES que
// cualquier migración posterior y su ADD CONSTRAINT valida las filas existentes,
// así que un ensanche desde una 0076 aborta el arranque en cuanto haya UNA fila
// con un motivo nuevo.
// 🔴 ESO NO SE CONFÍA A LA MEMORIA DE NADIE: el test de este fichero LEE los
// `.sql` de disco y compara, como conjunto, la lista del CHECK con Reasons().
// Añadir un motivo en un solo lado lo pone rojo (candado D-F4-2).
//
// Los cuatro primeros y los dos últimos son fallos de la vía LOCAL (el Edge y su
// Ollama); `ReasonAPIError` y `ReasonCredencial`, de la vía API (el proveedor
// externo). ⚠️ El reparto es DESCRIPTIVO, no exclusivo: `ReasonTimeout` es
// plausible en las dos vías —una llamada HTTP a un proveedor también expira— y por
// eso NO existe una función que ate motivo↔vía ni un CHECK que lo haga en la base.
const (
	// ReasonOllamaDown — el Ollama del Edge no está disponible (vía local).
	ReasonOllamaDown Reason = "ollama_down"
	// ReasonBreakerOpen — el breaker del Edge está abierto y devolvió el fallo
	// inmediato en vez de colgarse (ADR-0042, vía local).
	ReasonBreakerOpen Reason = "breaker_open"
	// ReasonEdgeOffline — no hay sesión viva del Edge a la que mandar el frame
	// (vía local).
	ReasonEdgeOffline Reason = "edge_offline"
	// ReasonTimeout — la inferencia no respondió dentro del plazo.
	ReasonTimeout Reason = "timeout"
	// ReasonAPIError — el proveedor externo respondió un error (vía API).
	ReasonAPIError Reason = "api_error"
	// ReasonCredencial — la credencial del tenant no vale: falta, caducó o el
	// proveedor la rechazó (vía API). El nombre va en castellano porque así nació
	// y porque el valor viaja al wire y a la BD: renombrarlo «por coherencia»
	// rompería el contrato de la app sin ganar nada.
	ReasonCredencial Reason = "credencial"
	// ReasonLeaseInvalid — el Edge rechazó servir la inferencia porque no tiene
	// lease vigente: el kill-switch del ADR-0007 hizo su trabajo (vía local). Es
	// uno de los CUATRO errores que REQ-34 obliga al frame a saber nombrar
	// (`ollama_down`, `breaker_open`, `timeout`, `lease_invalid`).
	ReasonLeaseInvalid Reason = "lease_invalid"
	// ReasonEdgeSinCapacidad — el semáforo de concurrencia del Edge rechazó la
	// petición: la máquina del cliente está saturada (vía local).
	//
	// 🔴 SE DISTINGUE DE ReasonTimeout A PROPÓSITO, y fundirlos sería el error
	// natural: los dos acaban en «no hubo inferencia». Pero este vocabulario no
	// describe lo que le pasó al código, describe QUÉ TIENE QUE MIRAR EL DUEÑO, y
	// ahí son opuestos — `timeout` le manda a la red y al enlace; este, a SU
	// equipo. Si se queda sin productor no pasa nada: un valor admitido que nadie
	// usa no corrompe la tabla, y retirarlo costaría una migración con la tabla
	// ya poblada.
	ReasonEdgeSinCapacidad Reason = "edge_sin_capacidad"
)

// Reasons devuelve una COPIA del vocabulario cerrado de motivos: los ocho, en el
// orden del `IN (…)` de la 0075 —ollama_down, breaker_open, edge_offline,
// timeout, api_error, credencial, lease_invalid, edge_sin_capacidad— para que
// leer los dos ficheros a la vez sea leer la misma lista.
//
// La copia no es paranoia decorativa: sin ella, quien lo recorriera tendría en la
// mano el respaldo del slice del paquete y `Reasons()[0] = "fastlane"` abriría el
// vocabulario para todo el proceso. Mutar lo devuelto no cambia ni la siguiente
// llamada ni lo que dice Valid.
func Reasons() []Reason { panic(pendiente.Implementar("degradation.Reasons")) }

// Valid dice si r pertenece al vocabulario cerrado: true SOLO para los ocho
// literales exactos. Un motivo SANO —«fastlane», «atajo_determinista»,
// «sin_texto», «umbral_no_alcanzado»— da false por la MISMA razón que uno
// inventado: ninguno de los dos es un fallo del adaptador, y esta tabla solo
// registra fallos del adaptador.
//
// No recorta espacios, no pliega mayúsculas y no normaliza Unicode: "",
// "OLLAMA_DOWN", " timeout", "timeout\n" y "ｔｉｍｅｏｕｔ" (ancho completo) son
// false.
func (r Reason) Valid() bool { panic(pendiente.Implementar("degradation.Reason.Valid")) }

// String hace de Reason un fmt.Stringer para que un `%s` en un error o en un log
// imprima el valor y no el tipo: devuelve el literal tal cual, también el de un
// motivo fuera del vocabulario.
func (r Reason) String() string { panic(pendiente.Implementar("degradation.Reason.String")) }

// Vocabulario CERRADO de VÍAS. Es el MISMO eje que `tenant_llm.via` (migración
// 0073) y los mismos dos valores.
//
// ⚠️ SE DECLARA AQUÍ EN VEZ DE IMPORTAR tenantllm, y es una decisión: lo que este
// paquete necesita es el VOCABULARIO de dos cadenas, no la credencial del
// tenant, y depender del paquete de la credencial para eso ataría el escritor de
// notificaciones —que vive dentro del pipeline y del gateway— al paquete que
// descifra API keys. Un vocabulario de dos valores duplicado en dos sitios es más
// barato de mantener que esa dependencia. El test de este fichero custodia que
// los valores coincidan literalmente con los de tenantllm.
const (
	// ViaLocal — la inferencia la ejecuta el Edge del propio tenant (ADR-0045).
	ViaLocal = "local"
	// ViaAPI — la inferencia la ejecuta un proveedor externo con la credencial
	// del tenant (ADR-0030).
	ViaAPI = "api"
)

// ValidVia dice si v pertenece al vocabulario cerrado de vías: true SOLO para
// los dos literales exactos, "local" y "api". No recorta espacios, no pliega
// mayúsculas y no normaliza Unicode.
func ValidVia(v string) bool { panic(pendiente.Implementar("degradation.ValidVia")) }

// VentanaPorDefecto es el tamaño de la ventana de dedupe cuando no se configura
// otra: una degradación sostenida produce, como mucho, UN aviso cada 15 minutos
// por cada par (motivo, vía).
//
// POR QUÉ 15 MINUTOS Y NO OTRA COSA: es el techo que hace el peor caso legible
// —4 avisos por hora y por par, 96 al día si TODO falla TODO el tiempo— y a la
// vez es lo bastante corto para que «se cayó, se arregló, se volvió a caer» no se
// funda en un solo aviso que diga «lleva rota tres horas». No es un número
// medido: es un punto de partida razonado, y cambiarlo es cambiar una constante.
const VentanaPorDefecto = 15 * time.Minute

// Errores del escritor. Son centinelas (se comparan con errors.Is) y no cadenas
// formateadas porque el llamante —que está dentro de un camino de fallo— tiene
// que poder distinguir «me equivoqué de motivo» (un defecto SUYO, que debe romper
// sus tests) de «la base no está» (un fallo transitorio que debe reintentar o
// tragarse). Sus textos son exactos.
var (
	// ErrMotivoDesconocido — el motivo no está en el vocabulario cerrado. Incluye
	// el caso IMPORTANTE: un motivo SANO. Ver Notifier.Record.
	ErrMotivoDesconocido = errors.New("degradation: motivo fuera del vocabulario cerrado de la degradación")
	// ErrViaDesconocida — la vía no es local ni api.
	ErrViaDesconocida = errors.New("degradation: vía fuera del vocabulario cerrado (local|api)")
	// ErrTenantVacio — no hay a quién avisar. Un aviso sin dueño no es un aviso.
	ErrTenantVacio = errors.New("degradation: tenant vacío: un aviso sin dueño no es un aviso")
)

// Notice es UN aviso de degradación: el par (motivo, vía) que falló, la ventana
// en la que cayó, cuántos fallos colapsó y si el dueño ya lo leyó.
//
// 🔴 INV-6 — LO QUE NO ESTÁ EN ESTE STRUCT ES LO QUE LO HACE CORRECTO. No hay
// `Message`, no hay `Detail`, no hay `Error`, no hay teléfono, no hay JID, no hay
// id de sesión. No es que no se serialicen: es que no EXISTEN, así que un
// productor futuro no tiene dónde meter el texto del cliente ni el mensaje de
// error del proveedor —que también puede llevar contenido reflejado—.
//
// ⚠️ `SessionID` se consideró y se dejó fuera: sería cómodo para operación pero
// es un puntero a un número de WhatsApp, y la degradación es un hecho DEL TENANT,
// no de una conversación.
type Notice struct {
	// ID lo pone la base (UUID). Vacío al escribir; relleno al leer.
	ID string
	// TenantID es de quién es el aviso. Sale del token o de la sesión que falló,
	// jamás del cuerpo de una petición (INV-7 / INV-8).
	TenantID string
	// Reason es POR QUÉ. Vocabulario cerrado de ocho.
	Reason Reason
	// Via es QUÉ vía falló. Vocabulario cerrado de dos.
	Via string
	// WindowStart / WindowEnd son la ventana YA CALCULADA. Las pone Notifier a
	// partir del instante del fallo; quien llame al store directamente (la suite
	// de contrato) las pone a mano.
	WindowStart time.Time
	WindowEnd   time.Time
	// Occurrences es cuántos fallos colapsó este aviso. Nace en 1 y lo sube el
	// store al colapsar. Al escribir se ignora: lo decide la base.
	Occurrences int
	// ReadAt es cuándo lo leyó el dueño. CERO = sin leer, que es el estado de
	// TODA fila hoy (no hay endpoint de marcar-como-leída; lo pide el Plan
	// 045/047). Al escribir se ignora.
	ReadAt time.Time
	// CreatedAt es el nacimiento del aviso (el primer fallo de la ventana). Al
	// escribir se ignora: es el LastSeenAt del Save que lo creó.
	CreatedAt time.Time
	// LastSeenAt es el último fallo que cayó dentro. Junto con CreatedAt dice
	// cuánto lleva durando la degradación.
	LastSeenAt time.Time
}

// Leida dice si el dueño ya vio el aviso: true si y solo si ReadAt no es el
// instante cero. Existe para que quien proyecte al wire no tenga que saber que
// «cero significa sin leer» — esa traducción vive en un solo sitio, aquí.
func (n Notice) Leida() bool { panic(pendiente.Implementar("degradation.Notice.Leida")) }

// ListFilter acota la lectura. El tenant NO está aquí a propósito: va como
// argumento aparte de List, para que no exista la forma de construir un filtro
// que pida los avisos de otro (INV-7).
type ListFilter struct {
	// SoloSinLeer devuelve únicamente los avisos con ReadAt cero. Es la pregunta
	// que hace el teléfono («¿tengo algo pendiente?») y por eso tiene índice
	// parcial propio en la 0075.
	SoloSinLeer bool
	// Limit es el tamaño de página. <= 0 ⇒ el store aplica su default (50); por
	// encima del tope (200), el store recorta en silencio.
	Limit int
	// Offset es el desplazamiento. < 0 se trata como 0.
	Offset int
}

// Store es el puerto de persistencia de los avisos. Lo satisface *Postgres y, en
// los tests, degradationhelpertest.Memoria; las dos corren la misma suite,
// degradationhelpertest.Contrato.
//
// TODAS las operaciones van acotadas al tenant (INV-7 / INV-8): el tenant es un
// ARGUMENTO, no un campo del filtro, así que no hay forma de pedirle los avisos
// de otro.
type Store interface {
	// Save escribe el aviso APLICANDO EL DEDUPE: si ya existe una fila con la
	// misma (tenant, motivo, vía, inicio-de-ventana), NO crea una segunda —sube
	// su contador en uno y adelanta su LastSeenAt, que nunca retrocede—. Del
	// aviso que ya estaba no toca nada más: ni su fin de ventana, ni su
	// nacimiento, ni su lectura.
	//
	// creado true ⇒ nació un aviso nuevo (era el PRIMER fallo de esa ventana):
	// Occurrences 1, sin leer, y CreatedAt igual a su LastSeenAt. creado false ⇒
	// se colapsó sobre uno que ya estaba. El booleano existe para el consumidor
	// de mañana: el Plan 045/047 empuja al teléfono SOLO cuando nace, no cada vez
	// que sube el contador.
	//
	// De n se escriben el tenant, el motivo, la vía, la ventana y LastSeenAt (un
	// LastSeenAt cero se toma como el fin de la ventana); ID, Occurrences, ReadAt
	// y CreatedAt se ignoran.
	//
	// 🔴 NO VALIDA EL VOCABULARIO. Es el puerto de persistencia y su trabajo es
	// persistir; quien custodia el enum cerrado es Notifier, ANTES de llegar aquí
	// (y el CHECK de la 0075 detrás, como red). Ver Notifier.
	Save(ctx context.Context, n Notice) (creado bool, err error)

	// List devuelve los avisos del tenant —y de nadie más—, el más reciente
	// primero: por inicio de ventana descendente y, dentro de la misma ventana,
	// por nacimiento descendente. Aplica f (solo sin leer, página). Nunca
	// devuelve nil sin error: sin avisos es una lista vacía, para que quien la
	// serialice produzca `[]` y no `null`.
	List(ctx context.Context, tenantID string, f ListFilter) ([]Notice, error)
}

// Notifier es EL ESCRITOR, y es la pieza que hace cumplir REQ-38.
//
// Es un tipo aparte del Store —y no dos métodos del mismo objeto— porque las dos
// responsabilidades se rompen de formas distintas: el Store falla cuando la base
// no está; el Notifier falla cuando el LLAMANTE se equivoca de motivo. Separarlos
// permite que el test del «motivo sano ⇒ cero filas» use un Store que cuenta
// llamadas y demuestre que la fila no llegó ni a intentarse, que es una
// afirmación más fuerte que «la base la rechazó».
//
// # La ventana
//
// El instante del fallo se TRUNCA a un múltiplo de Ventana, y ese truncado es
// parte de la clave del dedupe. Es una FUNCIÓN PURA del instante: dos réplicas
// del servidor calculan el mismo `window_start` sin hablar entre ellas, y el
// índice único de la 0075 colapsa la segunda escritura. Un dedupe basado en «¿hay
// algo de los últimos N minutos?» no se puede indexar y se pierde en cuanto haya
// dos procesos.
//
// ⚠️ Cambiar Ventana NO corrompe nada ni exige migración: re-agrupa a partir de
// ese momento, y las filas viejas siguen diciendo la verdad sobre el intervalo
// que las produjo, porque la fila guarda el intervalo y no la política.
//
// El store sobre el que escribe no es un campo exportado: solo lo pone
// NewNotifier. Un Notifier escrito con literal de struct (`&Notifier{}`) no tiene
// store, y Record se lo dice al llamante con un error en vez de reventar.
type Notifier struct {
	// Ventana es el tamaño del bucket. <= 0 ⇒ VentanaPorDefecto, y eso se
	// resuelve EN CADA USO, no en el constructor (T-13): un Notifier al que se
	// le deja o se le pone la ventana a cero se comporta igual que uno
	// construido con el default. Si el default viviera solo en NewNotifier, ese
	// Notifier truncaría con duración cero y time.Truncate devolvería el
	// instante intacto, o sea: un aviso por fallo y REQ-38 roto sin que nada
	// fallara.
	Ventana time.Duration
	// Ahora es el reloj de RecordAhora, inyectable para los tests. nil ⇒
	// time.Now, resuelto también en el uso.
	Ahora func() time.Time
}

// NewNotifier construye el escritor sobre un Store, con esa ventana (que queda
// en el campo Ventana tal cual llega: ventana <= 0 cae a VentanaPorDefecto al
// usarse) y sin reloj inyectado. No llama al store.
func NewNotifier(store Store, ventana time.Duration) *Notifier {
	panic(pendiente.Implementar("degradation.NewNotifier"))
}

// VentanaDe devuelve el bucket en el que cae at para una ventana de tamaño v:
// inicio = at.UTC().Truncate(v) y fin = inicio + v. v <= 0 ⇒ VentanaPorDefecto.
// Los dos instantes vuelven en UTC.
//
// Se exporta —y es una función libre, no un método— porque es la pieza que los
// tests tienen que poder ejercitar sin montar un Notifier, y porque un productor
// puede necesitar saber en qué ventana caería un fallo sin escribirlo.
//
// Es una FUNCIÓN PURA: el mismo instante da la misma ventana en dos procesos y
// en dos zonas horarias. El borde es del bucket siguiente: at == fin de una
// ventana abre la otra (el precio aceptado de la ventana fija).
//
// 🔴 `.UTC()` ANTES DE `Truncate` NO ES COSMÉTICO. time.Truncate opera sobre el
// tiempo absoluto desde el año cero, así que el INSTANTE resultante es el mismo
// venga el argumento en la zona que venga; lo que cambia es la zona con la que se
// imprime y con la que el driver lo manda a Postgres. Fijarlo en UTC hace que la
// clave del dedupe se escriba siempre igual, y que dos procesos con TZ distinta
// no partan la ventana en dos. Truncate además descarta el reloj monótono, que es
// lo que hace falta para que el valor sea comparable entre procesos.
func VentanaDe(at time.Time, v time.Duration) (inicio, fin time.Time) {
	panic(pendiente.Implementar("degradation.VentanaDe"))
}

// Record registra que la vía `via` falló por el motivo `reason` para `tenantID`,
// EN EL INSTANTE `at`: llama UNA vez a Store.Save con el tenant, el motivo, la
// vía, la ventana de VentanaDe(at, Ventana) y LastSeenAt = at.UTC() —y nada
// más—, con el ctx recibido, y devuelve lo que Save devuelva, error incluido,
// sin envolverlo.
//
// creado true ⇒ nació un aviso nuevo. creado false ⇒ se colapsó sobre el aviso que
// ya cubría esa ventana. N fallos dentro de la misma ventana ⇒ UNA fila.
//
// 🔴 EL FILTRO ES EL VOCABULARIO, Y ESTA ES LA GUARDA. Esta función puede ser
// llamada desde cualquier sitio, y el registro automático solo vale lo que valga
// el estado que AFIRMA: si aquí entrara un motivo SANO —el cliente respondió «3»,
// el fastlane resolvió el turno, no había texto que analizar—, la tabla dejaría
// de significar «el LLM se cayó» y el dueño dejaría de leerla. Se elige el ERROR
// y no el silencio a propósito — tragárselo dejaría al productor equivocado
// creyendo que avisó.
//
// Rechaza SIN LLAMAR AL STORE, con (false, error), en este orden:
//
//  1. tenantID == "" ⇒ ErrTenantVacio, sin envolver.
//  2. motivo fuera del vocabulario (Valid) ⇒ envuelve ErrMotivoDesconocido y lo
//     NOMBRA: `<centinela>: "<motivo>" (los válidos son [<los ocho, separados
//     por espacios, en el orden de Reasons>])`, con el motivo en %q.
//  3. vía fuera del vocabulario (ValidVia) ⇒ envuelve ErrViaDesconocida:
//     `<centinela>: "<vía>"`, con la vía en %q.
//  4. Notifier sin store ⇒ el error de texto exacto
//     "degradation: Notifier sin store: el aviso no se puede escribir". Es una
//     guarda de programación y NO un panic: descubrirlo con un panic en el
//     camino de FALLO del pipeline —el único desde el que se llama— convertiría
//     una degradación en una caída.
//
// ⚠️ El instante viene como ARGUMENTO y no se toma del reloj interno: el fallo
// ocurrió cuando ocurrió, y un productor que lo registre con retraso —un job que
// reintenta, un frame que llega tarde— tiene que poder poner el instante real, o
// dos fallos de la misma ventana caerían en buckets distintos y REQ-38 se rompería
// por el camino largo. RecordAhora existe para el caso trivial.
func (n *Notifier) Record(ctx context.Context, tenantID string, reason Reason, via string, at time.Time) (bool, error) {
	panic(pendiente.Implementar("degradation.Notifier.Record"))
}

// RecordAhora es Record con el instante del reloj: el de Ahora si está puesto,
// el de time.Now si no. Es el caso normal del productor que registra el fallo en
// cuanto lo ve.
func (n *Notifier) RecordAhora(ctx context.Context, tenantID string, reason Reason, via string) (bool, error) {
	panic(pendiente.Implementar("degradation.Notifier.RecordAhora"))
}
