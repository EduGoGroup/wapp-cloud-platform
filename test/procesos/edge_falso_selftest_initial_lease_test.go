//go:build integracion

package procesos

import (
	"errors"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"
)

// TestArnes_EdgeInitialLease, sin servidor: qué concluye conectar del primer LeaseUpdate de una
// conexión (vigente, revocación, rechazado por el Validator).
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// edgeTestTenant y edgeTestID son el tenant y el id del Edge de los casos de
// TestArnes_EdgeInitialLease (los mismos que usa edgeDePrueba).
const (
	edgeTestTenant = "11111111-1111-4111-8111-111111111111"
	edgeTestID     = "edge-prueba"
)

// edgeIssuedLease es lo que devuelve Issuer.Issue o Issuer.Revoke: el LeaseUpdate y el error de
// emitirlo, tal cual, para dárselo a edgeLeaseCommand.
type edgeIssuedLease struct {
	lu  *cloudlinkv1.LeaseUpdate
	err error
}

// edgeInitialLeaseCase es un caso de TestArnes_EdgeInitialLease: los leases que recibe la conexión,
// en orden, y lo que se espera: la causa del rechazo del primero (nil si conectar lo da por
// bueno), si el Edge puede operar, si queda revocado y cuántos errores anota el núcleo.
type edgeInitialLeaseCase struct {
	name       string
	leases     []edgeIssuedLease
	cause      error
	canOperate bool
	revoked    bool
	coreErrors int
}

// TestArnes_EdgeInitialLease prueba, sin servidor, qué concluye conectar del PRIMER LeaseUpdate de
// una conexión (esperarLeaseInicial sobre un enlace que no termina): si el Validator lo rechaza
// —firma de otra clave, blob malformado— devuelve un error que envuelve errEdgeInitialLeaseRejected
// y la causa del Validator, y el Edge no puede operar; si es un lease vigente, nil y puede operar;
// si es una REVOCACIÓN, nil también (el Validator la acepta), con puedeOperar falso y revocado
// verdadero; un rechazo que no es el primero (contador viejo tras uno bueno) no cuenta; y una
// conexión nueva (reiniciarLeases) olvida el rechazo de la anterior. Leases() cuenta todos.
func TestArnes_EdgeInitialLease(t *testing.T) {
	t.Parallel()
	k := nuevasClaves(t)
	iss := edgeEmisorLease(t, k)
	foreign := edgeEmisorLease(t, nuevasClaves(t))
	issue := func(i *cllease.Issuer, counter int64) edgeIssuedLease {
		lu, err := i.Issue(edgeTestID, edgeTestTenant, time.Hour, counter)
		return edgeIssuedLease{lu, err}
	}
	revocation := func() edgeIssuedLease {
		lu, err := iss.Revoke(edgeTestID, edgeTestTenant)
		return edgeIssuedLease{lu, err}
	}
	cases := []edgeInitialLeaseCase{
		{"vigente", []edgeIssuedLease{issue(iss, 1)}, nil, true, false, 0},
		{"firma de otra clave", []edgeIssuedLease{issue(foreign, 1)}, cllease.ErrBadSignature, false, false, 1},
		{"malformado", []edgeIssuedLease{{&cloudlinkv1.LeaseUpdate{}, nil}}, cllease.ErrMalformed, false, false, 1},
		{"revocación", []edgeIssuedLease{revocation()}, nil, false, true, 0},
		{"vigente y después uno de contador viejo", []edgeIssuedLease{issue(iss, 5), issue(iss, 3)}, nil, true, false, 1},
		{"firma de otra clave y después uno vigente", []edgeIssuedLease{issue(foreign, 1), issue(iss, 2)}, cllease.ErrBadSignature, true, false, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := edgeCheckInitialLease(t, k, c)
			// Una conexión nueva empieza limpia: el rechazo de la anterior no la contamina.
			e.reiniciarLeases()
			if err := e.initialLeaseRejection(); err != nil || e.Leases() != 0 || e.puedeOperar() || e.revocado() {
				t.Errorf("tras reiniciarLeases: rechazo %v, leases %d, puedeOperar %v, revocado %v; quería todo a cero",
					err, e.Leases(), e.puedeOperar(), e.revocado())
			}
		})
	}
}

// edgeCheckInitialLease es el cuerpo de un caso de TestArnes_EdgeInitialLease: le da a un Edge
// nuevo los leases del caso, en orden, y comprueba lo que devuelve esperarLeaseInicial (nil, o un
// error que envuelve errEdgeInitialLeaseRejected y la causa), el estado del Validator y las cuentas
// de Leases y Errores. Devuelve el Edge, tal como quedó. Falla el test con t.Errorf por cada
// incumplimiento.
func edgeCheckInitialLease(t *testing.T, k claves, c edgeInitialLeaseCase) *edge {
	t.Helper()
	e := nuevoEdge(edgeTestTenant, edgeTestID, "sesion-prueba", k.NubePub, k.LeasePub)
	for _, l := range c.leases {
		e.manejar(edgeLeaseCommand(t, e, l.lu, l.err))
	}
	err := e.esperarLeaseInicial(t.Context(), &edgeEnlace{fin: make(chan struct{})})
	switch {
	case c.cause == nil && err != nil:
		t.Errorf("esperarLeaseInicial = %v, quería nil", err)
	case c.cause != nil && (!errors.Is(err, errEdgeInitialLeaseRejected) || !errors.Is(err, c.cause)):
		t.Errorf("esperarLeaseInicial = %v, quería un error que envuelva errEdgeInitialLeaseRejected y %v", err, c.cause)
	}
	if errors.Is(err, errEdgeLeaseNoLlego) {
		t.Errorf("un lease rechazado no es un lease que no llegó: %v", err)
	}
	if e.puedeOperar() != c.canOperate || e.revocado() != c.revoked {
		t.Errorf("puedeOperar=%v revocado=%v, quería %v y %v", e.puedeOperar(), e.revocado(), c.canOperate, c.revoked)
	}
	if e.Leases() != len(c.leases) || len(e.Errores()) != c.coreErrors {
		t.Errorf("Leases()=%d Errores()=%v, quería %d leases y %d errores", e.Leases(), e.Errores(), len(c.leases), c.coreErrors)
	}
	return e
}
