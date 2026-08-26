package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/evidence"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
)

// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL PLAZO POR LLAMADA: LO QUE ESTA ETAPA **NO** FIJA, Y POR QUÉ
// ════════════════════════════════════════════════════════════════════════════
//
// La cabecera de la Ola 2 pide que «el Cloud fije un `timeout_ms` honesto por llamada
// de lote» y que no lo deje a cero «porque el Edge tiene default». P3 es justo la etapa
// que lo hace urgente: cada ítem es UNA llamada de lote de **22–32 s medidos**
// (veredicto §1.4). Aquí queda escrito lo que se investigó y lo que NO se decidió.
//
// CÓMO VIAJA HOY. `local.Provider.plazo` (`internal/llmvia/local/local.go:443`) NO
// inventa nada: HEREDA el deadline del ctx del llamante y le resta `MargenVeredicto`
// (7 s); sin deadline cae a su red de seguridad `local.DefaultTimeout` = **30 s**. El
// wiring de producción (`bootstrap.go:373-380`) no le pasa `local.WithTimeout`, así que
// esos 30 s son el número real.
//
// QUÉ RECIBE P3 HOY, DE VERDAD: **nada**. Esta etapa, como P2, todavía no tiene
// llamante de producción —el worker es T2.5—, así que el único ctx que la alcanza es el
// de sus tests. Y ese hueco tiene DOS lados malos, los dos medibles:
//
//   - **ctx SIN deadline** ⇒ el Edge recibe `timeout_ms = 30 s`, el breaker llama lento
//     a todo lo que pase de `30 × 0,8 = 24 s` (T1.7-2) ⇒ una P3 CALIENTE de 27 s ya
//     cuenta como lenta y una FRÍA de 32 s **muere por timeout sin generar nada**. Es la
//     misma avería de campo del 2026-08-23 que documenta el bloque «UN SOLO RELOJ» de
//     `local.go`, solo que al revés: allí el adaptador cortaba por debajo de su
//     llamante; aquí corta por debajo de lo MEDIDO para esta etapa.
//   - **ctx con el deadline del JOB entero** (lo natural en T2.5: un presupuesto para
//     los N ítems) ⇒ la primera llamada se lleva `restante − 7 s`, que para 10 ítems son
//     minutos ⇒ el umbral de lento se va a minutos y **el breaker se queda ciego**.
//
// POR QUÉ NO SE FIJA AQUÍ UN NÚMERO. Porque el número honesto no existe todavía. La
// condición que tendría que cumplir un plazo por llamada `D` es doble:
//
//	D − 7 s > max(P3)              (que la llamada no muera antes de contestar)
//	(D − 7 s) × 0,8 > p99(P3)      (que una P3 sana no cuente como lenta)
//
// Y **`p99(P3)` no está medido**: lo que hay son DOS observaciones (170 y 293 tokens de
// salida, 22–32 s) y una cifra de planificación (≈ 25 s/ítem, D-044.39). Con el máximo
// observado, la primera condición pide `D > 39 s` y la segunda `D > 47 s` — y un `D` de
// 48 s significa que un ítem puede retener la plaza única 41 s, o sea **10 ítems ≈ 6:50**,
// por encima de la métrica reina del plan. Eso es una decisión de producto con coste
// (roza T2.6 y T2.7), no un ajuste que esta tarea pueda hacer sola.
//
// ⇒ **HUECO SEÑALADO, no número fabricado.** Lo que falta para cerrarlo es un p99 de P3
// en campo, no una decisión de diseño. Quien construya T2.5 tiene que acotar el plazo
// **por llamada** —`context.WithTimeout` alrededor de CADA ítem, no del job— o el
// breaker mentirá en una de las dos direcciones de arriba.
// ════════════════════════════════════════════════════════════════════════════

// Motivos por los que un ítem queda AISLADO: no se pudo especificar, pero no se pierde.
// Son el vocabulario cerrado del campo `reason` de ItemAislado y los lee la bandeja del
// dueño (Ola 3), así que se exportan.
//
// 🔴 LOS TRES ESTÁN AQUÍ JUNTOS A PROPÓSITO, aunque el tercero lo produzca `tope.go`:
// son UN vocabulario, y el lector de `ItemAislado.Reason` tiene que poder verlo entero
// de una vez. Repartirlo por el fichero que produce cada valor es cómo dos mitades del
// mismo enum acaban divergiendo.
//
// ⚠️ LOS TRES PIDEN COSAS DISTINTAS AL DUEÑO, y por eso son tres y no uno: `quality` y
// `evidence` le dicen «el modelo se portó mal con este ítem» —y si se repiten, que mire
// su Ollama—; `over_limit` le dice «este ítem ni se preguntó: el pedido no cabía», que
// no es un fallo de nadie y se resuelve hablando con el cliente. Un motivo único los
// mandaría a diagnosticar la máquina en la mitad de los casos.
const (
	// MotivoCalidad es «el modelo contestó dos veces algo ilegible» (REQ-03: una
	// llamada más un reintento a temperatura 0.3).
	MotivoCalidad = "quality"
	// MotivoEvidencia es «el modelo contestó algo bien formado pero cuya frase de
	// respaldo NO aparece en el texto del cliente»: se lo inventó.
	MotivoEvidencia = "evidence"
	// MotivoTope es «el pedido traía más ítems que MaxItemsPorPedido y a éste no se
	// le llegó a preguntar» (T2.6, ADR-0046 § Mecanismo 1). NO es un fallo: es la
	// admisión acotada, y el ítem queda visible para que el dueño lo atienda. El
	// porqué entero, en `tope.go`.
	MotivoTope = "over_limit"
)

// ErrSinIdeas no existe, y su ausencia es deliberada: un P2 que dejó CERO ideas vivas
// —todas descartadas por el anclaje— no es un fallo (design §3.2: «cero resultados
// válidos tampoco es fatal»). P3 persiste un artefacto vacío, no llama al modelo y no
// devuelve error. Ver el bloque de Run.

// ItemAislado es la MARCA de un ítem que P3 no pudo especificar. El pedido del cliente
// no se pierde: queda anotado en el artefacto para que la bandeja del dueño lo enseñe.
//
// 🔴 NO LLEVA UNA PALABRA DEL CLIENTE, y eso no es una omisión: `IdeaPos` es un PUNTERO
// a `artifacts.p2.wants[IdeaPos]`, que ya está persistido y cifrado como parte del job.
// Duplicar aquí el texto de la idea sería una segunda copia del literal en la base
// —justo lo que el barrido de PII del Plan 046 quitó de en medio— y además una copia que
// podría divergir de la primera.
type ItemAislado struct {
	// IdeaPos es la posición de la idea en la lista que P2 dejó viva.
	IdeaPos int `json:"idea_pos"`
	// Reason es MotivoCalidad o MotivoEvidencia.
	Reason string `json:"reason"`
}

// ArtefactoP3 es lo que la etapa PERSISTE, y es un SUPERCONJUNTO del contrato de
// design §7.2: `{"version":1,"items":[...]}` más la lista de los ítems aislados.
//
// # POR QUÉ NO ES `llm.ItemSpecs` A SECAS
//
// Porque §7.2 describe lo que el MODELO devuelve en UNA llamada —un ítem— y esto es el
// artefacto del fan-out ENTERO: las N respuestas de una sola llamada por ítem, fundidas
// en un `items`, más la marca que el criterio de T2.3 exige («ítem aislado con marca»).
// Esa marca no tiene campo en `llm.ItemSpecs` y dárselo sería modificar
// `shared/wapp-shared` —otro repo, y una release `llm/v0.5.0` por delante de este
// commit—, así que la clave extra la pone el Cloud, que es quien la necesita.
//
// # LA CLAVE EXTRA NO ROMPE AL LECTOR COMPARTIDO, Y ESTÁ COMPROBADO
//
// `llm.ParseItemSpecs` decodifica con `json.Unmarshal` a secas —su `decodeArtifact` dice
// literalmente «tolerante a campos futuros (no usa DisallowUnknownFields)»—, así que un
// `isolated` de más lo ignora en vez de fallar. Lo custodia
// TestP3_ElArtefactoPersistidoLoSigueLeyendoElParserCompartido, porque «lo comprobé
// leyendo el código» envejece y un test no.
type ArtefactoP3 struct {
	// Version es la del artefacto (`llm.ArtifactVersion`). La exige
	// `intake.Artifact.Validate`, que rechaza cualquier cosa sin un entero >= 1.
	Version int `json:"version"`
	// Items son las especificaciones que sobrevivieron: una por ítem que se pudo
	// especificar Y cuya evidencia aparece en el literal del cliente.
	Items []llm.ItemSpec `json:"items"`
	// Isolated son los ítems que no se pudieron especificar, con su motivo. Vacío
	// en el caso normal.
	Isolated []ItemAislado `json:"isolated,omitempty"`
}

// P3 es la etapa de las ESPECIFICACIONES POR ÍTEM (Plan 044 · T2.3): por cada idea que
// P2 dejó viva hace UNA llamada al modelo, con contexto fresco, y le pide que
// especifique ESE ítem y ninguno más (patrón EduGo «1 candidata por llamada»).
//
// # ESTO ES EL FAN-OUT, Y ES LO QUE LA ETAPA ESTRENA
//
// Hasta aquí el pipeline era una llamada por etapa. P3 es N llamadas, y cada una son
// **22–32 s de la plaza única** del Edge. De ahí salen tres cosas, y a día de hoy dos
// están puestas y una no:
//
//   - ✅ **el tope de ítems** (T2.6, 10 por D-044.39): el fan-out YA tiene techo, y lo
//     aplica `acotarAlTope` a la entrada de Run, antes de gastar una sola llamada. Los
//     ítems por encima NO se descartan: quedan marcados con `MotivoTope`. Ver `tope.go`;
//   - 🔴 **el aforo `K = 1` por Edge/plaza** (T2.7): SIGUE SIN HACERSE. Hoy dos cadenas
//     de lote del mismo Edge pueden solaparse;
//   - **el plazo por llamada**: lo cierra T2.5 con `ConPlazoPorLlamada`; lo que queda
//     abierto es el número (`p99(P3)` sin medir). Ver el bloque de arriba.
//
// # P3 PROPONE, EL MATCH DECIDE (D-044.14)
//
// Un `addon_candidate` que EXISTA en el catálogo se volverá línea propia con precio, y
// el que no exista caerá a `customization` de la línea. **Nada de eso pasa aquí**: esta
// etapa no consulta el catálogo, no busca precios y no crea líneas. Estructuralmente no
// puede: sus tres puertos son el log, el selector de vía y `StageStore`, y ninguno sabe
// de catálogos.
//
// Y las `customizations` («sin sal», «sin cebolla») **nunca** se vuelven línea. P3 las
// separa de los candidatos porque el prompt se lo pide; lo que garantiza que no acaben
// cobradas es que viajan en un campo distinto y el matcher no las mira.
//
// # LOS RANGOS SE CONSERVAN TEXTUALES
//
// «10 o 12 porciones» se persiste tal cual. Partirlo en `{min,max,unit}` es de P4
// (T2.4) y elegir un número es de nadie. Esta etapa no toca `variant`.
type P3 struct {
	log    logger.Logger
	sel    ProviderSelector
	store  StageStore
	plazos plazos
}

// NewP3 construye la etapa. Devuelve ErrSinCablear si le falta cualquiera de las tres
// piezas: una etapa a medio cablear no nace «por si acaso».
//
// 🔴 `ConPlazoPorLlamada` acota CADA ÍTEM del fan-out, no el fan-out entero. Es lo que
// el bloque de cabecera de este fichero dejó pedido por escrito, y el motivo por el que
// la opción no se pudo resolver desde el worker (ver plazo.go).
func NewP3(log logger.Logger, sel ProviderSelector, store StageStore, opts ...Opción) (*P3, error) {
	if log == nil || sel == nil || store == nil {
		return nil, ErrSinCablear
	}
	return &P3{log: log, sel: sel, store: store, plazos: nuevosPlazos(opts)}, nil
}

// Run ejecuta el fan-out de P3 sobre un job YA RECLAMADO y devuelve el artefacto tal
// como quedó persistido, que es lo que P4 consume.
//
// `literal` es el `source_text` EN CLARO (lo descifra el worker); `ideas` son las que
// P2 dejó VIVAS, en su orden — el mismo orden al que apunta `ItemAislado.IdeaPos`.
//
// # UNA LLAMADA POR ÍTEM, Y NUNCA UNA POR EL LOTE
//
// Es la decisión que da nombre a la tarea. Mandar los N ítems en un solo prompt sería
// más barato en plaza y peor en todo lo demás: los modelos chicos funden ítems, se
// saltan el último y contagian la variante de uno al de al lado (la lección medida que
// el plan hereda: «ni llamada monstruo ni exceso de micro-llamadas»). Lo custodia
// TestP3_TresItems_TresLlamadas, que cuenta las llamadas del fake.
//
// # LOS TRES DESENLACES DE UN ÍTEM, Y POR QUÉ SON TRES
//
//  1. **Sale bien** ⇒ su spec entra en el artefacto.
//  2. **Sale degenerada** (`llm.ErrLLMQuality`) ⇒ UN reintento a `TemperatureRetry`
//     (0.3) y, si persiste, el ítem queda AISLADO con marca y los demás siguen
//     (REQ-03/REQ-14). El job NO se tumba.
//  3. **Falla la infraestructura** (timeout, Edge sin capacidad, socket caído) ⇒ NO se
//     reintenta aquí y NO se aísla: el error sale hacia arriba con su familia intacta
//     (`%w`) para que el worker suelte el job y lo recoja el backoff de T2.5. Reintentar
//     una caída de red a los dos segundos es gastar la plaza única en volver a fallar, y
//     aislar el ítem sería peor todavía: dejaría al cliente sin un ítem que el sistema
//     nunca llegó a preguntar.
//
// 🔴 La diferencia entre (2) y (3) es la razón de ser de `llm.ErrLLMQuality`, y aquí es
// donde se paga. Lo custodia TestP3_ErrorDeInfraestructura_NiSeReintentaNiSeAisla.
//
// # POR QUÉ EL RETRY VIVE AQUÍ Y EN P2 NO
//
// P2 hace UNA llamada por job: si sale mal, el worker reintenta el job y no se pierde
// nada. P3 hace N: reintentar el JOB por un solo ítem envenenado tiraría las 22–32 s
// que costó cada uno de los otros N−1. El reintento tiene que ser DEL ÍTEM, y por eso
// el docstring de p2.go dice «la política de reintentos es de T2.5» y aquí no: no son
// la misma política. La de T2.5 es la del job.
//
// # CERO IDEAS NO ES UN FALLO
//
// Un P2 que se quedó sin ideas vivas —todas descartadas por el anclaje— deja aquí un
// artefacto vacío, SIN llamar al modelo. Es design §3.2 al pie de la letra («cero
// resultados válidos tampoco es fatal») y además no gasta la plaza en un prompt en el
// que lo único concreto sería lo que listamos nosotros (D-044.24).
//
// # Y MÁS DE `MaxItemsPorPedido` TAMPOCO ES UN FALLO (T2.6)
//
// El corte lo hace `acotarAlTope` AQUÍ, antes de que `fanOut` pida siquiera el
// provider: es el único punto donde ya se sabe cuántos ítems hay y todavía no se ha
// gastado ni una llamada. Los ítems por encima del tope quedan aislados con
// `MotivoTope` —presentes y visibles, nunca descartados— y el job sigue hasta `done`.
// El porqué del número, del sitio y de la política, en `tope.go`.
func (s *P3) Run(ctx context.Context, job intake.ClaimedJob, literal string, ideas []llm.Want) (*ArtefactoP3, error) {
	if literal == "" {
		return nil, ErrSinLiteral
	}

	atendidas, sobrantes := acotarAlTope(ideas)

	art := &ArtefactoP3{Version: llm.ArtifactVersion, Items: make([]llm.ItemSpec, 0, len(atendidas))}
	if len(atendidas) > 0 {
		if err := s.fanOut(ctx, job, literal, atendidas, art); err != nil {
			return nil, err
		}
	}
	s.marcarSobreTope(art, len(atendidas), sobrantes, job.ID)

	if err := s.persistir(ctx, job.ID, art); err != nil {
		return nil, err
	}
	// 🔴 `items_sobre_tope` va APARTE de `items_aislados` y no sumado dentro: los dos
	// estados que caben en «aislado» piden cosas OPUESTAS al dueño —mirar su Ollama, o
	// hablar con el cliente—, y un solo número mentiría en la mitad de los casos.
	s.log.Info("p3: especificaciones por ítem extraídas y persistidas",
		"job_id", job.ID, "stage", intake.StageP3,
		"ideas", len(ideas), "items", len(art.Items),
		"items_aislados", len(art.Isolated), "items_sobre_tope", sobrantes)
	return art, nil
}

// fanOut recorre las ideas y llena el artefacto. Devuelve error SOLO cuando el fallo es
// de infraestructura: todo lo demás —salida ilegible, evidencia inventada— se resuelve
// aislando el ítem y siguiendo.
//
// El provider se pide UNA VEZ, fuera del bucle, y no una por ítem: la vía es del tenant
// y de la sesión de origen, no de la idea, y `Selector.For` lee la configuración del
// tenant. N llamadas a `For` por pedido serían N lecturas para obtener N veces lo mismo.
//
// # EL ANCLAJE, CON LA MISMA REGLA QUE P2 Y UNA RESPUESTA DISTINTA
//
// La regla es la de `internal/evidence`, la misma y desde el mismo sitio: la frase que
// el modelo dice haber copiado tiene que aparecer en el literal. La RESPUESTA sí cambia:
// P2 descarta la idea sin respaldo y sigue, y aquí el ítem se AÍSLA con marca. No es
// una tolerancia distinta, es que la unidad es distinta: en P2 la idea sin respaldo se
// la acababa de inventar el modelo y no hay nada que perder; aquí P2 YA demostró que el
// cliente pidió este ítem —su `want` pasó el anclaje—, así que hacerlo desaparecer sería
// perder una petición real. Aislar es lo conservador; descartar, no.
//
// Y NO se reintenta: una evidencia inventada es una salida bien formada que miente, no
// una salida ilegible, y subir la temperatura no la vuelve honesta. Volver a llamar
// costaría otras 22–32 s de la plaza única para, con suerte, inventar otra frase.
func (s *P3) fanOut(ctx context.Context, job intake.ClaimedJob, literal string, ideas []llm.Want, art *ArtefactoP3) error {
	prov, err := s.sel.For(ctx, job.Key.TenantID, job.Key.SessionID)
	if err != nil {
		return fmt.Errorf("p3: elegir el proveedor del tenant: %w", err)
	}

	norm := evidence.Normalize(literal)
	for i := range ideas {
		spec, motivo, err := s.especificar(ctx, prov, literal, ideas[i].Idea, job.ID, i)
		if err != nil {
			return err
		}
		if motivo == "" && !evidence.Contains(norm, spec.Evidence) {
			// El modelo devolvió algo bien formado que no sale del texto del
			// cliente: ni se reintenta ni se descarta en silencio. Se aísla.
			// El porqué de las dos cosas, en el docstring de esta función.
			s.log.Warn("p3: la evidencia del ítem no aparece en el literal del cliente; el ítem queda aislado",
				"job_id", job.ID, "stage", intake.StageP3, "idea_pos", i)
			motivo = MotivoEvidencia
		}
		if motivo != "" {
			art.Isolated = append(art.Isolated, ItemAislado{IdeaPos: i, Reason: motivo})
			continue
		}
		art.Items = append(art.Items, *spec)
	}
	return nil
}

// especificar resuelve UN ítem. Devuelve, y los tres retornos son excluyentes:
//
//   - `(spec, "", nil)` — salió bien;
//   - `(nil, motivo, nil)` — hay que aislarlo, y el job sigue;
//   - `(nil, "", err)` — infraestructura: el job entero se suelta.
//
// # EL REINTENTO ES EXACTAMENTE UNO, Y SOLO POR CALIDAD
//
// Uno porque lo dice REQ-03 («exactamente una vez con temperatura 0.3») y porque cada
// intento cuesta 22–32 s de la plaza única: un segundo reintento por ítem convertiría un
// pedido de 5 ítems en 5 minutos de cola ajena. Y solo por calidad porque un fallo de
// infraestructura no se arregla subiendo la temperatura.
//
// La temperatura sube a 0.3 y no a más: lo justo para que el modelo no repita palabra
// por palabra la misma salida degenerada (el docstring de `llm.TemperatureRetry` lo
// dice, y el número lo fija el paquete compartido, no esta etapa).
func (s *P3) especificar(ctx context.Context, prov llm.LLMProvider, literal, idea, jobID string, pos int) (*llm.ItemSpec, string, error) {
	spec, err := s.unaLlamada(ctx, prov, literal, idea, llm.TemperatureGreedy, jobID, pos)
	if err == nil {
		return spec, "", nil
	}
	if !errors.Is(err, llm.ErrLLMQuality) {
		return nil, "", fmt.Errorf("p3: especificar el ítem en la posición %d: %w", pos, err)
	}

	s.log.Warn("p3: la salida del modelo no es legible; se reintenta UNA vez a temperatura de reintento",
		"job_id", jobID, "stage", intake.StageP3, "idea_pos", pos)

	spec, err = s.unaLlamada(ctx, prov, literal, idea, llm.TemperatureRetry, jobID, pos)
	if err == nil {
		return spec, "", nil
	}
	if !errors.Is(err, llm.ErrLLMQuality) {
		return nil, "", fmt.Errorf("p3: especificar el ítem en la posición %d (reintento): %w", pos, err)
	}

	s.log.Warn("p3: la salida sigue sin ser legible tras el reintento; el ítem queda aislado y el resto del pedido sigue",
		"job_id", jobID, "stage", intake.StageP3, "idea_pos", pos)
	return nil, MotivoCalidad, nil
}

// unaLlamada es UNA pasada por el cable y su lectura. El error sale SIN envolver a
// propósito: quien lo recibe tiene que poder preguntar `errors.Is(err, ErrLLMQuality)` y
// —si es de transporte— sacarle el motivo de degradación con `errors.As`. Envolverlo
// aquí con un prefijo por etapa no rompería ninguna de las dos cosas, pero el sitio
// donde se decide qué es cada error es `especificar`, y el prefijo lo pone allí una vez.
//
// # DOS SALIDAS BIEN FORMADAS QUE AUN ASÍ SON DEGENERADAS
//
//  1. **Cero ítems.** Se pidió UNO y no vino ninguno: `llm.ParseItemSpecs` lo acepta
//     (su bucle recorre cero elementos) y sería un ítem que desaparece sin marca. Se
//     trata como fallo de calidad ⇒ reintento y, si persiste, aislamiento. Es
//     estrictamente más conservador que aceptarlo.
//  2. **Más de un ítem.** El prompt dice «especifica UN SOLO ítem: ignora los demás,
//     aunque aparezcan en el texto», y el hilo entero va en el prompt como contexto. Un
//     modelo chico que lo ignore devolvería en CADA una de las N llamadas los N ítems
//     ⇒ N² especificaciones y el mismo producto cobrado N veces. Por eso se queda el
//     PRIMERO y los demás se cuentan en el log: la 1:1 entre idea y spec es lo que
//     sostiene el `IdeaPos` de la marca, y el lado seguro de romperla es perder una
//     repetición, nunca duplicar una línea con precio.
func (s *P3) unaLlamada(ctx context.Context, prov llm.LLMProvider, literal, idea string, temp float64, jobID string, pos int) (*llm.ItemSpec, error) {
	raw, err := s.pedirSpec(ctx, prov, literal, idea, temp)
	if err != nil {
		return nil, err
	}

	specs, err := llm.ParseItemSpecs(raw)
	if err != nil {
		// 🔴 El error NO cita `raw`: la salida del modelo lleva frases del cliente.
		return nil, fmt.Errorf("la salida del modelo no es un artefacto P3 legible: %w", err)
	}
	if len(specs.Items) == 0 {
		return nil, fmt.Errorf("%w: la llamada del ítem no devolvió ninguna especificación", llm.ErrLLMQuality)
	}
	if len(specs.Items) > 1 {
		s.log.Warn("p3: la llamada de un ítem devolvió varias especificaciones; se conserva la primera",
			"job_id", jobID, "stage", intake.StageP3, "idea_pos", pos, "descartadas", len(specs.Items)-1)
	}
	return &specs.Items[0], nil
}

// pedirSpec es LA llamada de UN ítem, acotada por SU propio plazo — el de una llamada,
// no el de las N. Está extraída para que el `defer cancel()` cierre el plazo donde
// acaba la llamada y no arrastre el deadline al parseo, al anclaje ni a la
// persistencia: eso convertiría el plazo por llamada en un plazo por etapa por la
// puerta de atrás.
//
// 🔴 EL PLAZO SE APLICA TAMBIÉN AL REINTENTO por calidad (temperatura 0.3), y tiene
// que ser así: el reintento es otra llamada de lote de 22–32 s, y dejarlo sin acotar
// mandaría al Edge un `timeout_ms` distinto —el default de 30 s— para exactamente el
// mismo trabajo, corrompiendo la señal del breaker justo en el caso raro.
func (s *P3) pedirSpec(ctx context.Context, prov llm.LLMProvider, literal, idea string, temp float64) (json.RawMessage, error) {
	llamada, cancel := s.plazos.acotar(ctx)
	defer cancel()
	return prov.ExtractItemSpecs(llamada,
		llm.ExtractItemSpecsInput{SourceText: literal, Idea: idea},
		llm.Options{Temperature: temp})
}

// persistir serializa el artefacto y lo deja en la máquina de estados.
//
// Se serializa el artefacto DEL CLOUD y no la salida cruda del modelo por el mismo
// motivo que en P2: lo que se guarda es lo que P4 se va a creer, y guardar el crudo
// dejaría dentro las specs inventadas —descartadas de boquilla, presentes en la base—.
//
// No se revalida el `version` aquí: la puerta es `intake.Artifact.Validate`, dentro de
// `SaveStage`. Una segunda red con el mismo síntoma taparía a los tests de conducta de
// la primera.
func (s *P3) persistir(ctx context.Context, jobID string, art *ArtefactoP3) error {
	payload, err := json.Marshal(art)
	if err != nil {
		return fmt.Errorf("p3: serializar el artefacto: %w", err)
	}
	guardado, err := s.store.SaveStage(ctx, jobID, intake.Artifact{
		Stage:   intake.StageP3,
		Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("p3: persistir el artefacto: %w", err)
	}
	if !guardado {
		return ErrJobFueraDeProcessing
	}
	return nil
}
