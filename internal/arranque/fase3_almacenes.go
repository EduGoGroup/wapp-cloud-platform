// Copia de internal/bootstrap/arranque/fase3_almacenes.go @ 80807ba (F0 · 05 §6): cablea paquetes VIEJOS,
// salvo acceso (F2, T2.31, conmutar(acceso)) y edge (F3, T3.28, conmutar(edge)), que son
// internal/modulos/{acceso,edge}: un solo gateway, el nuevo, que recibe acceso sin adaptador.
package arranque

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/receipts"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
)

// faseAlmacenes construye el stack de cifrado de PII y TODOS los adaptadores de salida
// del proceso: los ~16 objetos que hablan con PostgreSQL.
//
// # LA REGLA DE ESTA FASE, Y SE REPITE EN CASI TODOS LOS COMENTARIOS DE ABAJO
//
// De cada almacén hay UNO SOLO. No por ahorro de memoria —los repositorios no tienen
// estado que pese— sino porque varios de ellos llevan cipher, y dos instancias con dos
// KeyProviders son DOS ROTACIONES DE KEK que mantener: el día que se rote, la mitad de
// las filas se quedaría atrás sin que nadie lo notara hasta no poder abrirlas. Por eso
// están todos aquí y por eso las fases siguientes reciben el campo del contenedor en
// vez de construirse el suyo.
type faseAlmacenes struct{}

func (faseAlmacenes) nombre() string { return "almacenes" }

// requiere la base. El cifrado de PII lo construye ESTA fase (es su primera línea),
// así que no puede exigirlo.
func (faseAlmacenes) requiere() []string { return []string{"db"} }

func (faseAlmacenes) ejecutar(ctx context.Context, c *contenedor) error {
	// --- Dependencias del Motor que se construyen con fail-fast: el resolver de
	// contactos (cifrado de PII, Plan 011) y el almacén de objetos R2 (Plan 017).
	// Se agrupan para no cargar el arranque con dos ramas de error separadas.
	//
	// 🔴 ES LA PRIMERA LÍNEA DE LA FASE porque casi todo lo de abajo necesita su
	// cipher: el repositorio de FLOTA (desde que `fleet_sessions.self_pn` va cifrado),
	// el de solicitudes, el del comprador, el del puente CRM, el de la credencial LLM
	// y el del hilo del evento. Todos «EL MISMO», que no es una comodidad: es la
	// condición de que haya UNA rotación de KEK.
	//
	// ⚠️ ES EL PRIMER ERROR DE ARRANQUE QUE PUEDE SALIR DE ESTA FASE, y conviene
	// saberlo al depurar. buildFlowRuntimeDeps construye DOS cosas, no una
	// (flows.go): el KeyProvider + FieldCipher, y ADEMÁS el cliente de presign de R2
	// (Plan 017). Y con `WAPP_KEK_PROVIDER=kms` el primero hace una LLAMADA DE RED A
	// GCP KMS al arrancar (ADR-0036 §3: el KMS interviene una vez, en el arranque).
	flowDeps, err := buildFlowRuntimeDeps(ctx, c.cfg, c.db)
	if err != nil {
		return err
	}
	c.flowDeps = flowDeps

	// --- Acuses persistidos (Plan 018 · T10, R11): los MessageReceipt del Edge
	// (Plan 013) se materializan en message_receipts (migración 0022) de forma
	// idempotente, reemplazando el LogReceiptSink log-only. onRecord alimenta la
	// métrica wapp_receipts_total (delivered|read). CERO PII: solo metadatos. ---
	c.receiptSink = receipts.NewSink(receipts.NewPostgresStore(c.db), c.mtx.Receipt)

	// --- Entitlements (ADR-0022) + config de intenciones (Plan 029): el resolver de
	// features (con caché) es el gate de VERDAD del servidor; el store del blob de
	// intents alimenta el push de config y la API /api/v1/intents. El provider ata el
	// kind "intents" al push al conectar (el Gateway queda genérico).
	//
	// 🔴 UN SOLO entResolver EN TODO EL PROCESO. Lo consumen la ventana de captación,
	// el hilo, el gate del puente CRM, la bandeja del operador, el despachador de
	// eventos, el re-análisis y el motor: dos resolvers serían dos cachés con TTL y
	// dos verdades sobre qué tiene contratado un tenant. ---
	c.entResolver = entitlements.NewPostgres(c.db)
	c.intentStore = intentcfg.NewPostgresStore(c.db)

	// --- Diagnóstico remoto (Plan 031 · T5, ADR-0023 capa 3): el store persiste las
	// solicitudes/bundles y el consentimiento por tenant. Se comparte entre el Gateway
	// (recibe el DiagnosticsBundle por el demux) y la API pública (emite el request y
	// sirve la descarga). ---
	c.diagStore = diagnostics.NewPostgres(c.db)

	// 📌 fleetRepo se construye AQUÍ, una sola vez, y lo comparten los cuatro
	// consumidores: el Gateway (WithFleet), el provider de filters (T2.1), las rutas
	// de sesión de la API pública y las de admin. Hasta el Plan 046 · T2.1 había DOS
	// instancias sobre el MISMO *sql.DB; no era un bug (el repo no tiene estado) pero
	// sí una invitación a que mañana lo tuviera y las dos mitades vieran cosas
	// distintas.
	//
	// 🔒 Desde T4.1 SÍ tiene estado que importa: el cipher y el KeyProvider con los
	// que abre y cierra el sobre del self_pn. El logger va enchufado a propósito —es
	// la ÚNICA vía por la que se entera nadie de que un sobre no descifró al servir
	// el listado; sin él, ese fallo sería un campo vacío y ni una línea de log.
	c.fleetRepo = fleet.NewPostgresRepository(c.db, c.flowDeps.cipher, c.flowDeps.kp,
		fleet.WithLogger(c.log))

	// 📌 AQUÍ VIVÍAN LOS DOS BACKFILLS CIFRADOS DEL PLAN 046 (T4.1 y T4.2), y se dice
	// en vez de dejar el hueco: cifraban las filas que todavía tenían el número propio
	// y el nombre del contacto EN CLARO —las anteriores a las migraciones 0068 y 0069—
	// y vaciaban esas dos columnas. Corrían síncronos y bloqueando el arranque, después
	// de las migraciones y antes de que se abriera un solo listener.
	//
	// Se retiraron con la 0070 (T5.4), que BORRA las dos columnas en claro: ya no hay
	// nada que migrar, y su primer SELECT —`WHERE self_pn IS NOT NULL`— habría abortado
	// el arranque con «column does not exist». Van en el MISMO commit por eso: no es
	// limpieza posterior, es la otra mitad de esa migración.
	//
	// 🔴 Lo que se fue con ellos, dicho para que nadie lo busque: la consulta
	// `count(*) WHERE self_pn IS NOT NULL = 0` que acreditaba el saneo. Ya no se puede
	// hacer —la columna que cuenta no existe— y su última ejecución contra UAT quedó
	// anotada en el journal (criterio (c) de T5.4). La garantía pasó de ser un conteo
	// a ser el esquema: no hay dónde escribir un teléfono en claro.

	c.flowStore = flowstore.NewPostgresRepository(c.db)
	c.flowResolver = flowruntime.NewPostgresTenantResolver(c.db)
	c.triggerStore = trigger.NewPostgresStore(c.db)

	// El store de SOLICITUDES lo comparten dos consumidores: el proyector del
	// carrito, que le cuelga la revisión 1 al cerrar (ADR-0031 §3) y le pone la
	// línea de envío (D-041.11), y la API pública, que lee la bandeja. Es el mismo
	// pool y el mismo dominio: dos instancias solo serían dos nombres para lo mismo.
	// Por eso el proyector lo recibe DOS VECES: satisface sus dos puertos —escritor
	// de revisiones y garante del envío— sin que el carrito conozca el store entero.
	//
	// 🔧 DESDE T3.5 EL STORE SÍ LLEVA CIPHER, y eso corrige a medias el párrafo de
	// abajo: el escritor del comprador ya no es «el único componente del dominio que
	// necesita el cipher». La diferencia sigue siendo real y es de ALCANCE — aquél
	// cifra la FILA ENTERA de datos personales del comprador; éste cifra UN CAMPO de
	// nivel 2 dentro del payload de la revisión (el literal del cliente), y deja en
	// claro la interpretación estructurada, que es lo que el negocio cuenta.
	// Comparten keyring, que es lo que evita una tercera rotación.
	c.intakeStore = intakes.NewPostgres(c.db,
		intakes.ConCifraDeLiteral(c.flowDeps.cipher),
		intakes.ConLogDeRetencion(c.log),
	)
	// Los DATOS DEL COMPRADOR van por su propio escritor y no por intakeStore (T4.5,
	// D-041.13): es el único componente del dominio de solicitudes que necesita el
	// cipher de PII, y tenerlo aparte hace que el store normal —que lo consumen la
	// API pública, el notificador y el proyector— no pueda cifrar ni descifrar nada.
	// Reusa el MISMO stack de claves que los contactos (flowDeps.cipher, KEK del
	// keyring versionado del Plan 012): dos ciphers serían dos rotaciones.
	c.buyerDataStore = intakes.NewPostgresBuyerData(c.db, c.flowDeps.cipher)
	// El puente CRM (Plan 042 · Ola 3) reusa el MISMO cipher que buyerDataStore
	// (mismo keyring versionado del Plan 012): el secreto HMAC de
	// tenant_integrations y los datos del comprador comparten el stack de
	// claves, no hay una tercera rotación que gestionar.
	c.integrationsStore = integrations.NewPostgres(c.db, c.flowDeps.cipher)
	// La credencial de la vía LLM API (Plan 044 · T0.3) reusa EL MISMO cipher, y
	// por tanto el mismo keyring versionado del Plan 012, que buyerDataStore y el
	// puente CRM: tres sobres distintos, una sola rotación que gestionar. Es lo
	// que hace que meter tenant_llm en el censo de rekeyTargets (rekey.go) baste
	// para que su clave rote con todo lo demás.
	c.tenantLLMStore = tenantllm.NewPostgres(c.db, c.flowDeps.cipher)
	// Los avisos de degradación al dueño (Plan 044 · T1.5-4, REQ-38). NO lleva
	// cipher, y esa ausencia es una afirmación: en owner_degradation_notices no hay
	// NADA que cifrar porque no hay nada sensible (INV-6 — la tabla no tiene una
	// sola columna donde quepa una frase). El día que alguien tenga que añadir un
	// cipher aquí, lo que ha pasado es que se coló una columna que no debía existir.
	//
	// 🔧 Y SU LLAMANTE está en la fase de captación, desde T1.6-4: el selector de vía
	// envuelve a los DOS adaptadores con el decorador que escribe el aviso
	// (llmvia/notify.go), y el primer productor de fallos reales es el adelanto de
	// ventana por pull. Ventana de dedupe: la de plataforma (0 ⇒
	// degradation.VentanaPorDefecto).
	c.degradationStore = degradation.NewPostgres(c.db)
	c.degradationNotifier = degradation.NewNotifier(c.degradationStore, 0)
	// LA COLA DEL PIPELINE DE CAPTACIÓN (Plan 044 · Ola 1, migración 0072). Sin
	// cipher a propósito, y no es un olvido NI CADUCÓ CON T1.4: lo que llega a
	// PutSourceText son bytes YA cifrados por el compositor. Un store sin cipher no
	// puede escribir literal aunque alguien se lo pida, y eso es lo que sostiene
	// D-044.26 por construcción.
	//
	// 🔴 UNA SOLA INSTANCIA PARA TRES PUERTOS: la cola en línea con el mensaje
	// (`intake.JobStore`), la máquina de estados del worker (`intake.PipelineStore`,
	// machine.go) y la puerta del re-análisis. Dos instancias serían dos pools contra
	// la misma base sin ganar nada.
	c.intakeJobStore = intake.NewPostgres(c.db)
	// El almacén del EVENTO conversacional (Plan 043 · Ola 1) reusa el MISMO cipher
	// que los contactos y los datos del comprador: el historial del evento guarda
	// texto literal del cliente y va cifrado con el keyring versionado del Plan 012.
	// Una tercera instancia de cipher sería una tercera rotación que gestionar.
	//
	// Tiene tres consumidores en tres fases distintas: el compositor del literal y la
	// puerta de /reanalyze (captación), el despachador y el motor (flujos) y la
	// bandeja de eventos de la API pública (transporte).
	c.eventStore = events.NewStore(c.db, c.flowDeps.cipher)

	c.marca("cipher", "almacenes")
	return nil
}
