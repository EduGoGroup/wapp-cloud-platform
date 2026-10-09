// Porta internal/intake/pipeline/plaza.go @ 56097aa

package pipeline

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// slot.go — EL ENTERO (Plan 044 · Ola 2 · T2.7, ADR-0046 · Mecanismo 1; R-01).
//
// # QUÉ ES ESTO, EN UNA FRASE
//
// Una sola cadena de lote en vuelo A LA VEZ POR EDGE. No una cola con prioridades, no un
// planificador: **un entero por plaza**. El ADR-0046 descarta el planificador por escrito,
// y la primera razón es que FIFO ya acota la espera y sale gratis. Es la doctrina del
// ADR-0003: cuando un entero resuelve el problema, no se mete infraestructura.
//
// # POR QUÉ POR EDGE Y NO POR PROCESO
//
// La plaza es la máquina: UN Ollama por Edge. Un `K = 1` global en el proceso del Cloud
// serializaría los presupuestos de TODOS los clientes detrás del más lento —el tenant A
// con un pedido de 10 ítems dejaría al tenant B, con su propio Edge ocioso, esperando sin
// motivo (D-044.42)—. Por eso la plaza tiene dirección, y la dirección es `(tenant, Edge)`.
//
// # POR QUÉ 1 Y NO OTRO NÚMERO
//
// Con N cadenas de lote sueltas sobre la misma plaza, un turno interactivo puede quedar
// detrás de **N** llamadas de lote; con 1, su espera queda acotada a **una sola** — que
// son 22–32 s medidos, no «segundos». Ese acotamiento es TODO lo que compra el entero.
//
// 🔴 Y NO LO ARREGLA DEL TODO: una llamada de lote NO CABE en el turno del Nivel B. El
// entero lo baja de N×32 s a 32 s; el desalojo —que el interactivo cancele la llamada de
// lote en curso— NO se construye hasta tener un dato de campo.
//
// # LA SEDE ES EL CLOUD
//
// Aquí NO se construye un segundo aforo en el Edge: el Edge ya tiene el suyo y es control
// de ADMISIÓN —cuántas peticiones atiende a la vez—, no un repartidor de quién puede
// EMPEZAR una cadena.
//
// # LO QUE ESTE AFORO NO ES
//
// ⚠️ NO ES DISTRIBUIDO. Es un mapa en memoria de ESTE proceso. Con dos réplicas del Cloud
// hay dos aforos y por tanto K = 2 por Edge. Es una decisión, no un descuido: el reparto
// entre réplicas exigiría un lease en Postgres con su renovación y su barrendero, y hoy el
// Cloud corre en UNA réplica. Por lo mismo, en el proceso hay UN solo Capacity: una
// segunda instancia serían dos enteros para la misma plaza.

// KPerSlot (antes `KPorPlaza`) es EL ENTERO del Mecanismo 1: cuántas cadenas de lote
// pueden estar en vuelo a la vez SOBRE LA MISMA PLAZA. Vale 1.
const KPerSlot = 1

// Slot (antes `Plaza`) es la dirección del recurso escaso: un tenant y uno de sus Edges.
//
// Las dos mitades hacen falta. Sin `EdgeID` el entero sería por tenant y un cliente con
// dos instalaciones perdería la mitad de su capacidad; sin `TenantID` dos Edges de tenants
// distintos podrían colisionar el día que los `edge_id` dejen de ser únicos globalmente —
// y ese día llegaría sin un solo error.
type Slot struct {
	TenantID string
	EdgeID   string
}

// Valid (antes `Valida`) dice si la dirección identifica una plaza: las DOS mitades no
// vacías. Una plaza a medias no se toma: se sigue sin aforo, que es lo que hace el worker
// cuando el tenant no tiene Edge.
func (s Slot) Valid() bool {
	panic(pendiente.Implementar("pipeline.Slot.Valid"))
}

// String es para el log: `<tenant_id>/<edge_id>`. No lleva PII: `edge_id` es el CN del
// certificado y `tenant_id` un UUID.
func (s Slot) String() string {
	panic(pendiente.Implementar("pipeline.Slot.String"))
}

// Slots (antes `Plazas`) responde a la única pregunta que el worker necesita hacerle al
// resto del sistema: **qué plaza ocupa una inferencia de este (tenant, sesión), si ocupa
// alguna**. Lo satisface `*llmvia.Selector` de forma estructural (por eso el método
// conserva su nombre, `PlazaDe`): este paquete no importa `llmvia`.
//
// 🔴 EL WORKER NO PREGUNTA POR LA VÍA, Y ESTE PUERTO ES LA RAZÓN. Que un tenant en vía API
// no ocupe plaza —allí el tope es de precio, no de capacidad— es una decisión que vive
// donde vive el switch por vía, y desde aquí se ve como un `ok = false` indistinguible de
// «este tenant no tiene ningún Edge conectado». Las dos cosas significan lo mismo para el
// worker: no hay plaza que tomar, adelante.
type Slots interface {
	PlazaDe(ctx context.Context, tenantID, originSessionID string) (edgeID string, ok bool, err error)
}

// Capacity (antes `Aforo`) reparte `k` plazas por dirección. Es seguro para uso
// concurrente y es lo único compartido entre los workers del pipeline.
//
// # EL SEGUNDO ESPERA — NO FALLA, NO SE REENCOLA CASTIGADO, NO SE DEGRADA
//
// Tomar plaza es un ENVÍO a un canal con buffer `k`; soltarla, una recepción. El segundo
// job del mismo Edge simplemente espera su turno.
//
// ⚠️ El despertar FIFO de los bloqueados en un canal NO ES UNA GARANTÍA DEL SPEC DE GO —es
// una propiedad de la implementación `gc`—. No se depende de ella para NINGUNA invariante:
// lo que este tipo promete es el AFORO (cuántos a la vez), no el orden.
//
// ⚠️ ESPERAR TIENE DOS PRECIOS, Y QUIEN CABLEE ESTO TIENE QUE CONOCERLOS:
//
//  1. **Bloqueo en cabeza.** El worker que espera ya tiene UN job reclamado y no atiende
//     ningún otro mientras espera. No es un interbloqueo (el que tiene la plaza avanza),
//     es hambre acotada.
//  2. **La ventana del huérfano se alarga.** Un SIGKILL mientras se espera deja el job en
//     `processing` sin rescate —`intake_jobs` no tiene `claimed_at`—, y ahora esa ventana
//     incluye la espera.
//
// # POR QUÉ NO HAY UN «INTENTAR TOMAR»
//
// Porque la alternativa a esperar sería soltar el job con `Release` y volver a reclamarlo.
// `Release` NO CASTIGA, así que el drenaje —que no tiene techo de vueltas— giraría
// reclamando y soltando el mismo job a la velocidad del error: la tormenta que la
// migración 0078 existe para impedir. Esperar bloquea UN worker; el intentar-y-soltar
// quemaría el proceso.
type Capacity struct{}

// NewCapacity (antes `NuevoAforo`) construye el aforo con `k` plazas por dirección. Un
// `k <= 0` cae a KPerSlot: un aforo de cero no dejaría pasar a NADIE y el pipeline entero
// se quedaría colgado sin un solo error.
func NewCapacity(k int) *Capacity {
	panic(pendiente.Implementar("pipeline.NewCapacity"))
}

// Acquire (antes `Tomar`) ocupa la plaza `s` y devuelve la función que la suelta. Si hay
// sitio vuelve EN EL ACTO —también con un `ctx` ya cancelado: el intento sin bloqueo va
// primero— y si no, bloquea hasta que otro la suelte.
//
// Devuelve el error de `ctx` si el llamante se rinde antes de conseguirla —y solo
// entonces—; en ese caso NO hay nada que soltar y la función devuelta es nil. Que el ctx
// corte aquí es el apagado ordenado del worker, no un fallo del job.
//
// Lo que promete, además:
//
//   - en ningún instante hay más de `k` plazas tomadas sobre la MISMA dirección;
//   - direcciones distintas no se estorban: cada una tiene sus `k`;
//   - la función de soltar es IDEMPOTENTE: una segunda llamada no libera nada. Una
//     liberación de más le quitaría el sitio a otro y dejaría DOS cadenas sobre la misma
//     plaza, sin dar un solo error;
//   - la dirección no se valida aquí (lo hace el worker con Slot.Valid).
func (c *Capacity) Acquire(ctx context.Context, s Slot) (func(), error) {
	panic(pendiente.Implementar("pipeline.Capacity.Acquire"))
}

// Waiting (antes `Esperando`) son las cadenas BLOQUEADAS ahora mismo pidiendo plaza,
// sumadas todas las direcciones. La que tomó la plaza sin esperar no cuenta, ni mientras
// la toma ni después; la que se rinde por su `ctx` deja de contar.
func (c *Capacity) Waiting() int {
	panic(pendiente.Implementar("pipeline.Capacity.Waiting"))
}
