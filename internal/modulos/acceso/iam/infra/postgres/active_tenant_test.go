package iampostgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

// Los adaptadores de este paquete se prueban en unitario SIN base: su SQL lo cubre la suite de
// iam/ports/out/outhelpertest contra Postgres (P4, F9). Lo que sí se afirma aquí es lo que el
// contrato promete cuando la base NO contesta: el error de infraestructura se propaga envuelto
// con su texto y nunca se disfraza de ausencia, de lista vacía o de un centinela de negocio.

// errPoolDown es la causa que devuelve un pool caído: cualquier operación sobre él falla con
// este error al pedir conexión.
var errPoolDown = errors.New("pool caído (test)")

// downConnector es un driver.Connector que nunca da conexión: el pool que construye con
// sql.OpenDB no tiene base detrás y falla en el acto, sin red.
type downConnector struct{}

func (downConnector) Connect(context.Context) (driver.Conn, error) { return nil, errPoolDown }
func (downConnector) Driver() driver.Driver                        { return downDriver{} }

// downDriver es el driver.Driver de downConnector: tampoco abre nada.
type downDriver struct{}

func (downDriver) Open(string) (driver.Conn, error) { return nil, errPoolDown }

// downPool devuelve un *sql.DB sin base detrás: toda consulta, sentencia o transacción falla
// con errPoolDown. Se cierra al acabar el test.
func downPool(t *testing.T) *sql.DB {
	t.Helper()
	db := sql.OpenDB(downConnector{})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrar el pool caído: %v", err)
		}
	})
	return db
}

// wantInfraError exige que err envuelva errPoolDown y empiece por prefix: el texto con que el
// adaptador nombra la operación que falló.
func wantInfraError(t *testing.T, what string, err error, prefix string) {
	t.Helper()
	if !errors.Is(err, errPoolDown) {
		t.Fatalf("%s con el pool caído = %v; quiere un error que envuelva la causa", what, err)
	}
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("%s: texto %q; quiere el prefijo %q", what, err.Error(), prefix)
	}
}

const (
	testUserID   = "0b6a7f3e-5c1d-4e2a-9f8b-7c6d5e4f3a21"
	testTenantID = "6f0f3c2e-8d1b-4a57-9c64-2b7a1d5e9f10"
)

var _ func(*sql.DB) *ActiveTenantRepo = NewActiveTenantRepo

// TestNewActiveTenantRepo_DoesNotTouchPool: construir no consulta nada; con un pool sin base
// detrás el repositorio se construye igual.
func TestNewActiveTenantRepo_DoesNotTouchPool(t *testing.T) {
	if NewActiveTenantRepo(downPool(t)) == nil {
		t.Fatal("NewActiveTenantRepo devolvió nil")
	}
}

// TestActiveTenantOf_InfraErrorIsNotAbsence: un fallo de la base NO es «no ha elegido»: sale
// ("", false, err) con la causa envuelta.
func TestActiveTenantOf_InfraErrorIsNotAbsence(t *testing.T) {
	tenant, ok, err := NewActiveTenantRepo(downPool(t)).ActiveTenantOf(t.Context(), testUserID)
	wantInfraError(t, "ActiveTenantOf", err, "iam: leyendo la empresa activa: ")
	if ok || tenant != "" {
		t.Errorf("ActiveTenantOf con error = (%q, %v); quiere (\"\", false)", tenant, ok)
	}
}

// TestSetActiveTenant_InfraError: un fallo de la base al guardar se propaga envuelto.
func TestSetActiveTenant_InfraError(t *testing.T) {
	err := NewActiveTenantRepo(downPool(t)).SetActiveTenant(t.Context(), testUserID, testTenantID)
	wantInfraError(t, "SetActiveTenant", err, "iam: guardando la empresa activa: ")
}
