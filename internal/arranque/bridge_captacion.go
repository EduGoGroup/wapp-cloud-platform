// Adaptador de tipos entre la captación NUEVA (internal/modulos/captacion/{intake,intakeahead,
// reanalisis}) y los dos consumidores VIEJOS que siguen pidiendo tipos del intake viejo: el
// agregador de ventanas y el compositor del literal, que viven en internal/flujos/runtime y no son
// de F7 (reglas de F7, T-2). No porta ningún fichero: nace en F7 (T7.23, arquitectura §4 del plan
// de F7) para que el arranque nuevo cablee UN solo pool, UN solo worker y UN solo servicio de
// re-análisis, los nuevos, sin tocar a esos dos consumidores (E-1).
//
// 🔴 ES EL ÚNICO FICHERO DE PRODUCCIÓN DE ESTE DIRECTORIO QUE IMPORTA LA CAPTACIÓN VIEJA
// (internal/intake, con el alias intakeviejo). Ninguna fase la nombra: la segunda instancia vieja
// de la cola se construye aquí (newLegacyIntakeJobs) y el contenedor la guarda por el alias
// legacyIntakeJobs. Lo vigila TestCableado_OnlyTheBridgeImportsTheOldCapture.
//
// Vida: nace en F7 · MUERE EN F8, cuando el agregador y el compositor nuevos (conversacion) pidan
// los tipos de la captación nueva; con él entra `captacion` en Conmutados.

package arranque

import (
	"context"
	"database/sql"

	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	intakeviejo "github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intakeahead"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/reanalisis"
)

// legacyThreadLimit es el límite del hilo que recibe reanalisis.NewService en su octavo parámetro:
// cuántas entradas del hilo del evento lee el re-análisis para recomponer el literal.
//
// 🔴 DERIVADO, NO COPIADO. Es flowruntime.DefaultThreadLimit, la MISMA constante con la que el
// compositor viejo lee el hilo al cerrar una ventana. El servicio nuevo no la importa (sería un
// tercer puente al runtime viejo) y no trae valor por defecto: la recibe, y el arranque se la lee
// al runtime. Un literal suelto aquí serían dos verdades, y el día que se separaran el re-análisis
// recompondría un literal distinto del que compuso el flush sin un solo error. Lo fija
// TestCaptureBridge_ThreadLimitIsTheLegacyComposerLimit.
const legacyThreadLimit = flowruntime.DefaultThreadLimit

// legacyIntakeJobs es la cola VIEJA del pipeline de captación (internal/intake.Postgres), con un
// nombre de este paquete para que el contenedor y las fases la guarden y la pasen sin importar el
// paquete viejo. Es un ALIAS, no un tipo nuevo: *legacyIntakeJobs ES *intakeviejo.Postgres y
// satisface tal cual los puertos del agregador (intake.JobStore viejo) y del compositor
// (flowruntime.SourceTextWriter).
type legacyIntakeJobs = intakeviejo.Postgres

// newLegacyIntakeJobs construye la SEGUNDA instancia, VIEJA, de la cola (D-F7-1): la que reciben
// el agregador (fase 7) y el compositor (fase 5), y nadie más. La primera, la nueva, es
// c.intakeJobStore y la leen el worker, las etapas y el re-análisis.
//
// No tiene estado: guarda db y nada más, igual que la nueva. Dos instancias sobre el MISMO
// *sql.DB no son dos pools ni pueden divergir; lo que sí serían dos verdades es un segundo
// compositor o un segundo pool de clasificaciones, y de esos sigue habiendo uno.
func newLegacyIntakeJobs(db *sql.DB) *legacyIntakeJobs {
	return intakeviejo.NewPostgres(db)
}

// toNewWindowKey convierte la clave de ventana del intake viejo en la del nuevo, y toLegacyWindowKey
// hace el camino contrario. Los dos WindowKey tienen los MISMOS cuatro campos (TenantID, SessionID,
// ContactID, EventID) en el mismo orden, y por eso Go permite la conversión de tipo directa; el
// compilador, en cambio, no los mezcla solo (T-3). Son funciones aparte para que el test afirme la
// ida y la vuelta campo a campo: el día que uno de los dos tipos gane un campo, la conversión deja
// de compilar aquí, que es el único sitio donde existe.
func toNewWindowKey(k intakeviejo.WindowKey) intake.WindowKey { return intake.WindowKey(k) }

// toLegacyWindowKey: ver toNewWindowKey.
func toLegacyWindowKey(k intake.WindowKey) intakeviejo.WindowKey { return intakeviejo.WindowKey(k) }

// aheadRequests es lo único que aheadBridge necesita del pool nuevo: encolar una petición de
// clasificación. Lo satisface *intakeahead.Pool, que es lo que cablea el arranque (lo afirma el
// test de cableado, por identidad); se guarda como puerto y no como el tipo concreto para que el
// test unitario lo ejercite con un doble, sin selector ni goroutines.
type aheadRequests interface {
	Request(key intake.WindowKey, text string)
}

// aheadBridge presenta el *intakeahead.Pool NUEVO como el flowruntime.AheadRequester que pide el
// agregador viejo (flowruntime.WithAheadRequester, fase 7). Hace falta porque AheadRequester nombra
// el WindowKey del intake VIEJO: un *intakeahead.Pool nuevo no lo satisface.
//
// Delega en pool, que es EL MISMO pool que arranca la fase de fondo y que recibe gw.OnWarmup: la
// alternativa (un pool viejo aparte, solo para el agregador) serían dos colas de clasificación y
// dos juegos de workers contra la única plaza del Edge.
//
// Promesas:
//   - Request convierte la clave vieja en la nueva campo a campo y delega en pool.Request con esa
//     clave y el MISMO text, sin mirarlos. No valida la clave: el pool ya descarta la incompleta.
//   - No devuelve nada y no bloquea más de lo que bloquee el pool: corre en línea con el mensaje
//     (INV-10), igual que cuando el agregador llamaba al pool viejo directamente.
type aheadBridge struct {
	// pool es el pool nuevo en el que se delega; el adaptador no guarda más estado.
	pool aheadRequests
}

// aheadBridge es un flowruntime.AheadRequester y *intakeahead.Pool es lo que envuelve: si alguna
// de las dos firmas cambia, esto no compila.
var (
	_ flowruntime.AheadRequester = (*aheadBridge)(nil)
	_ aheadRequests              = (*intakeahead.Pool)(nil)
)

// Request implementa flowruntime.AheadRequester: convierte la clave y delega (ver aheadBridge).
func (b *aheadBridge) Request(key intakeviejo.WindowKey, text string) {
	b.pool.Request(toNewWindowKey(key), text)
}

// legacyFlushComposer es lo único que composerBridge necesita del compositor viejo: componer el
// literal de una ventana al cerrarla. Lo satisface *flowruntime.SourceTextComposer, que es lo que
// cablea el arranque; se guarda como puerto por el mismo motivo que aheadRequests.
type legacyFlushComposer interface {
	ComposeAtFlush(ctx context.Context, key intakeviejo.WindowKey) error
}

// composerBridge presenta el *flowruntime.SourceTextComposer VIEJO como el reanalisis.Composer que
// pide el servicio nuevo del re-análisis (reanalisis.NewService, fase 5). Hace falta porque
// Composer nombra el WindowKey del intake NUEVO y el compositor, que es de flujos/runtime (F8),
// el del viejo.
//
// 🔴 Envuelve EL MISMO compositor que recibe el agregador (c.intakeComposer), no uno construido
// para el re-análisis: dos compositores serían dos `source_text` que divergen en el primer rótulo
// que cambie (T-5). El adaptador no construye nada; lo que envuelve se lo dan. Lo fija
// TestCableado_TheCaptureBridgesWrapTheBootObjects.
//
// Promesas:
//   - ComposeAtFlush convierte la clave nueva en la vieja campo a campo y delega en
//     composer.ComposeAtFlush con el MISMO ctx y esa clave.
//   - Devuelve lo que devuelva el compositor TAL CUAL, nil incluido: el error no se envuelve ni se
//     traduce. El re-análisis no compara ningún centinela sobre él (lo envuelve con su propio
//     texto), así que no hay nada que traducir.
type composerBridge struct {
	// composer es el compositor viejo en el que se delega; el adaptador no guarda más estado.
	composer legacyFlushComposer
}

// composerBridge es un reanalisis.Composer y *flowruntime.SourceTextComposer es lo que envuelve:
// si alguna de las dos firmas cambia, esto no compila.
var (
	_ reanalisis.Composer = (*composerBridge)(nil)
	_ legacyFlushComposer = (*flowruntime.SourceTextComposer)(nil)
)

// ComposeAtFlush implementa reanalisis.Composer: convierte la clave y delega (ver composerBridge).
func (b *composerBridge) ComposeAtFlush(ctx context.Context, key intake.WindowKey) error {
	return b.composer.ComposeAtFlush(ctx, toLegacyWindowKey(key))
}

// legacyClassifiedSink es lo único que la clausura del sink necesita del agregador viejo: recibir
// una clasificación. Lo satisface *flowruntime.IntakeAggregator.
type legacyClassifiedSink interface {
	OnClassified(key intakeviejo.WindowKey, intent string, confidence float64)
}

// *flowruntime.IntakeAggregator es lo que resuelve classifiedSink: si su firma cambia, esto no
// compila.
var _ legacyClassifiedSink = (*flowruntime.IntakeAggregator)(nil)

// classifiedSink devuelve el intakeahead.Sink que recibe el pool nuevo (intakeahead.New, fase 7):
// la respuesta de una clasificación vuelve por él al agregador VIEJO.
//
// 🔴 CLAUSURA DIFERIDA, Y ES LO QUE CORTA EL NUDO DE CONSTRUCCIÓN: el pool se construye ANTES que
// el agregador (el agregador pide por el pool y el pool responde al agregador), así que cuando
// esta función corre c.intakeAggregator es nil. El campo se lee AL LLAMAR, no al construir: es el
// mismo patrón, con el mismo porqué, que tenía la clausura escrita en línea en fase7_flujos.go
// hasta F7. Cuando llegue la primera clasificación el proceso lleva rato arrancado.
//
// No comprueba nil a propósito: la clausura de antes tampoco, y OnClassified del agregador se
// llamaba sobre el puntero tal cual. Una guarda aquí cambiaría un fallo ruidoso de arranque mal
// ordenado por una clasificación tragada en silencio.
func classifiedSink(c *contenedor) intakeahead.SinkFunc {
	return newClassifiedSink(func() legacyClassifiedSink { return c.intakeAggregator })
}

// newClassifiedSink es el interior de classifiedSink, separado para afirmar sus promesas sin un
// agregador real: recibe CÓMO se obtiene el destino, no el destino.
//
// Promesas:
//   - No llama a target al construirse: solo dentro de cada clasificación, y una vez por cada una
//     (así un destino que todavía no existe al construir el pool se resuelve cuando ya existe).
//   - Cada clasificación convierte la clave nueva en la vieja campo a campo y llama a
//     OnClassified del destino con esa clave y los MISMOS intent y confidence, sin mirarlos.
func newClassifiedSink(target func() legacyClassifiedSink) intakeahead.SinkFunc {
	return func(key intake.WindowKey, intent string, confidence float64) {
		target().OnClassified(toLegacyWindowKey(key), intent, confidence)
	}
}
