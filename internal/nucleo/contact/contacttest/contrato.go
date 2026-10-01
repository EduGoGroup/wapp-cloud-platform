// Package contacttest es la suite de contrato del puerto contact.Resolver y sus dobles.
// Ningún código de producción lo importa (mismo criterio que internal/gateway/fleet/fleettest).
//
// A diferencia de fleettest, este paquete SÍ importa "testing" (las firmas de Contrato y de
// Estado lo piden), así que arrastra el runtime de test: una razón más para que solo lo
// importen tests. Lo consumen los tests de cada implementación del puerto —MemoryResolver en
// unitario, PostgresResolver en los procesos de F9—: cada una monta su Resolver, dos tenants y un
// Estado, y la suite afirma una vez, para todas, lo que el puerto promete.
//
// Son dos ficheros: contrato.go (la suite, con Montaje y Estado) y estado.go (EstadoMemoria, el
// doble en memoria del flow_state que la fusión migra).
package contacttest

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/google/uuid"
)

// Montaje es lo que cada implementación entrega a la suite para UN caso: el Resolver bajo
// prueba, dos tenants y el flow_state que la fusión migra. Tiene que venir limpio —sin
// contactos ni estado— porque los casos reutilizan los mismos valores de ref: Contrato llama a
// nuevo una vez por caso y no se limpia nada entre llamadas.
type Montaje struct {
	// Resolver es la implementación bajo prueba.
	Resolver contact.Resolver
	// TenantA y TenantB son dos tenants válidos y distintos: UUID bien formados (con Postgres,
	// filas reales de public.tenants, porque public.contacts los referencia por clave foránea).
	// El contrato solo promete UUID bien formados: con uno mal formado Postgres devuelve un
	// error de parseo y la memoria, ErrContactNotFound, así que la suite no los usa.
	TenantA, TenantB string
	// Estado es el flow_state que la fusión migra y que la suite siembra y observa. Es
	// obligatorio. Con la memoria es el MISMO EstadoMemoria que se le pasa a
	// contact.NewMemoryResolver como migrador; con Postgres, un adaptador sobre public.flow_state
	// de la misma base.
	Estado Estado
}

// Estado es la vista que la suite necesita del estado conversacional (flow_state): sembrarlo y
// observar a quién pertenece. Lo implementa EstadoMemoria (que además es el
// contact.StateMigrator de la memoria) y, para Postgres, un adaptador sobre public.flow_state.
//
// El estado se identifica por (tenant, sesión) y pertenece a un contact_id. Antes de una fusión
// pueden coexistir DOS dueños de la misma sesión, el huérfano y el canónico: es el conflicto de
// R-17. Después de la fusión tiene que quedar uno solo, el canónico. Estado solo expone quién es
// el dueño, no qué contiene el estado.
type Estado interface {
	// Sembrar da estado a la sesión sessionID del tenant tenantID, con contactID como dueño
	// (un contacto que ya existe en ese tenant). Sembrar dos contactos distintos en la misma
	// sesión los deja a los dos como dueños; repetir lo ya sembrado no cambia nada. Si no puede,
	// falla el test t.
	Sembrar(t *testing.T, tenantID, sessionID, contactID string)
	// Dueno devuelve el contact_id dueño del estado de la sesión sessionID del tenant tenantID,
	// y ok=false si la sesión no tiene estado. Si tuviera más de un dueño (una fusión que no
	// resolvió el conflicto) la implementación falla el test t en vez de elegir uno: es
	// justamente el defecto que la suite quiere ver.
	Dueno(t *testing.T, tenantID, sessionID string) (contactID string, ok bool)
}

// Contrato ejecuta todas las promesas de contact.Resolver contra la implementación que
// devuelve nuevo, con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta
// nada: un caso que no aplica a una implementación es un defecto del puerto, no de la suite.
//
// Son 19 casos, uno por promesa de Resolve y de Destino que dos implementaciones pueden
// compartir; la tabla de casos de la función casos dice la regla (R-xx, N-xx) de cada uno.
// Todos usan UUID bien formados y los tenants del Montaje, y construyen las refs con
// contact.NewRef.
//
// Lo que la suite NO afirma, a propósito:
//   - Qué push_name sobrevive si llegan varios (R-28): Postgres conserva el primero y la memoria
//     el último, y esa divergencia está aceptada. Solo afirma que el nombre nunca cambia el
//     contact_id (PushName_NoCambiaLaIdentidad).
//   - La ausencia de deadlock 40P01 (R-29): es del proceso «entrante a respuesta» de F9.
//   - El desempate por id menor cuando dos contactos tienen la misma antigüedad: no se puede
//     forzar sin controlar el reloj de la implementación. Lo cubren los tests de cada una.
//   - Una Ref vacía o no normalizable: la precondición del puerto es que cada Ref venga de
//     contact.NewRef, que no puede construirlas. Resolve las cuenta como una ref más, pero la
//     suite no las ejercita.
//   - Cuál de los dos contenidos de estado sobrevive en el conflicto de una sesión (R-17).
//     Estado solo expone QUIÉN es el dueño, no el contenido: «se conserva el estado del canónico»
//     y «se conserva el del huérfano, re-clavado en el canónico» dejan el mismo dueño. Lo que la
//     suite afirma es lo observable: tras el conflicto la sesión tiene un solo dueño, el
//     canónico, y las sesiones sin conflicto del huérfano migran igualmente. Distinguir el
//     contenido pediría que Sembrar y Dueno llevaran una marca.
//
// Con memoria, los dos casos de fusión de estado (Fusion_MigraElEstadoDelHuerfano y
// Fusion_ConflictoConservaElCanonico) prueban que MemoryResolver llama bien al migrador: la
// política de conflicto que ahí se ve es la de EstadoMemoria, no la del resolver. Con Postgres
// prueban el SQL de la fusión, que migra el estado dentro de su misma transacción.
func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("contacttest.Contrato: nuevo es nil; hace falta una función que devuelva un Montaje")
	}
	for _, c := range casos() {
		t.Run(c.nombre, func(t *testing.T) {
			m := nuevo(t)
			validarMontaje(t, m)
			c.corre(t, m)
		})
	}
}

// caso es una promesa del puerto: su nombre (el del t.Run) y la función que la afirma.
type caso struct {
	nombre string
	corre  func(t *testing.T, m Montaje)
}

// casos es la tabla de la suite: un caso, una función. El comentario de cada fila es la regla del
// puerto que fija (las R-xx y N-xx son las de plan/F1-nucleo-contact/diseno.md §4).
func casos() []caso {
	return []caso{
		{"SinRefs_ErrNoRefs", casoSinRefs},                                                 // R-18
		{"RefNueva_CreaID", casoRefNuevaCreaID},                                            // R-12
		{"MismaRef_MismoID", casoMismaRefMismoID},                                          // R-13
		{"RefRepetidaEnLaEntrada_UnID", casoRefRepetidaEnLaEntrada},                        // dedup de la entrada
		{"DosRefsJuntas_UnID", casoDosRefsJuntas},                                          // R-14
		{"RefNuevaJuntoAExistente_SeAta", casoRefNuevaJuntoAExistente},                     // R-15
		{"MismoValorOtroKind_OtroContacto", casoMismoValorOtroKind},                        // N-02
		{"MismaRefOtroTenant_OtroContacto", casoMismaRefOtroTenant},                        // N-01
		{"Fusion_CanonicoElMasAntiguo_ElHuerfanoDesaparece", casoFusionCanonicoMasAntiguo}, // R-16, N-03
		{"Fusion_MigraElEstadoDelHuerfano", casoFusionMigraElEstado},                       // R-16
		{"Fusion_ConflictoConservaElCanonico", casoFusionConflictoConservaElCanonico},      // R-17
		{"Destino_PrefiereTelefono", casoDestinoPrefiereTelefono},                          // R-19
		{"Destino_SoloLID", casoDestinoSoloLID},                                            // R-19
		{"Destino_SoloUsername_ErrNoDestino", casoDestinoSoloUsername},                     // R-20
		{"Destino_Inexistente", casoDestinoInexistente},                                    // R-21
		{"Destino_OtroTenant", casoDestinoOtroTenant},                                      // N-01, R-21
		{"Destino_DevuelveElValorNormalizado", casoDestinoDevuelveElValorNormalizado},      // R-23
		{"Concurrente_MismaRef_UnSoloID", casoConcurrenteMismaRefUnSoloID},                 // R-32
		{"PushName_NoCambiaLaIdentidad", casoPushNameNoCambiaLaIdentidad},                  // N-04
	}
}

// Los valores de las refs de los casos. Cada caso parte de un Montaje limpio, así que pueden
// repetirse de un caso a otro. Los LID y los números son válidos para contact.NewRef.
const (
	numeroAna   = "573001112233"
	numeroBeto  = "573004445566"
	numeroCarla = "573007778899"
	lidAna      = "88887777"
	lidBeto     = "99991111"
	lidCarla    = "55554444"
	usuarioAna  = "juanito"
	usuarioBeto = "betico"
)

// Las sesiones del estado conversacional que siembran los casos de fusión.
const (
	sesionUno          = "sesion-uno"
	sesionDos          = "sesion-dos"
	sesionDelCanonico  = "sesion-del-canonico"
	sesionConflicto    = "sesion-en-conflicto"
	sesionSoloHuerfano = "sesion-solo-huerfano"
)

// rondasFusion es cuántas parejas independientes funde Fusion_CanonicoElMasAntiguo. Los id de
// contacto son UUID aleatorios: con UNA sola fusión, una implementación que eligiera el canónico
// por id menor acertaría la mitad de las veces. Con ocho rondas la acierta por azar una de cada
// 256 y la suite deja de ser un cara o cruz; la mitad de las rondas, además, crea primero el LID,
// para cazar a quien prefiera el contacto del teléfono.
const rondasFusion = 8

// gorutinasConcurrentes es cuántas llamadas simultáneas lanza Concurrente_MismaRef_UnSoloID.
const gorutinasConcurrentes = 16

// casoSinRefs (R-18): sin ninguna ref, tras deduplicar, Resolve devuelve ErrNoRefs y contactID
// "", se llame con nil o con []Ref{} y venga o no con push_name (el nombre solo no crea nada).
func casoSinRefs(t *testing.T, m Montaje) {
	listas := []struct {
		nombre string
		refs   []contact.Ref
	}{
		{"nil", nil},
		{"[]Ref{}", []contact.Ref{}},
	}
	for _, l := range listas {
		for _, nombre := range []string{"", "Ana"} {
			id, err := m.Resolver.Resolve(t.Context(), m.TenantA, l.refs, nombre)
			if !errors.Is(err, contact.ErrNoRefs) {
				t.Errorf("Resolve(refs %s, push_name %q): error %v; quiere contact.ErrNoRefs", l.nombre, nombre, err)
			}
			if id != "" {
				t.Errorf("Resolve(refs %s, push_name %q) con error devolvió contact_id %q; quiere \"\"", l.nombre, nombre, id)
			}
		}
	}
}

// casoRefNuevaCreaID (R-12): una ref desconocida crea un contact_id nuevo (un UUID), le ata la
// ref y no se confunde con el de otra ref nueva. Lo único que muestra por el puerto que la ref
// quedó atada es Destino.
func casoRefNuevaCreaID(t *testing.T, m Montaje) {
	ana, beto := refTel(t, numeroAna), refTel(t, numeroBeto)

	idAna := resolverOK(t, m, m.TenantA, ana)
	idBeto := resolverOK(t, m, m.TenantA, beto)

	distintoID(t, "dos refs nuevas distintas", idAna, idBeto)
	exigirDestino(t, m, m.TenantA, idAna, ana)
	exigirDestino(t, m, m.TenantA, idBeto, beto)
}

// casoMismaRefMismoID (R-13): el resultado es determinista por (tenant, ref): la misma ref da
// siempre el mismo contact_id, también escrita de otra forma (NewRef la normaliza a la misma
// Ref), y otra ref no se mezcla con ella.
func casoMismaRefMismoID(t *testing.T, m Montaje) {
	ana := refTel(t, numeroAna)
	id := resolverOK(t, m, m.TenantA, ana)

	for i := range 4 {
		mismoID(t, fmt.Sprintf("llamada %d con la misma ref", i+2), resolverOK(t, m, m.TenantA, ana), id)
	}

	otraForma := refTel(t, "+57 (300) 111-2233")
	if otraForma != ana {
		t.Fatalf("precondición: contact.NewRef debía normalizar el mismo número a la misma Ref: %+v vs %+v", otraForma, ana)
	}
	mismoID(t, "el mismo número con otro formato", resolverOK(t, m, m.TenantA, otraForma), id)
	distintoID(t, "otro número", resolverOK(t, m, m.TenantA, refTel(t, numeroBeto)), id)
}

// casoRefRepetidaEnLaEntrada: una ref repetida en la entrada cuenta una vez. Una lista con solo
// repetidas no es una lista vacía (no da ErrNoRefs) y crea un único contacto; repetida junto a
// una existente y a una nueva, tampoco cambia el resultado.
func casoRefRepetidaEnLaEntrada(t *testing.T, m Montaje) {
	ana, lid := refTel(t, numeroAna), refLID(t, lidAna)

	id := resolverOK(t, m, m.TenantA, ana, ana, ana)

	mismoID(t, "la ref sola tras crearla repetida", resolverOK(t, m, m.TenantA, ana), id)
	exigirDestino(t, m, m.TenantA, id, ana)
	mismoID(t, "LID nuevo repetido junto al teléfono existente", resolverOK(t, m, m.TenantA, lid, ana, lid), id)
	mismoID(t, "el LID solo", resolverOK(t, m, m.TenantA, lid), id)
}

// casoDosRefsJuntas (R-14): dos refs del mismo contacto en una llamada dan un solo contact_id,
// y cada una por separado, y las dos en otro orden, vuelven a dar ese mismo.
func casoDosRefsJuntas(t *testing.T, m Montaje) {
	tel, lid := refTel(t, numeroAna), refLID(t, lidAna)

	id := resolverOK(t, m, m.TenantA, tel, lid)

	mismoID(t, "el teléfono solo", resolverOK(t, m, m.TenantA, tel), id)
	mismoID(t, "el LID solo", resolverOK(t, m, m.TenantA, lid), id)
	mismoID(t, "las dos, en el otro orden", resolverOK(t, m, m.TenantA, lid, tel), id)
}

// casoRefNuevaJuntoAExistente (R-15): con un solo contacto existente entre las refs, Resolve
// reutiliza su contact_id y ata las que faltan, esté la nueva delante o detrás.
func casoRefNuevaJuntoAExistente(t *testing.T, m Montaje) {
	tel, lid, user := refTel(t, numeroAna), refLID(t, lidAna), refUsuario(t, usuarioAna)

	id := resolverOK(t, m, m.TenantA, tel)

	mismoID(t, "teléfono existente + LID nuevo", resolverOK(t, m, m.TenantA, tel, lid), id)
	mismoID(t, "el LID, atado, solo", resolverOK(t, m, m.TenantA, lid), id)
	mismoID(t, "username nuevo delante del LID existente", resolverOK(t, m, m.TenantA, user, lid), id)
	mismoID(t, "el username, atado, solo", resolverOK(t, m, m.TenantA, user), id)
}

// casoMismoValorOtroKind (N-02): el kind es parte de la clave de dedup. El mismo valor como
// phone_e164, wa_lid y wa_username son tres contactos distintos, y cada ref sigue resolviendo
// al suyo.
func casoMismoValorOtroKind(t *testing.T, m Montaje) {
	const valor = "88887777"
	refs := []contact.Ref{
		nuevaRef(t, contact.KindPhoneE164, valor),
		nuevaRef(t, contact.KindWALID, valor),
		nuevaRef(t, contact.KindWAUsername, valor),
	}
	ids := make([]string, len(refs))
	for i, r := range refs {
		ids[i] = resolverOK(t, m, m.TenantA, r)
	}
	for i := range refs {
		for j := i + 1; j < len(refs); j++ {
			distintoID(t, fmt.Sprintf("el valor %s como %s y como %s", valor, refs[i].Kind, refs[j].Kind), ids[i], ids[j])
		}
	}
	for i, r := range refs {
		mismoID(t, "la ref "+r.Kind+" otra vez", resolverOK(t, m, m.TenantA, r), ids[i])
	}
}

// casoMismaRefOtroTenant (N-01): las mismas refs en dos tenants son dos contactos distintos, y
// nada se funde a través de un tenant: el LID que solo existe en B no se mezcla con el teléfono
// de A cuando llegan juntos en A.
func casoMismaRefOtroTenant(t *testing.T, m Montaje) {
	tel, lid := refTel(t, numeroAna), refLID(t, lidAna)

	idA := resolverOK(t, m, m.TenantA, tel, lid)
	idB := resolverOK(t, m, m.TenantB, tel, lid)

	distintoID(t, "las mismas refs en dos tenants", idA, idB)
	mismoID(t, "en A, el teléfono solo", resolverOK(t, m, m.TenantA, tel), idA)
	mismoID(t, "en B, el LID solo", resolverOK(t, m, m.TenantB, lid), idB)

	otroTel, otroLID := refTel(t, numeroBeto), refLID(t, lidBeto)
	telEnA := resolverOK(t, m, m.TenantA, otroTel)
	lidEnB := resolverOK(t, m, m.TenantB, otroLID)
	mismoID(t, "en A, el teléfono junto a un LID que solo existe en B", resolverOK(t, m, m.TenantA, otroTel, otroLID), telEnA)
	mismoID(t, "en B, el LID sigue siendo suyo", resolverOK(t, m, m.TenantB, otroLID), lidEnB)
	distintoID(t, "el contacto de A y el de B", telEnA, lidEnB)
}

// casoFusionCanonicoMasAntiguo (R-16, N-03): cuando las refs de una llamada pertenecen a varios
// contactos distintos, Resolve los funde en el más antiguo. Las refs del huérfano pasan al
// canónico y el huérfano deja de existir (Destino de su id da ErrContactNotFound). Se prueba
// con parejas teléfono+LID (rondasFusion veces, alternando cuál se crea primero) y con tres
// contactos a la vez.
//
// La antigüedad se garantiza por construcción: se crea primero un contacto y después el otro,
// sin pausa. El resultado es robusto sin reloj: la memoria ordena por orden de alta y Postgres,
// por created_at, y dos transacciones consecutivas no empatan en el microsegundo.
func casoFusionCanonicoMasAntiguo(t *testing.T, m Montaje) {
	for ronda := range rondasFusion {
		p := crearPareja(t, m, m.TenantA, ronda, ronda%2 == 0)
		donde := fmt.Sprintf("ronda %d", ronda)

		canonico := resolverOK(t, m, m.TenantA, p.refsHuerfanoPrimero()...)

		mismoID(t, donde+": canónico de la fusión", canonico, p.antiguo())
		mismoID(t, donde+": la ref del teléfono tras la fusión", resolverOK(t, m, m.TenantA, p.tel), canonico)
		mismoID(t, donde+": la ref del LID tras la fusión", resolverOK(t, m, m.TenantA, p.lid), canonico)
		exigirNoEncontrado(t, m, m.TenantA, p.huerfano(), donde+": Destino del huérfano")
		exigirDestino(t, m, m.TenantA, canonico, p.tel)
	}
	fusionDeTres(t, m)
}

// fusionDeTres funde tres contactos creados uno tras otro (teléfono, LID y username) con las refs
// en orden inverso al de alta: el canónico es el primero que se creó, no el primero de la lista.
func fusionDeTres(t *testing.T, m Montaje) {
	tel, lid, user := refTel(t, numeroAna), refLID(t, lidAna), refUsuario(t, usuarioAna)
	idTel := resolverOK(t, m, m.TenantA, tel)
	idLid := resolverOK(t, m, m.TenantA, lid)
	idUser := resolverOK(t, m, m.TenantA, user)
	if idTel == idLid || idTel == idUser || idLid == idUser {
		t.Fatalf("precondición: tres refs distintas debían dar tres contactos: %q %q %q", idTel, idLid, idUser)
	}

	canonico := resolverOK(t, m, m.TenantA, user, lid, tel)

	mismoID(t, "fusión de tres: canónico", canonico, idTel)
	mismoID(t, "fusión de tres: el LID", resolverOK(t, m, m.TenantA, lid), canonico)
	mismoID(t, "fusión de tres: el username", resolverOK(t, m, m.TenantA, user), canonico)
	exigirNoEncontrado(t, m, m.TenantA, idLid, "fusión de tres: Destino del primer huérfano")
	exigirNoEncontrado(t, m, m.TenantA, idUser, "fusión de tres: Destino del segundo huérfano")
	exigirDestino(t, m, m.TenantA, canonico, tel)
}

// casoFusionMigraElEstado (R-16): el estado conversacional del huérfano, en todas sus sesiones,
// pasa al canónico. El del propio canónico queda donde estaba y el de otro tenant, aunque sea
// de un contacto con la misma ref y de una sesión con el mismo nombre, no se toca.
func casoFusionMigraElEstado(t *testing.T, m Montaje) {
	p := crearPareja(t, m, m.TenantA, 0, true)
	enOtroTenant := resolverOK(t, m, m.TenantB, p.tel)
	m.Estado.Sembrar(t, m.TenantA, sesionUno, p.huerfano())
	m.Estado.Sembrar(t, m.TenantA, sesionDos, p.huerfano())
	m.Estado.Sembrar(t, m.TenantA, sesionDelCanonico, p.antiguo())
	m.Estado.Sembrar(t, m.TenantB, sesionUno, enOtroTenant)

	canonico := resolverOK(t, m, m.TenantA, p.refsHuerfanoPrimero()...)

	mismoID(t, "canónico de la fusión", canonico, p.antiguo())
	exigirDueno(t, m, m.TenantA, sesionUno, canonico)
	exigirDueno(t, m, m.TenantA, sesionDos, canonico)
	exigirDueno(t, m, m.TenantA, sesionDelCanonico, canonico)
	exigirDueno(t, m, m.TenantB, sesionUno, enOtroTenant)
}

// casoFusionConflictoConservaElCanonico (R-17): si el canónico y el huérfano tienen estado en
// la MISMA sesión, tras la fusión la sesión tiene un único dueño, el canónico (Dueno falla el
// test si quedaran los dos); y el conflicto de una sesión no arrastra a las demás: la sesión que
// solo tenía el huérfano sí migra. Qué contenido sobrevive no lo ve Estado (ver Contrato).
func casoFusionConflictoConservaElCanonico(t *testing.T, m Montaje) {
	p := crearPareja(t, m, m.TenantA, 0, true)
	m.Estado.Sembrar(t, m.TenantA, sesionConflicto, p.antiguo())
	m.Estado.Sembrar(t, m.TenantA, sesionConflicto, p.huerfano())
	m.Estado.Sembrar(t, m.TenantA, sesionSoloHuerfano, p.huerfano())

	canonico := resolverOK(t, m, m.TenantA, p.refsHuerfanoPrimero()...)

	mismoID(t, "canónico de la fusión", canonico, p.antiguo())
	exigirDueno(t, m, m.TenantA, sesionConflicto, canonico)
	exigirDueno(t, m, m.TenantA, sesionSoloHuerfano, canonico)
}

// casoDestinoPrefiereTelefono (R-19): entre las refs direccionables, el teléfono gana al LID,
// sea cual sea el orden de la entrada y el orden en que las refs se ataron, y un wa_username
// (que no es direccionable) no se lo quita.
func casoDestinoPrefiereTelefono(t *testing.T, m Montaje) {
	// Las dos en una llamada, con el LID delante.
	telAna := refTel(t, numeroAna)
	idAna := resolverOK(t, m, m.TenantA, refLID(t, lidAna), telAna)
	exigirDestino(t, m, m.TenantA, idAna, telAna)

	// El contacto nace por LID y el teléfono se ata después: antes solo hay LID.
	lidDeBeto, telDeBeto := refLID(t, lidBeto), refTel(t, numeroBeto)
	idBeto := resolverOK(t, m, m.TenantA, lidDeBeto)
	exigirDestino(t, m, m.TenantA, idBeto, lidDeBeto)
	mismoID(t, "el teléfono se ata al contacto del LID", resolverOK(t, m, m.TenantA, lidDeBeto, telDeBeto), idBeto)
	exigirDestino(t, m, m.TenantA, idBeto, telDeBeto)

	// Con un username de por medio.
	telCarla := refTel(t, numeroCarla)
	idCarla := resolverOK(t, m, m.TenantA, refUsuario(t, usuarioBeto), refLID(t, lidCarla), telCarla)
	exigirDestino(t, m, m.TenantA, idCarla, telCarla)
}

// casoDestinoSoloLID (R-19): sin teléfono, el destino es el LID. wa_username figura antes que
// wa_lid en el orden de preferencia pero hoy no es direccionable, así que en la práctica se
// degrada a wa_lid: un contacto con username y LID da el LID.
func casoDestinoSoloLID(t *testing.T, m Montaje) {
	lid := refLID(t, lidAna)
	id := resolverOK(t, m, m.TenantA, lid)
	exigirDestino(t, m, m.TenantA, id, lid)

	lidConUsername := refLID(t, lidBeto)
	idConUsername := resolverOK(t, m, m.TenantA, refUsuario(t, usuarioAna), lidConUsername)
	exigirDestino(t, m, m.TenantA, idConUsername, lidConUsername)
}

// casoDestinoSoloUsername (R-20): un contacto que existe pero solo tiene un wa_username (aún no
// direccionable) da ErrNoDestino, y no ErrContactNotFound: el contacto sí existe.
func casoDestinoSoloUsername(t *testing.T, m Montaje) {
	id := resolverOK(t, m, m.TenantA, refUsuario(t, usuarioAna))

	err := destinoError(t, m, m.TenantA, id)

	exigirErrorIs(t, err, contact.ErrNoDestino, "Destino de un contacto solo con username")
	if errors.Is(err, contact.ErrContactNotFound) {
		t.Errorf("Destino de un contacto que existe dio también ErrContactNotFound: %v", err)
	}
}

// casoDestinoInexistente (R-21): un contact_id bien formado que no existe en el tenant da
// ErrContactNotFound (no ErrNoDestino), envuelto con el id entre comillas como promete el
// puerto. Se prueba en un tenant que ya tiene un contacto, para que no sea solo «tenant vacío».
func casoDestinoInexistente(t *testing.T, m Montaje) {
	resolverOK(t, m, m.TenantA, refTel(t, numeroAna))

	id := uuid.NewString()
	err := destinoError(t, m, m.TenantA, id)

	exigirErrorIs(t, err, contact.ErrContactNotFound, "Destino de un contact_id inexistente")
	if errors.Is(err, contact.ErrNoDestino) {
		t.Errorf("Destino de un contact_id inexistente dio también ErrNoDestino: %v", err)
	}
	if !strings.Contains(err.Error(), strconv.Quote(id)) {
		t.Errorf("el error %q no lleva el contact_id entre comillas (%s)", err, strconv.Quote(id))
	}
	exigirNoEncontrado(t, m, m.TenantB, uuid.NewString(), "Destino de otro contact_id inexistente, en el tenant vacío")
}

// casoDestinoOtroTenant (N-01, R-21): un contact_id solo existe dentro del tenant que lo creó;
// pedirlo desde otro tenant da ErrContactNotFound, en los dos sentidos, y en el suyo sí se
// encuentra.
func casoDestinoOtroTenant(t *testing.T, m Montaje) {
	tel, lid := refTel(t, numeroAna), refLID(t, lidBeto)
	idA := resolverOK(t, m, m.TenantA, tel)
	idB := resolverOK(t, m, m.TenantB, lid)

	exigirDestino(t, m, m.TenantA, idA, tel)
	exigirDestino(t, m, m.TenantB, idB, lid)
	exigirNoEncontrado(t, m, m.TenantB, idA, "Destino en B del contacto de A")
	exigirNoEncontrado(t, m, m.TenantA, idB, "Destino en A del contacto de B")
}

// casoDestinoDevuelveElValorNormalizado (R-23): Destino devuelve la ref tal como quedó guardada,
// con el value normalizado y no el crudo con el que llegó. Va de ida y vuelta por el almacén
// (con Postgres, cifrado y descifrado).
func casoDestinoDevuelveElValorNormalizado(t *testing.T, m Montaje) {
	crudos := []struct{ kind, crudo string }{
		{contact.KindPhoneE164, "+57 (300) 111-2233"},
		{contact.KindWALID, "88887777:12@lid"},
	}
	for _, c := range crudos {
		ref := nuevaRef(t, c.kind, c.crudo)
		if ref.Value == c.crudo {
			t.Fatalf("precondición: el valor crudo %q de %s debía diferir del normalizado", c.crudo, c.kind)
		}
		id := resolverOK(t, m, m.TenantA, ref)

		exigirDestino(t, m, m.TenantA, id, ref)
	}
}

// casoConcurrenteMismaRefUnSoloID (R-32): get-or-create atómico y seguro para uso concurrente.
// Dieciséis llamadas simultáneas con la misma ref nueva terminan todas sin error y con UN solo
// contact_id, que además es el que guarda la ref. Corre bien con -race.
func casoConcurrenteMismaRefUnSoloID(t *testing.T, m Montaje) {
	ref := refTel(t, numeroAna)
	ids := make([]string, gorutinasConcurrentes)
	errs := make([]error, gorutinasConcurrentes)
	salida := make(chan struct{})
	var wg sync.WaitGroup
	for i := range gorutinasConcurrentes {
		wg.Go(func() {
			<-salida
			ids[i], errs[i] = m.Resolver.Resolve(t.Context(), m.TenantA, []contact.Ref{ref}, "")
		})
	}
	close(salida)
	wg.Wait()

	for i := range ids {
		if errs[i] != nil {
			t.Fatalf("Resolve concurrente %d: %v", i, errs[i])
		}
	}
	for i, id := range ids {
		mismoID(t, fmt.Sprintf("la llamada concurrente %d frente a la 0", i), id, ids[0])
	}
	if _, err := uuid.Parse(ids[0]); err != nil {
		t.Fatalf("el contact_id concurrente %q no es un UUID: %v", ids[0], err)
	}
	mismoID(t, "la ref después de la ráfaga", resolverOK(t, m, m.TenantA, ref), ids[0])
	exigirDestino(t, m, m.TenantA, ids[0], ref)
}

// casoPushNameNoCambiaLaIdentidad (N-04): el push_name nunca cambia el contact_id: ni al crear,
// ni en llamadas posteriores con otro nombre, con ninguno o con uno no ASCII, ni al atar una ref
// nueva. Dos contactos distintos con el mismo nombre siguen siendo distintos. La suite no afirma
// qué nombre sobrevive (R-28).
func casoPushNameNoCambiaLaIdentidad(t *testing.T, m Montaje) {
	ana := refTel(t, numeroAna)
	id := resolverConNombre(t, m, m.TenantA, "Ana", ana)

	for _, nombre := range []string{"", "Beto", "Ana", "Ñandú 🙂"} {
		mismoID(t, fmt.Sprintf("la misma ref con push_name %q", nombre), resolverConNombre(t, m, m.TenantA, nombre, ana), id)
	}

	lid := refLID(t, lidAna)
	mismoID(t, "una ref nueva atada con otro push_name", resolverConNombre(t, m, m.TenantA, "Carla", ana, lid), id)
	mismoID(t, "esa ref, sola y sin nombre", resolverOK(t, m, m.TenantA, lid), id)

	otro := resolverConNombre(t, m, m.TenantA, "Ana", refTel(t, numeroBeto))
	distintoID(t, "otra ref con el mismo push_name", otro, id)
}

// validarMontaje exige lo que la suite da por hecho de un Montaje: Resolver y Estado presentes y
// dos tenants distintos y con forma de UUID.
func validarMontaje(t *testing.T, m Montaje) {
	t.Helper()
	switch {
	case m.Resolver == nil:
		t.Fatal("Montaje.Resolver es nil")
	case m.Estado == nil:
		t.Fatal("Montaje.Estado es nil: la fusión necesita un flow_state que migrar")
	case m.TenantA == m.TenantB:
		t.Fatalf("Montaje: TenantA y TenantB deben ser distintos y son %q", m.TenantA)
	}
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		if _, err := uuid.Parse(tenant); err != nil {
			t.Fatalf("Montaje: el tenant %q no es un UUID bien formado: %v", tenant, err)
		}
	}
}

// pareja son dos contactos del mismo tenant que describen a la misma persona y aún no se han
// fundido: uno creado por su teléfono y otro por su LID. telPrimero dice cuál de los dos se creó
// antes, que es el más antiguo y, por tanto, el canónico de la fusión.
type pareja struct {
	tel, lid     contact.Ref
	idTel, idLid string
	telPrimero   bool
}

// crearPareja crea, en este orden y con llamadas separadas, los dos contactos de una pareja. Los
// valores dependen de ronda, para que parejas distintas de un mismo tenant no se toquen.
func crearPareja(t *testing.T, m Montaje, tenantID string, ronda int, telPrimero bool) pareja {
	t.Helper()
	p := pareja{
		tel:        refTel(t, fmt.Sprintf("5730011%05d", ronda)),
		lid:        refLID(t, fmt.Sprintf("7770%04d", ronda)),
		telPrimero: telPrimero,
	}
	if telPrimero {
		p.idTel = resolverOK(t, m, tenantID, p.tel)
		p.idLid = resolverOK(t, m, tenantID, p.lid)
	} else {
		p.idLid = resolverOK(t, m, tenantID, p.lid)
		p.idTel = resolverOK(t, m, tenantID, p.tel)
	}
	if p.idTel == p.idLid {
		t.Fatalf("precondición: el teléfono y el LID de la pareja debían ser dos contactos y comparten %q", p.idTel)
	}
	return p
}

// antiguo es el contact_id del contacto que se creó primero: el canónico esperado.
func (p pareja) antiguo() string {
	if p.telPrimero {
		return p.idTel
	}
	return p.idLid
}

// huerfano es el contact_id del contacto que se creó después: el que la fusión hace desaparecer.
func (p pareja) huerfano() string {
	if p.telPrimero {
		return p.idLid
	}
	return p.idTel
}

// refsHuerfanoPrimero son las dos refs con la del huérfano delante: así, quien eligiera el
// canónico por el orden de la entrada se equivocaría.
func (p pareja) refsHuerfanoPrimero() []contact.Ref {
	if p.telPrimero {
		return []contact.Ref{p.lid, p.tel}
	}
	return []contact.Ref{p.tel, p.lid}
}

// nuevaRef construye la Ref de (kind, valor) con contact.NewRef, el único constructor que el
// puerto admite.
func nuevaRef(t *testing.T, kind, valor string) contact.Ref {
	t.Helper()
	r, err := contact.NewRef(kind, valor)
	if err != nil {
		t.Fatalf("contact.NewRef(%q, %q): %v", kind, valor, err)
	}
	return r
}

// refTel es la Ref de un teléfono E.164.
func refTel(t *testing.T, valor string) contact.Ref {
	t.Helper()
	return nuevaRef(t, contact.KindPhoneE164, valor)
}

// refLID es la Ref de un LID de WhatsApp.
func refLID(t *testing.T, valor string) contact.Ref {
	t.Helper()
	return nuevaRef(t, contact.KindWALID, valor)
}

// refUsuario es la Ref de un username de WhatsApp.
func refUsuario(t *testing.T, valor string) contact.Ref {
	t.Helper()
	return nuevaRef(t, contact.KindWAUsername, valor)
}

// resolverOK llama a Resolve sin push_name y exige que salga bien.
func resolverOK(t *testing.T, m Montaje, tenantID string, refs ...contact.Ref) string {
	t.Helper()
	return resolverConNombre(t, m, tenantID, "", refs...)
}

// resolverConNombre llama a Resolve con pushName y exige que salga bien: sin error y con un
// contact_id que sea un UUID.
func resolverConNombre(t *testing.T, m Montaje, tenantID, pushName string, refs ...contact.Ref) string {
	t.Helper()
	id, err := m.Resolver.Resolve(t.Context(), tenantID, refs, pushName)
	if err != nil {
		t.Fatalf("Resolve(tenant %s, refs %+v, push_name %q): %v", tenantID, refs, pushName, err)
	}
	if _, perr := uuid.Parse(id); perr != nil {
		t.Fatalf("Resolve(tenant %s, refs %+v) devolvió %q, que no es un UUID: %v", tenantID, refs, id, perr)
	}
	return id
}

// mismoID falla el test si obtenido no es esperado.
func mismoID(t *testing.T, que, obtenido, esperado string) {
	t.Helper()
	if obtenido != esperado {
		t.Errorf("%s: contact_id %q; quiere %q", que, obtenido, esperado)
	}
}

// distintoID falla el test si a y b son el mismo contact_id.
func distintoID(t *testing.T, que, a, b string) {
	t.Helper()
	if a == b {
		t.Errorf("%s: dieron el mismo contact_id %q; quiere dos distintos", que, a)
	}
}

// exigirDestino exige que Destino del contacto devuelva exactamente la ref quiere.
func exigirDestino(t *testing.T, m Montaje, tenantID, contactID string, quiere contact.Ref) {
	t.Helper()
	got, err := m.Resolver.Destino(t.Context(), tenantID, contactID)
	if err != nil {
		t.Errorf("Destino(tenant %s, %s): %v; quiere %+v", tenantID, contactID, err, quiere)
		return
	}
	if got != quiere {
		t.Errorf("Destino(tenant %s, %s) = %+v; quiere %+v", tenantID, contactID, got, quiere)
	}
}

// destinoError llama a Destino y exige que falle: devuelve el error.
func destinoError(t *testing.T, m Montaje, tenantID, contactID string) error {
	t.Helper()
	ref, err := m.Resolver.Destino(t.Context(), tenantID, contactID)
	if err == nil {
		t.Fatalf("Destino(tenant %s, %s) = %+v sin error; quiere un error", tenantID, contactID, ref)
	}
	return err
}

// exigirErrorIs falla el test si err no es, según errors.Is, el centinela quiere.
func exigirErrorIs(t *testing.T, err, quiere error, que string) {
	t.Helper()
	if !errors.Is(err, quiere) {
		t.Errorf("%s: error %v; quiere %v (errors.Is)", que, err, quiere)
	}
}

// exigirNoEncontrado exige que Destino del contactID en el tenant dé ErrContactNotFound.
func exigirNoEncontrado(t *testing.T, m Montaje, tenantID, contactID, que string) {
	t.Helper()
	exigirErrorIs(t, destinoError(t, m, tenantID, contactID), contact.ErrContactNotFound, que)
}

// exigirDueno exige que el estado de la sesión del tenant pertenezca a quiere.
func exigirDueno(t *testing.T, m Montaje, tenantID, sessionID, quiere string) {
	t.Helper()
	got, ok := m.Estado.Dueno(t, tenantID, sessionID)
	switch {
	case !ok:
		t.Errorf("la sesión %q del tenant %s no tiene estado; quiere el de %q", sessionID, tenantID, quiere)
	case got != quiere:
		t.Errorf("el estado de la sesión %q del tenant %s es de %q; quiere el de %q", sessionID, tenantID, got, quiere)
	}
}
