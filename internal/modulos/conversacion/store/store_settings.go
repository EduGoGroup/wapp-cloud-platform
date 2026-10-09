// Porta internal/flujos/store/store.go @ c0c0c03
//
// Trozo de store.go (05 E-13): la configuración por tenant (public.tenant_settings),
// sus valores por defecto y la bienvenida única (public.conversation_welcomes), que se
// gobierna con dos claves de esa configuración. Solo declaraciones movidas.

package store

import (
	"context"
	"time"
)

// TenantSettingsReader lee la config del carrito por-tenant (Plan 016 · T0).
type TenantSettingsReader interface {
	// GetTenantSettings devuelve la config del carrito para tenantID desde
	// public.tenant_settings (Plan 016 · T0). Si el tenant NO tiene fila, devuelve
	// DefaultTenantSettings(tenantID) SIN error: el carrito funciona sin configurar
	// nada (design.md §9.E/§9.G). Si SÍ tiene fila, devuelve lo que diga la fila sin
	// sustituir ceros por defaults (un 0 puede ser un override explícito, no un hueco).
	GetTenantSettings(ctx context.Context, tenantID string) (TenantSettings, error)
}

// WelcomeMark es el estado de la BIENVENIDA ÚNICA de UNA conversación (Plan 044 ·
// T1.8-2, D6): lo que la tabla `conversation_welcomes` guarda, y lo único que hace
// falta para decidir si a este turno le toca.
//
// Los dos ceros significan cosas distintas y las dos importan:
//
//   - LastIncomingAt cero ⇒ NO HAY FILA: este contacto nunca habló por esta sesión.
//   - WelcomedAt cero ⇒ NUNCA SE LE SALUDÓ, que es el estado correcto de una
//     conversación nueva (el NULL de la columna, igual que fleet_sessions.greeted_at).
//
// 🔴 SON INSTANTES ESCRITOS POR EL RELOJ DEL RUNTIME, no por el de Postgres. El
// llamante pasa su `now` y compara contra lo que aquí vuelve, así que los dos lados
// de la resta salen del MISMO reloj. Meter `now()` de la BD en la escritura y
// `time.Now()` de Go en la comparación es el fallo permanente y silencioso de
// comparar dos relojes, ya documentado en esta casa.
type WelcomeMark struct {
	// LastIncomingAt es el instante del ÚLTIMO mensaje del contacto ANTES de este
	// turno. Es el ancla del umbral de silencio (TenantSettings.WelcomeSilence).
	LastIncomingAt time.Time
	// WelcomedAt es el instante de la ÚLTIMA bienvenida entregada. Cero = ninguna.
	// Sirve además de TESTIGO para el compare-and-set de MarkWelcomed.
	WelcomedAt time.Time
}

// WelcomeStore persiste el estado de la bienvenida única por conversación (Plan 044 ·
// T1.8-2, D6). Son DOS métodos y no uno porque el envío va EN MEDIO: primero se
// registra que el contacto habló y se pregunta si toca, luego se manda, y solo si el
// Edge acusa `ok=true` se marca. Es el runbook exacto de fleet_sessions.greeted_at
// (0066 / gateway/grpc/greeting.go): un aviso que no llegó no se da por dado.
type WelcomeStore interface {
	// TouchContact registra que el contacto acaba de escribir (last_incoming_at :=
	// now) y devuelve el estado que había ANTES de este turno. Una sola sentencia:
	// corre en línea con el mensaje del cliente.
	//
	// 🔴 DEVUELVE EL ESTADO PREVIO, NO EL NUEVO, y ese matiz es toda la función: el
	// umbral de silencio se mide contra el mensaje ANTERIOR del contacto. Si
	// devolviera el estado ya tocado, LastIncomingAt valdría siempre `now`, el
	// silencio sería siempre 0 y la bienvenida no volvería NUNCA tras la primera.
	TouchContact(ctx context.Context, key Key, now time.Time) (WelcomeMark, error)
	// MarkWelcomed sella la bienvenida como entregada, con CENTINELA sobre el testigo
	// leído: solo escribe si `welcomed_at` sigue siendo EXACTAMENTE el que
	// TouchContact devolvió. Devuelve false —sin error— si otro turno ganó la carrera.
	//
	// Se llama SOLO cuando el Ack del Edge vuelve ok=true. Si el envío falla, no se
	// llama: el siguiente mensaje del contacto reintenta solo.
	MarkWelcomed(ctx context.Context, key Key, witness WelcomeMark, now time.Time) (bool, error)
}

// TenantSettings es la config del carrito por-tenant (public.tenant_settings,
// Plan 016 · design.md §3.4/§9.G). PageSize es el tamaño de página de la
// paginación (default 5); OrderTTL se LEE y no se obedece (ver su campo).
// GetTenantSettings devuelve los defaults si el tenant no tiene fila.
type TenantSettings struct {
	TenantID string
	PageSize int // default DefaultPageSize
	// OrderTTL es el TTL de la solicitud (order_ttl_seconds INTEGER, default
	// 3600s), DEROGADO como causa de muerte por D-041.16 (T4.7): se sigue leyendo
	// por compatibilidad y NINGÚN código actúa sobre él. Si vuelves a consumirlo
	// para matar algo, estás reintroduciendo el reloj que este plan derogó.
	OrderTTL time.Duration
	// ConversationTTL es el TTL CONVERSACIONAL genérico (Plan 029 · T9, migración
	// 0034): tiempo tras el cual un estado vivo sin tocar se descarta y el entrante
	// arranca de nuevo. Persistido como conversation_ttl_seconds; 0 (DEFAULT) ⇒ sin
	// vencimiento (tenants existentes intactos). Es el ÚNICO TTL vivo de este
	// struct: OrderTTL quedó derogado (D-041.16) y este no lo sustituye — vencer
	// la conversación descarta el estado conversacional, jamás la solicitud.
	ConversationTTL time.Duration
	// BuyerFields es el CHECKLIST de datos mínimos del comprador que el dueño
	// configura (buyer_fields JSONB, migración 0045 · D-041.13). Vacío ⇒ el carrito
	// no pregunta nada y el recorrido de compra es EXACTAMENTE el de antes (INV-15).
	//
	// Es CONFIGURACIÓN, no dato: aquí viven las etiquetas de lo que se va a pedir
	// («RUT», «Dirección de entrega»), jamás lo que el cliente responde. Lo que el
	// cliente responde es PII y no pasa por este struct ni por Vars: viaja en un
	// efecto privado y acaba CIFRADO en intake_buyer_data (T4.5).
	BuyerFields []BuyerField
	// EventInactivityTTL es EL reloj de conversación, en singular (ADR-0029 E-6 /
	// D-043.7, migración 0052 · event_inactivity_ttl_seconds): el silencio tolerado
	// entre interacciones de un evento. Se mide desde conversation_events.last_activity_at
	// y se REFRESCA en cada interacción, así que una conversación activa nunca vence.
	//
	// 0 ⇒ SIN VENCIMIENTO, y es un override EXPLÍCITO de la empresa, no un "no
	// configurado": la columna es NOT NULL DEFAULT 7200, de modo que un tenant que no
	// tocó nada trae 2 h y solo llega un 0 aquí si alguien lo escribió. Confundir los
	// dos casos es el error caro de este campo (ver GetTenantSettings).
	//
	// No sustituye a ConversationTTL ni se colapsa con él (ADR-0029 E-9.2): aquel es
	// recolección de basura del flow_state y solo se evalúa cuando NO hay evento activo.
	EventInactivityTTL time.Duration
	// EventHistoryTTL es RETENCIÓN DE DATOS, no un reloj de conversación (D-043.13,
	// migración 0052 · event_history_ttl_seconds): cuánto tiempo se tiene derecho a
	// guardar el texto del hilo (conversation_event_messages) antes de que la poda
	// perezosa lo vacíe. Vencer no interrumpe ninguna conversación.
	//
	// 🔴 ESTA CLAVE ES INERTE: SE LEE Y NADIE LA OBEDECE (D-046.14, ADR-0043).
	// La columna existe y se carga aquí, pero NO HAY PODA construida ni la va a haber:
	// el Plan 046 la descartó el 2026-08-20 porque wApp no es sistema de registro
	// contable ni fiscal. La prueba dura de que nada la ejecuta es que purged_at
	// (0051_conversation_events.sql) tiene CERO apariciones en .go.
	//
	// Se conserva en vez de retirarse porque quitarla costaría otra migración y romper
	// TenantSettings para no ganar nada. Y quien retome la retención algún día EMPIEZA
	// POR ESCRIBIR LA PODA, no por elegir un número: mientras no exista el barrido,
	// cambiar este valor no hace absolutamente nada.
	EventHistoryTTL time.Duration
	// AggregationWindow es LA VENTANA DEL AGREGADOR de captación (Plan 044 · T1.2,
	// migración 0072 · aggregation_window_seconds INTEGER NOT NULL DEFAULT 45).
	// Cuántos segundos espera el IntakeAggregator DESDE EL PRIMER MENSAJE de la
	// ventana antes de cerrarla (aggregating -> pending) y disparar el pipeline.
	//
	// 🔧 DESDE T1.8-1 LA VENTANA ES HÍBRIDA Y ESTE CAMPO YA NO SE PUEDE LEER SOLO. El
	// plazo cuenta desde el ÚLTIMO mensaje del cliente (`intake_jobs.updated_at`), no
	// desde el primero, así que una ráfaga lo reinicia; quien pone el límite duro es
	// AggregationMax (abajo). Aquí decía «ES LA LATENCIA DE PEOR CASO DEL PIPELINE» y
	// eso ahora le corresponde al techo.
	//
	// 🔴 LO QUE SIGUE SIENDO CIERTO, Y HAY QUE LEERLO LITERAL DESDE T1.7: la señal que
	// ADELANTA el cierre puede no llegar nunca por causas NORMALES, así que sin ella los
	// plazos de esta ventana son la latencia de TODOS los casos, no la del peor. Pesan
	// directamente sobre la métrica reina del plan (primer borrador en < 5 min, T6.1).
	//
	// 🔧 Y LOS MOTIVOS CAMBIARON CON LA OLA 1.6 (D-044.31), aunque la conclusión no:
	// aquí se listaban los del PUSH del Edge (presupuesto de espera del despachador,
	// mensaje sin texto, grupo, breaker abierto). Ese push murió. Hoy la señal la PIDE
	// el Cloud (internal/intakeahead) y puede faltar por otros: vía caída, cola de
	// clasificación llena, catálogo de intents sin publicar, umbral no alcanzado, o una
	// respuesta que llega después de que la ventana ya cerró.
	//
	// 🔴 0 ES UN OVERRIDE LEGÍTIMO y significa FLUSH INMEDIATO —un pipeline por
	// mensaje, que es lo que el sistema hacía antes de esta ola—, no «sin
	// configurar». La distinción es la misma que muerde en EventInactivityTTL: con
	// fila, el 0 se respeta; sin fila manda DefaultAggregationWindow (45 s).
	AggregationWindow time.Duration
	// AggregationMax es el TECHO de esa misma ventana (Plan 044 · T1.8-1, migración
	// 0076 · aggregation_max_seconds INTEGER NOT NULL DEFAULT 120): cuántos segundos
	// como mucho puede el cliente seguir añadiendo mensajes al MISMO job desde que la
	// ventana nació (`created_at`) antes de que el barrido decida que ya hay bastante.
	//
	// 🔴 LOS DOS PLAZOS SON UNA SOLA REGLA Y NO SE PUEDEN LEER POR SEPARADO: la ventana
	// cierra por lo que llegue ANTES —silencio (`now()-updated_at >= AggregationWindow`)
	// o techo (`now()-created_at >= AggregationMax`)—. Sin techo, una conversación que
	// gotea cada 40 s no alcanza nunca los 45 s de silencio y su job NO CIERRA JAMÁS;
	// sin silencio, una ráfaga tecleada despacio se parte en dos jobs. Cada uno tapa
	// el agujero del otro, así que quien toque uno tiene que mirar el otro.
	//
	// 🔴 0 SIGNIFICA «VENCIDO SIEMPRE» (cierre en el primer barrido), la MISMA lectura
	// que el 0 de AggregationWindow — y NO «sin techo», que no existe a propósito: la
	// ausencia de techo es el defecto que T1.8-1 vino a cerrar. Ver el COMMENT de la
	// columna en la 0076.
	AggregationMax time.Duration
	// WelcomeText es el TEXTO FIJO de la bienvenida única (Plan 044 · T1.8-2, D6,
	// migración 0076 · welcome_text TEXT NOT NULL DEFAULT ''): la frase «estamos
	// procesando» que el Cloud le manda AL CLIENTE al primer mensaje de una
	// conversación, y otra vez tras WelcomeSilence de silencio.
	//
	// 🔴 CADENA VACÍA = EL TEXTO DE PLATAFORMA (DefaultWelcomeText), y NO «sin
	// bienvenida». Aquí el cero de Go NO es un override —al revés que en las dos
	// columnas de arriba—, y la asimetría es deliberada: '' es el DEFAULT de la
	// columna, así que TODA fila preexistente lo trae, y leerlo como «sin bienvenida»
	// apagaría la funcionalidad entera para todo tenant que tuviera fila. Apagar la
	// bienvenida se hace quitándole al tenant la feature `llm_intake`, que es el único
	// interruptor y es fail-closed.
	//
	// ⚠️ NO ES CONTENIDO DEL ANÁLISIS: este literal no entra en source_refs ni en
	// source_text (ni rotulado) ni mueve el updated_at del job. Ver welcome.go.
	WelcomeText string
	// WelcomeSilence es cuánto silencio del contacto hace falta para que la
	// bienvenida VUELVA a mandarse (Plan 044 · T1.8-2, migración 0076 ·
	// welcome_silence_seconds INTEGER NOT NULL DEFAULT 86400). Se mide contra
	// conversation_welcomes.last_incoming_at, que es el instante del último mensaje
	// del contacto — NUNCA contra el de la última bienvenida (ver WelcomeMark).
	//
	// 🔴 0 SIGNIFICA «VENCIDO SIEMPRE» (bienvenida en cada mensaje que no avance una
	// conversación viva), la MISMA lectura que el 0 de AggregationWindow y
	// AggregationMax — y NO la de ConversationTTL, donde 0 es «sin vencimiento». En
	// esta tabla el 0 ya significa dos cosas distintas y por eso se dice cuál es la de
	// aquí. Con fila, el 0 se respeta; sin fila manda DefaultWelcomeSilence (24 h).
	WelcomeSilence time.Duration
}

// DefaultTenantSettings es la config que vale para un tenant SIN fila en
// public.tenant_settings. Espeja los DEFAULT de las columnas (migraciones 0013,
// 0034, 0045, 0052 y 0067) y es la ÚNICA definición de ese juego de valores: la usan
// tanto PostgresRepository como MemoryRepository, para que los dos no puedan
// divergir en silencio como divergirían dos literales copiados.
//
// «Sin fila» es lo único que esto responde. Un tenant CON fila devuelve lo que diga
// la fila, aunque coincida con el cero de Go: ahí no se aplica nada de esto.
func DefaultTenantSettings(tenantID string) TenantSettings {
	return TenantSettings{
		TenantID: tenantID,
		PageSize: DefaultPageSize,
		OrderTTL: DefaultOrderTTL,
		// 🔴 ConversationTTL SE NOMBRA DESDE T4.4 (Plan 046), y omitirlo era el defecto:
		// mientras no estuvo aquí valía el cero de Go, o sea «sin vencimiento», y este
		// es el camino por el que pasan los tenants SIN fila --hoy 2 de 3 en UAT--, que
		// NO leen el DEFAULT de la 0067. Sin esta línea, aquel ALTER no habría llegado a
		// la mayoría de los tenants. BuyerFields nil ⇒ el carrito no pregunta nada.
		ConversationTTL:    DefaultConversationTTL,
		EventInactivityTTL: DefaultEventInactivityTTL,
		EventHistoryTTL:    DefaultEventHistoryTTL,
		// 🔴 AggregationWindow SE NOMBRA AQUÍ POR EL MISMO MOTIVO QUE ConversationTTL
		// arriba, y omitirla sería repetir EXACTAMENTE aquel defecto (design §6.5, el
		// cuarto sitio): sin esta línea, un tenant SIN fila se quedaría con el cero de
		// Go, y aquí el 0 no significa «sin configurar» sino FLUSH INMEDIATO — o sea,
		// la agregación apagada en silencio para todos los tenants que no tienen fila,
		// que hoy son 2 de 3 en UAT. El DEFAULT 45 de la 0072 solo alcanza a las filas
		// que existen; este espejo alcanza a las que no.
		AggregationWindow: DefaultAggregationWindow,
		// 🔴 AggregationMax SE NOMBRA POR EL MISMO MOTIVO Y CON MÁS URGENCIA (T1.8-1):
		// omitirla dejaría el cero de Go, que aquí significa VENCIDO SIEMPRE — o sea, un
		// tenant SIN fila cerraría su ventana en el PRIMER barrido, un job por mensaje y
		// la agregación entera apagada, que es justo el estado que 2 de 3 tenants de UAT
		// tendrían por no tener fila. El DEFAULT 120 de la 0076 solo alcanza a las filas
		// que existen; este espejo alcanza a las que no.
		AggregationMax: DefaultAggregationMax,
		// 🔴 WelcomeText y WelcomeSilence SE NOMBRAN POR EL MISMO MOTIVO QUE SUS DOS
		// VECINAS (T1.8-2, y ya es el quinto sitio donde muerde lo mismo): omitir
		// WelcomeSilence dejaría el cero de Go, que aquí significa VENCIDO SIEMPRE — o
		// sea, un tenant SIN fila recibiría la bienvenida en CADA mensaje que no
		// avanzara conversación viva, que es exactamente lo que el enunciado prohíbe
		// («nunca por interacción»), y le pasaría a 2 de 3 tenants de UAT por no tener
		// fila. WelcomeText es el caso benigno —'' ya significa «el de plataforma»— y se
		// nombra igualmente para que las dos claves de la bienvenida se lean juntas.
		WelcomeText:    DefaultWelcomeText,
		WelcomeSilence: DefaultWelcomeSilence,
	}
}

// BuyerField es UN campo del checklist del comprador (D-041.13, ADR-0031 §5).
//
// wApp NO valida la semántica (REQ-25): no sabe qué es un RUT ni si una dirección
// existe. Recolecta lo que el dueño pide y lo pasa. Por eso el campo no tiene tipo
// ni patrón — inventarlos sería prometer una validación que no se hace.
//
// Las etiquetas json son el CONTRATO con la columna JSONB y con el round-trip por
// Conversation.Vars: el runtime siembra estos campos para que el módulo puro los
// lea sin tocar la BD, y ese viaje pasa por JSONB (map[string]any) en cuanto la
// conversación se guarda.
type BuyerField struct {
	// Key es el identificador ESTABLE del dato ("rut"): es la clave con la que se
	// guarda dentro del blob cifrado. Renombrarla en la config no renombra lo ya
	// guardado.
	Key string `json:"key"`
	// Label es lo que se le enseña al cliente por WhatsApp ("RUT"). Vacío ⇒ se
	// pregunta por la Key, que siempre existe.
	Label string `json:"label"`
	// Required distingue lo que el carrito PIDE antes de cerrar de lo que solo
	// declara. Hoy el cart numérico pregunta EXCLUSIVAMENTE los required (D-041.13):
	// un campo opcional en un menú numérico obligaría a inventar una tecla de
	// "saltar" en cada paso, y quien no quiera pedir algo simplemente no lo pone.
	Required bool `json:"required"`
}

// Defaults de tenant_settings (design.md §9.E/§9.G): valen cuando el tenant no
// tiene fila en public.tenant_settings. Espejan los DEFAULT de las migraciones 0013,
// 0034 (conversation_ttl_seconds, hoy con el DEFAULT que le puso la 0067), 0045 y
// 0052.
const (
	// DefaultPageSize es el tamaño de página por defecto de la paginación del carrito.
	DefaultPageSize = 5
	// DefaultOrderTTL es el default de order_ttl_seconds (3600s = 1h) que espeja la
	// migración 0013. DEROGADO como causa de muerte (D-041.16): es el valor que se
	// devuelve cuando el tenant no tiene fila, no un plazo que alguien aplique.
	DefaultOrderTTL = time.Hour
	// DefaultConversationTTL es el default de PLATAFORMA de conversation_ttl_seconds
	// (7200s = 2h, D-046.12 / REQ-19) que espeja el DEFAULT de la migración 0067.
	//
	// 🔴 ANTES ERA 0, Y EL 0 SIGNIFICABA «SIN VENCIMIENTO». Esa era la raíz del
	// hallazgo de privacidad del Plan 046: con 0, el flow_state y sus vars --que
	// llevan el TEXTO LITERAL del cliente-- no caducaban nunca. El valor se igualó al
	// del reloj único (DefaultEventInactivityTTL) por D-046.12.
	//
	// Que compartan número NO los convierte en la misma clave: este es el reloj
	// SUBORDINADO y solo se evalúa con flow_state.event_id IS NULL; con evento
	// conversacional activo manda el otro (ADR-0029 §E-9.2). Colapsarlos está
	// prohibido.
	//
	// Vale para el tenant SIN fila; un tenant CON fila manda siempre, incluido su 0
	// explícito, que sigue siendo un override legítimo («sin vencimiento»).
	DefaultConversationTTL = 2 * time.Hour
	// DefaultEventInactivityTTL es el default de PLATAFORMA de
	// event_inactivity_ttl_seconds (7200s = 2h, D-043.7 / ADR-0029 E-6) que espeja el
	// DEFAULT de la migración 0052. Vale para el tenant SIN fila; un tenant CON fila
	// manda siempre, incluido su 0 («sin vencimiento»).
	DefaultEventInactivityTTL = 2 * time.Hour
	// DefaultEventHistoryTTL es el default TÉCNICO de event_history_ttl_seconds (0 =
	// sin poda) que espeja el DEFAULT de la migración 0052. Se nombra en vez de dejar
	// el cero implícito para que quien lo cambie sepa qué está cambiando.
	//
	// 🔴 Y lo que está cambiando hoy es NADA: la clave es INERTE (D-046.14, ADR-0043).
	// No hay poda que la obedezca ni se va a construir. Ver el comentario del campo
	// EventHistoryTTL en TenantSettings, que lleva el detalle.
	DefaultEventHistoryTTL = time.Duration(0)
	// DefaultAggregationWindow es el default de PLATAFORMA de
	// aggregation_window_seconds (45 s, Plan 044 · T1.2) que espeja el DEFAULT de la
	// migración 0072. Vale para el tenant SIN fila; un tenant CON fila manda siempre,
	// incluido su 0 explícito, que aquí significa FLUSH INMEDIATO (sin agregación).
	//
	// ⚠️ NO es un número de estilo: es la latencia que el cliente espera a su primer
	// borrador cuando el intent no llega (T1.7), y el 044 se mide contra «< 5 min»
	// (T6.1). Subirlo es gastar ese presupuesto.
	DefaultAggregationWindow = 45 * time.Second
	// DefaultAggregationMax es el default de PLATAFORMA de aggregation_max_seconds
	// (120 s, Plan 044 · T1.8-1) que espeja el DEFAULT de la migración 0076. Vale para
	// el tenant SIN fila; un tenant CON fila manda siempre, incluido su 0 explícito,
	// que aquí significa VENCIDO SIEMPRE (cierre en el primer barrido).
	//
	// ⚠️ NO es un número de estilo, y pesa MÁS que la ventana de silencio: es el peor
	// caso REAL de latencia hasta el flush cuando el cliente teclea despacio, y el 044
	// se mide contra «< 5 min» (T6.1). Dos de esos cinco minutos se van aquí.
	//
	// 🔴 SU RELACIÓN CON DefaultAggregationWindow NO ES UNA INVARIANTE. Que 120 > 45 es
	// lo normal, pero un tenant puede configurar el techo por DEBAJO de su ventana y es
	// legítimo: significa «plazo fijo desde el primer mensaje», que es exactamente lo
	// que el sistema hacía antes de esta ola. No hay CHECK cruzado en la 0076 y no debe
	// haber un `if` aquí que lo simule.
	DefaultAggregationMax = 120 * time.Second
	// DefaultWelcomeSilence es el default de PLATAFORMA de welcome_silence_seconds
	// (86400 s = 24 h, Plan 044 · T1.8-2) que espeja el DEFAULT de la migración 0076.
	// Vale para el tenant SIN fila; un tenant CON fila manda siempre, incluido su 0
	// explícito, que aquí significa VENCIDO SIEMPRE (bienvenida en cada mensaje que no
	// avance conversación viva).
	//
	// 🔴 24 h NO ES UN NÚMERO DE ESTILO Y SU RELACIÓN CON LOS OTROS RELOJES SÍ IMPORTA
	// —al revés que la de AggregationMax con AggregationWindow—: está muy por encima de
	// DefaultConversationTTL y DefaultEventInactivityTTL (2 h los dos), y esa distancia
	// es lo que hace que el umbral no pueda vencer DURANTE una conversación viva. Para
	// que venciera, la conversación tendría que llevar 24 h de silencio, y a las 2 h el
	// reloj que manda ya la habría soltado. Quien lo baje por debajo de 2 h se queda
	// SOLO con la guarda del runtime (que no saluda sobre conversación viva); sigue
	// siendo correcto, pero es UNA red en vez de dos.
	DefaultWelcomeSilence = 24 * time.Hour
)

// DefaultWelcomeText es el texto de PLATAFORMA de la bienvenida única (Plan 044 ·
// T1.8-2, D6): lo que recibe el cliente de un tenant que no configuró
// `welcome_text`, y lo que recibe también el tenant SIN fila en tenant_settings.
//
// Es una CONSTANTE de Go y no un DEFAULT de la columna a propósito: el DEFAULT de la
// columna es la CADENA VACÍA —que significa «el de plataforma»—, de modo que cambiar esta frase
// llega a TODO tenant que no la haya sobrescrito sin necesitar una migración. Poner el
// texto como DEFAULT del esquema lo habría congelado en cada fila el día del ALTER, y
// corregir una errata habría costado un backfill.
//
// QUÉ DICE Y QUÉ NO. Dice que el mensaje LLEGÓ y que se está trabajando en él, que es
// lo único que el sistema sabe con certeza en ese instante. No promete un plazo exacto
// —el presupuesto del plan es «primer borrador en < 5 min» (T6.1) y «unos minutos» es
// lo más preciso que se puede decir sin mentir el día que el pipeline vaya lento—, no
// pide nada al cliente y no le enseña ninguna opción: no es un menú y no debe parecerlo.
//
// 🔴 LO ESCRIBE ESTA CONSTANTE, NO EL LLM (INV-1/INV-2). Es texto fijo del sistema,
// como defaultEscapeMessage o el aviso de sesión pasiva.
const DefaultWelcomeText = "¡Hola! Recibimos tu mensaje y lo estamos procesando. Te respondemos en unos minutos."
