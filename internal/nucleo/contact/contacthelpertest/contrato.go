// Package contacthelpertest es la suite de contrato del puerto contact.Resolver y sus dobles.
// Ningún código de producción lo importa (mismo criterio que internal/gateway/fleet/fleettest).
//
// A diferencia de fleettest, este paquete SÍ importa "testing" (las firmas de Contrato y de
// Estado lo piden), así que arrastra el runtime de test: una razón más para que solo lo
// importen tests. Lo consumen los tests de cada implementación del puerto —MemoryResolver en
// unitario, PostgresResolver en los procesos de F9—: cada una monta su Resolver, dos tenants y un
// Estado, y la suite afirma una vez, para todas, lo que el puerto promete.
//
// Un fichero por tema; los de casos y de apoyo terminan en _contrato.go:
//   - contrato.go: la entrada. Montaje, Estado, Contrato y la tabla de casos (casos).
//   - resolve_contrato.go: los casos de Resolve (crear, reutilizar, atar, repetidas, sin refs).
//   - isolation_contrato.go: el aislamiento por tenant y por kind.
//   - merge_contrato.go: la fusión de contactos y la migración de su estado, con la pareja de apoyo.
//   - destination_contrato.go: los casos de Destino.
//   - concurrency_contrato.go: el get-or-create concurrente.
//   - pushname_contrato.go: el push_name no cambia la identidad.
//   - fixtures_contrato.go: los valores de las refs y las ayudas para construirlas y resolverlas.
//   - assertions_contrato.go: las aserciones compartidas.
//   - estado.go: EstadoMemoria, el doble en memoria del flow_state que la fusión migra.
//
// Para añadir un caso: escribe su función en el fichero de su tema, añade su fila a la tabla de
// casos (con su regla R-xx o N-xx) y apóyate en las ayudas de fixtures y assertions.
package contacthelpertest

import (
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
// observar a quién pertenece y qué fila es. Lo implementa EstadoMemoria (que además es el
// contact.StateMigrator de la memoria) y, para Postgres, un adaptador sobre public.flow_state.
//
// El estado se identifica por (tenant, sesión) y pertenece a un contact_id. Antes de una fusión
// pueden coexistir DOS dueños de la misma sesión, el huérfano y el canónico: es el conflicto de
// R-17. Después de la fusión tiene que quedar uno solo, el canónico.
//
// La marca (mark, D-F1-7) es lo que deja ver QUÉ fila sobrevivió, no solo de quién es: una cadena
// opaca que la implementación guarda con la fila de estado al sembrarla y devuelve sin tocar al
// observarla. No significa nada para el puerto ni para la implementación, que no la interpreta
// ni la valida más allá de exigir que no venga vacía. En la memoria va junto al contact_id; en
// Postgres irá en una columna de la propia fila de public.flow_state (cuál, lo decide su adaptador
// en T1.13). Con ella la suite distingue «se conserva el estado del canónico» de «se re-clava el
// del huérfano en el canónico», que dejan el mismo dueño y marcas distintas (R-17). Fuera de eso,
// Estado no expone qué contiene el estado.
type Estado interface {
	// Sembrar da estado a la sesión sessionID del tenant tenantID, con contactID como dueño (un
	// contacto que ya existe en ese tenant) y mark como marca de esa fila. Sembrar dos contactos
	// distintos en la misma sesión los deja a los dos como dueños, cada uno con su marca. Repetir
	// un (tenant, sesión, contacto) ya sembrado no cambia nada, tampoco la marca: se queda la de
	// la primera siembra (como un INSERT … ON CONFLICT DO NOTHING sobre la clave de flow_state).
	// Si no puede, o si alguno de los argumentos viene vacío, falla el test t.
	Sembrar(t *testing.T, tenantID, sessionID, contactID, mark string)
	// Dueno devuelve el contact_id dueño del estado de la sesión sessionID del tenant tenantID y
	// la marca de esa fila tal como se sembró, y ok=false (con contactID y mark vacíos) si la
	// sesión no tiene estado. Si tuviera más de un dueño (una fusión que no resolvió el
	// conflicto) la implementación falla el test t en vez de elegir uno: es justamente el defecto
	// que la suite quiere ver.
	Dueno(t *testing.T, tenantID, sessionID string) (contactID, mark string, ok bool)
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
//     forzar sin controlar el reloj de la implementación. Hoy NO lo cubre ningún test. En la
//     memoria no puede cubrirse: cada alta recibe un número de orden propio y el empate nunca
//     llega a darse (lo dice MemoryResolver.Resolve). En Postgres lo cubrirá el test unitario
//     de la función pura que elige el canónico (pickCanonicalDB), que nace con el verde de
//     repository_postgres.go (T1.11): hasta entonces es una promesa del puerto sin aserción.
//   - Cuál de varias refs direccionables del mismo kind devuelve Destino (dos teléfonos en un
//     contacto): la memoria da la primera atada y Postgres lee sin ORDER BY, así que el puerto
//     solo promete el kind. Los casos de Destino usan contactos con una sola ref por kind.
//   - Una Ref vacía o no normalizable: la precondición del puerto es que cada Ref venga de
//     contact.NewRef, que no puede construirlas. Resolve las cuenta como una ref más, pero la
//     suite no las ejercita: lo fija el test propio de MemoryResolver, y qué hace Postgres con
//     una ref así después de contarla solo se ve contra una base real.
//   - Que el nombre que llega tarde se selle (R-27): un push_name que llega después a un contacto
//     creado sin él se guarda cifrado en su sobre, y ese sobre no se ve por el puerto (Resolve
//     solo devuelve el contact_id). Lo afirma el proceso «entrante a respuesta» de F9 (P3, paso 6),
//     como R-28 y R-29.
//
// Qué fila de estado sobrevive al conflicto de una sesión (R-17) SÍ lo afirma, por la marca de
// Estado: tras la fusión la sesión en conflicto conserva la marca del canónico, no la del huérfano,
// y las sesiones sin conflicto del huérfano pasan al canónico con la marca del huérfano.
//
// Con memoria, los dos casos de fusión de estado (Fusion_MigraElEstadoDelHuerfano y
// Fusion_ConflictoConservaElCanonico) prueban que MemoryResolver llama bien al migrador: la
// política de conflicto que ahí se ve es la de EstadoMemoria, no la del resolver. Con Postgres
// prueban el SQL de la fusión, que migra el estado dentro de su misma transacción.
func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("contacthelpertest.Contrato: nuevo es nil; hace falta una función que devuelva un Montaje")
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
