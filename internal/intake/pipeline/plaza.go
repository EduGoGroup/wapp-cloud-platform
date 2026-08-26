package pipeline

import (
	"context"
	"fmt"
	"sync"
)

// plaza.go — EL ENTERO (Plan 044 · Ola 2 · T2.7, ADR-0046 · Mecanismo 1).
//
// # QUÉ ES ESTO, EN UNA FRASE
//
// Una sola cadena de lote en vuelo A LA VEZ POR EDGE. No una cola con prioridades,
// no un planificador: **un entero por plaza**. El ADR-0046 §Alternativas descarta el
// planificador por escrito y con tres razones, y la primera es que FIFO ya acota la
// espera y sale gratis. Es la misma doctrina del ADR-0003: cuando un entero resuelve
// el problema, no se mete infraestructura.
//
// # POR QUÉ POR EDGE Y NO POR PROCESO
//
// La plaza es la máquina: UN Ollama por Edge. Un `K = 1` global en el proceso del
// Cloud serializaría los presupuestos de TODOS los clientes detrás del más lento —el
// tenant A con un pedido de 10 ítems (5–6 min) dejaría al tenant B, con su propio
// Edge ocioso, esperando sin motivo (D7-b, D-044.42). Por eso la plaza tiene
// dirección, y la dirección es `(tenant, Edge)`.
//
// # POR QUÉ 1 Y NO OTRO NÚMERO — LO ÚNICO QUE HAY QUE ENTENDER AQUÍ
//
// Con N cadenas de lote sueltas sobre la misma plaza, un turno interactivo puede
// quedar detrás de **N** llamadas de lote; con 1, su espera queda acotada a **una
// sola** — que son 22–32 s medidos (§Corrección 2.4 del ADR-0046), no «segundos».
// Ese acotamiento es TODO lo que compra el entero, y es exactamente lo que hay que
// medir: no el throughput agregado.
//
// 🔴 Y NO LO ARREGLA DEL TODO, QUE ES LA CONSECUENCIA HONESTA (D2). Una llamada de
// lote NO CABE en el turno del Nivel B: el interactivo espera hasta 32 s, su
// presupuesto (`intakeahead.DefaultTimeout`, 45 s) se lo come el descuento de cola y
// cae al Nivel A con aviso (ADR-0044 §5). El entero lo baja de N×32 s a 32 s; el
// desalojo —que el interactivo cancele la llamada de lote en curso— NO se construye
// hasta que exista la Ola 3.5 y un dato de campo.
//
// # LA SEDE ES EL CLOUD
//
// (ADR-0038 Enmienda 2.) Aquí NO se construye un segundo aforo en el Edge: el Edge
// ya tiene el suyo (`DefaultMaxConcurrent = 1`, ADR-0038 Enmienda 1 §d) y es control
// de ADMISIÓN —cuántas peticiones atiende a la vez—, no un repartidor de quién puede
// EMPEZAR una cadena. Dos aforos con el mismo nombre y distinta sede es la clase de
// duplicación que luego nadie sabe cuál manda.
//
// # LO QUE ESTE AFORO NO ES, Y NO PRETENDE SER
//
// ⚠️ NO ES DISTRIBUIDO. Es un mapa en memoria de ESTE proceso. Con dos réplicas del
// Cloud hay dos aforos y por tanto K = 2 por Edge. Eso es una decisión, no un
// descuido: el reparto entre réplicas exigiría un lease en Postgres con su renovación
// y su barrendero (la infraestructura que el ADR-0003 y el propio ADR-0046 rechazan
// para un problema de un entero), y hoy el Cloud corre en UNA réplica. El día que
// haya dos, la señal es medible sin desplegar nada: el mismo test de solapamiento
// del criterio (a), corrido contra la flota real, deja de valer.

// KPorPlaza es EL ENTERO del Mecanismo 1: cuántas cadenas de lote pueden estar en
// vuelo a la vez SOBRE LA MISMA PLAZA.
//
// 🔬 Es la constante que custodia el test de solapamiento: subirla a 2 pone rojo el
// criterio (a) de T2.7 (dos jobs del mismo Edge con una llamada en vuelo a la vez).
const KPorPlaza = 1

// Plaza es la dirección del recurso escaso: un tenant y uno de sus Edges.
//
// Las dos mitades hacen falta y ninguna sobra. Sin `EdgeID` el entero sería por
// tenant y un cliente con dos instalaciones perdería la mitad de su capacidad; sin
// `TenantID` dos Edges de tenants distintos podrían colisionar el día que los
// `edge_id` dejen de ser únicos globalmente — y ese día llegaría sin un solo error,
// que es como llegan los peores.
type Plaza struct {
	TenantID string
	EdgeID   string
}

// Valida dice si la dirección identifica una plaza. Una plaza a medias no se toma:
// se sigue sin aforo, que es lo que hace el worker cuando el tenant no tiene Edge.
func (p Plaza) Valida() bool { return p.TenantID != "" && p.EdgeID != "" }

// String es para el log. No lleva PII: `edge_id` es el CN del certificado y
// `tenant_id` un UUID.
func (p Plaza) String() string { return fmt.Sprintf("%s/%s", p.TenantID, p.EdgeID) }

// Plazas responde a la única pregunta que el worker necesita hacerle al resto del
// sistema: **qué plaza ocupa una inferencia de este (tenant, sesión), si ocupa
// alguna**. Lo satisface *llmvia.Selector.
//
// 🔴 EL WORKER NO PREGUNTA POR LA VÍA, Y ESTE PUERTO ES LA RAZÓN DE QUE NO TENGA QUE
// HACERLO. Que un tenant en vía API no ocupe plaza —allí el tope es de precio, no de
// capacidad— es una decisión que vive donde vive el switch por vía (llmvia), y desde
// aquí se ve como un `ok = false` indistinguible de «este tenant no tiene ningún
// Edge conectado». Las dos cosas significan lo mismo para el worker: no hay plaza que
// tomar, adelante.
type Plazas interface {
	PlazaDe(ctx context.Context, tenantID, originSessionID string) (edgeID string, ok bool, err error)
}

// Aforo reparte `k` plazas por dirección. Es seguro para uso concurrente y es lo
// único compartido entre los workers del pipeline.
//
// # EL SEGUNDO ESPERA — NO FALLA, NO SE REENCOLA CASTIGADO, NO SE DEGRADA
//
// Tomar plaza es un ENVÍO a un canal con buffer `k`; soltarla, una recepción. Las
// goroutines bloqueadas enviando se encolan en el `sendq` del canal y se despiertan
// EN ORDEN DE LLEGADA, así que el segundo job del mismo Edge simplemente espera su
// turno.
//
// ⚠️ Y ESO HAY QUE ESCRIBIRLO COMO LO QUE ES: el despertar FIFO de los bloqueados en
// un canal NO ES UNA GARANTÍA DEL SPEC DE GO —es una propiedad de la implementación
// `gc` (las colas `sendq`/`recvq` de `hchan` son FIFO)— y el ADR-0046 lo dice con
// todas las letras. No se depende de ella para NINGUNA invariante de este paquete: lo
// que los tests fijan es el AFORO (cuántos a la vez), no el orden. Si algún día hace
// falta el orden, hace falta también otra estructura.
//
// ⚠️ ESPERAR TIENE DOS PRECIOS, Y QUIEN CABLEE ESTO TIENE QUE CONOCERLOS:
//
//  1. **Bloqueo en cabeza.** El worker que espera ya tiene UN job reclamado y no
//     atiende ningún otro mientras espera. Con W workers y un Edge muy cargado, hasta
//     W−1 pueden quedar parados detrás de él aunque haya trabajo de otros Edges. No es
//     un interbloqueo (el que tiene la plaza avanza), es hambre acotada, y la palanca
//     es W: con W > número de Edges activos no puede ocurrir.
//  2. **La ventana del huérfano se alarga.** Un SIGKILL mientras se espera deja el job
//     en `processing` sin rescate, igual que antes —`intake_jobs` no tiene el
//     `claimed_at` de `webhook_outbox`—, solo que ahora esa ventana incluye la espera.
//     Quien decida ese `claimed_at` (ver Worker.cierre) tiene que contar la espera, no
//     solo la cadena.
//
// # POR QUÉ NO HAY `TryTomar`
//
// Porque la alternativa a esperar sería soltar el job con `Release` y volver a
// reclamarlo. `Release` NO CASTIGA (no toca `next_attempt_at`), así que `Drenar`
// —que no tiene techo de vueltas— giraría reclamando y soltando el mismo job a la
// velocidad del error: exactamente la tormenta que la migración 0078 existe para
// impedir, y la que la mutación M1 de T2.5 dejó medida (cuelga el proceso hasta el
// `-timeout`). Esperar bloquea UN worker; el try-and-release quemaría el proceso.
type Aforo struct {
	k int

	mu     sync.Mutex
	plazas map[Plaza]chan struct{}
	// esperando son las cadenas BLOQUEADAS de verdad pidiendo plaza, sumadas todas las
	// direcciones. La que la coge sin esperar NO se cuenta (ver Tomar). Es un observable
	// de verdad —el candidato natural a métrica
	// «cadenas de lote esperando plaza»— y no un adorno de test: si crece y no baja,
	// lo que hay es un Edge atascado reteniendo su plaza, y eso no se ve en ningún
	// otro sitio.
	esperando int
}

// NuevoAforo construye el aforo con `k` plazas por dirección. Un `k <= 0` cae a
// KPorPlaza: un aforo de cero no dejaría pasar a NADIE y el pipeline entero se
// quedaría colgado sin un solo error.
func NuevoAforo(k int) *Aforo {
	if k <= 0 {
		k = KPorPlaza
	}
	return &Aforo{k: k, plazas: make(map[Plaza]chan struct{})}
}

// Tomar ocupa la plaza `p` y devuelve la función que la suelta. Bloquea mientras la
// plaza esté llena.
//
// Devuelve el error de `ctx` si el llamante se rinde antes de conseguirla —y solo
// entonces—; en ese caso NO hay nada que soltar y la función devuelta es nil. Que el
// ctx corte aquí es el apagado ordenado del worker, no un fallo del job: quien lo
// reciba tiene que devolver el job con `Release` (sin castigo), no con `Retry`.
//
// 🔴 EL CANAL SE CREA BAJO EL CANDADO Y EL ENVÍO SE HACE FUERA. Enviar con `mu`
// tomado convertiría el primer bloqueo en un interbloqueo de todo el aforo: el que
// tuviera la plaza no podría ni soltarla (Soltar necesita el mismo candado para
// encontrar el canal), y ninguna otra dirección podría siquiera crearse.
func (a *Aforo) Tomar(ctx context.Context, p Plaza) (func(), error) {
	ch := a.canal(p)

	// 🔴 EL INTENTO SIN BLOQUEO VA PRIMERO, Y NO ES UNA OPTIMIZACIÓN: es lo que hace
	// que `Esperando` signifique lo que dice. Contando ANTES del select, una cadena que
	// va a coger la plaza sin esperar ni un instante se apuntaba como «esperando»
	// durante ese instante — un contador que miente en el caso normal, y encima de
	// forma intermitente. Lo destapó `-race -count=40`: el test del criterio (b) daba
	// por «contadas» dos cadenas cuando una de ellas todavía no había entrado, y fallaba
	// ~1 de cada 40 veces. Que un observable mienta el 2 % del tiempo es exactamente lo
	// que no se ve hasta que decide algo.
	select {
	case ch <- struct{}{}:
		return soltarUnaVez(ch), nil
	default:
	}

	a.mu.Lock()
	a.esperando++
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.esperando--
		a.mu.Unlock()
	}()

	select {
	case ch <- struct{}{}:
		return soltarUnaVez(ch), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// soltarUnaVez devuelve la función que libera la plaza, blindada contra la segunda
// llamada. El `defer soltar()` del worker es hoy el único llamante y solo corre una
// vez, pero una liberación de más NO fallaría: le quitaría el sitio a otro y dejaría
// DOS cadenas sobre la misma plaza — el defecto que este paquete existe para impedir,
// entrando por la puerta de atrás y sin dar un solo error.
func soltarUnaVez(ch chan struct{}) func() {
	var una sync.Once
	return func() { una.Do(func() { <-ch }) }
}

// Esperando son las cadenas bloqueadas ahora mismo pidiendo plaza, en todas las
// direcciones. Ver el campo homónimo.
func (a *Aforo) Esperando() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.esperando
}

// canal devuelve el canal de la plaza, creándolo la primera vez.
//
// ⚠️ LOS CANALES NO SE BORRAN NUNCA, y es deliberado. Borrar el de una plaza vacía
// abriría una carrera fea —dos workers con dos canales distintos para la misma
// dirección, o sea DOS plazas donde debe haber una— a cambio de liberar unos bytes
// por Edge que el proceso ya tiene indexados en tres estructuras más. El mapa crece
// con el PARQUE DE EDGES del tenant, no con el tráfico.
func (a *Aforo) canal(p Plaza) chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	ch, hay := a.plazas[p]
	if !hay {
		ch = make(chan struct{}, a.k)
		a.plazas[p] = ch
	}
	return ch
}
