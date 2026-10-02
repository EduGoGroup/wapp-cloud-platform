// Casos de fusión (R-16, R-17, N-03): el canónico es el contacto más antiguo, el huérfano desaparece y
// su estado conversacional migra. Incluye la pareja de contactos que los casos fusionan.
// Aquí crece lo que dependa de Estado: si Estado gana una marca (D-F1-7), este es el sitio donde se
// distingue qué contenido sobrevive en el conflicto de una sesión.

package contacttest

import (
	"fmt"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
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
