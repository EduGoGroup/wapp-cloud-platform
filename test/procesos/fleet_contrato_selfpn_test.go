//go:build integracion

package procesos

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// Casos propios del self_pn de fleet.PostgresRepository (hallazgo 30 de F3): lo que la suite de
// contrato no puede afirmar porque solo existe contra Postgres —el cifrado en reposo, el índice
// ciego, la guarda del UPDATE y la rotación de KEK—. Parte de fleet_contrato_test.go (05 E-13) y
// no importa nada de internal/ (R9.4.d, regla 1): el adaptador, su KeyProvider y su FieldCipher le
// llegan por el fleetContractRig que construye aquel fichero. Todo entra por el puerto
// (SetSelfPn, Get, List, CountLiveBySelfPn) y se observa por SQL.
//
// La guarda tiene una tercera rama, la del sobre incompleto (D-F3-12): su caso está en
// fleet_contrato_heal_test.go.

const (
	// fleetContractSelfPn es el número propio de los casos, en su forma canónica (E.164 sin «+» ni
	// separadores), y fleetContractSelfPnSpelled, el mismo número como lo escribiría una persona.
	fleetContractSelfPn        = "56984467443"
	fleetContractSelfPnSpelled = "+56 9 8446-7443"
	// fleetContractOtherSelfPn es otro número, también canónico.
	fleetContractOtherSelfPn = "573001112233"

	// fleetContractOldKeyID y fleetContractNewKeyID son los key_id de las dos KEK del caso de la
	// rotación: la que envolvió la fila y la que es current después.
	fleetContractOldKeyID = "antes"
	fleetContractNewKeyID = "despues"
)

// setFleetContractSelfPn llama a SetSelfPn por el puerto y falla el test si devuelve error.
func setFleetContractSelfPn(t *testing.T, rig fleetContractRig, tenantID, edgeID, sessionID, selfPn string) {
	t.Helper()
	if err := rig.m.Repository.SetSelfPn(t.Context(), tenantID, edgeID, sessionID, selfPn); err != nil {
		t.Fatalf("SetSelfPn(%s, %s, %s): error inesperado %v", tenantID, edgeID, sessionID, err)
	}
}

// requireFleetContractSelfPn afirma el número que Get sirve para la sesión (want vacío: sin número
// o con un sobre que no abre) y que servirlo no es un error.
func requireFleetContractSelfPn(t *testing.T, rig fleetContractRig, tenantID, edgeID, sessionID, want string) {
	t.Helper()
	s, found, err := rig.m.Repository.Get(t.Context(), tenantID, edgeID, sessionID)
	if err != nil || !found {
		t.Fatalf("Get(%s, %s, %s) = (found=%v, err=%v); quería la sesión sin error", tenantID, edgeID, sessionID, found, err)
	}
	if s.SelfPn != want {
		t.Errorf("Get(%s, %s): self_pn = %q; quería %q", edgeID, sessionID, s.SelfPn, want)
	}
}

// requireFleetContractCount afirma cuántas sesiones vivas del tenant cuenta el rig con ese número.
func requireFleetContractCount(t *testing.T, rig fleetContractRig, tenantID, selfPn string, want int) {
	t.Helper()
	got, err := rig.m.Repository.CountLiveBySelfPn(t.Context(), tenantID, selfPn)
	if err != nil || got != want {
		t.Errorf("CountLiveBySelfPn(%s) = (%d, %v); quería (%d, nil)", tenantID, got, err, want)
	}
}

// requireFleetContractNoPlainSelfPn afirma que el número propio de los casos no está en claro en
// ningún sitio de la fila: la tabla no tiene columna `self_pn`, el JSON de la fila entera (todas
// sus columnas, las bytea en hexadecimal) no lo contiene y los bytes del sobre tampoco.
func requireFleetContractNoPlainSelfPn(t *testing.T, rig fleetContractRig, row fleetContractRow) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), fleetContractTimeout)
	defer cancel()
	var plainColumns int
	err := rig.db.QueryRowContext(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'fleet_sessions' AND column_name = 'self_pn'`,
	).Scan(&plainColumns)
	if err != nil || plainColumns != 0 {
		t.Errorf("public.fleet_sessions tiene %d columnas self_pn en claro (err %v); quería 0", plainColumns, err)
	}
	if strings.Contains(row.json, fleetContractSelfPn) || bytes.Contains(row.enc, []byte(fleetContractSelfPn)) {
		t.Errorf("la fila lleva el número en claro: %s", row.json)
	}
}

// TestFleetContract_SetSelfPn_StoresAnEnvelopeAndABlindIndex: el número propio se guarda CIFRADO.
// Tras SetSelfPn con el número escrito a mano, la fila lleva las cuatro columnas del sobre y
// ninguna el número en claro:
//
//   - la columna `self_pn` no existe (la retiró la migración 0070) y el JSON de la fila entera no
//     contiene el número, ni tampoco los bytes del sobre;
//   - self_pn_kek_id es el de la KEK current, y el sobre abierto con ella da la forma CANÓNICA;
//   - self_pn_bidx es el índice ciego de la forma canónica, no el de lo que se escribió: se
//     normaliza antes de indexar, y por eso las dos grafías cuentan como un solo número.
func TestFleetContract_SetSelfPn_StoresAnEnvelopeAndABlindIndex(t *testing.T) {
	keyring := fleetContractKeyID + ":" + clavesSecretoB64(t)
	rig := newFleetContractRig(t, newFleetContractDB(t), keyring, fleetContractKeyID, clavesSecretoB64(t))
	tenant, edge, session := newFleetContractSession(t, rig)

	if born := readFleetContractRow(t, rig.db, tenant, edge, session); born.enc != nil || born.dek != nil || born.kekID.Valid || born.bidx.Valid {
		t.Fatalf("una sesión sin emparejar ya lleva sobre: %s", born.json)
	}
	setFleetContractSelfPn(t, rig, tenant, edge, session, fleetContractSelfPnSpelled)
	row := readFleetContractRow(t, rig.db, tenant, edge, session)
	requireFleetContractNoPlainSelfPn(t, rig, row)

	if len(row.enc) == 0 || len(row.dek) == 0 {
		t.Fatalf("el sobre está vacío: enc %d bytes, dek %d bytes", len(row.enc), len(row.dek))
	}
	if !row.kekID.Valid || row.kekID.String != rig.kp.CurrentKeyID() {
		t.Errorf("self_pn_kek_id = %q (válido %v); quería el de la KEK current %q", row.kekID.String, row.kekID.Valid, rig.kp.CurrentKeyID())
	}
	if opened, err := rig.cipher.Decrypt(row.enc, row.dek, row.kekID.String); err != nil || opened != fleetContractSelfPn {
		t.Errorf("el sobre abierto da (%q, %v); quería la forma canónica %q", opened, err, fleetContractSelfPn)
	}

	if want := rig.kp.BlindIndex(tenant, fleetContractSelfPn); !row.bidx.Valid || row.bidx.String != want {
		t.Errorf("self_pn_bidx = %q (válido %v); quería el índice ciego de la forma canónica %q", row.bidx.String, row.bidx.Valid, want)
	}
	if row.bidx.String == rig.kp.BlindIndex(tenant, fleetContractSelfPnSpelled) {
		t.Error("self_pn_bidx es el índice de la grafía escrita: se indexó sin normalizar")
	}
	requireFleetContractSelfPn(t, rig, tenant, edge, session, fleetContractSelfPn)
	requireFleetContractCount(t, rig, tenant, fleetContractSelfPn, 1)
}

// TestFleetContract_SetSelfPn_SameNumber_DoesNotRewriteTheRow es la guarda del UPDATE: el Edge
// reporta su número en cada latido, y Encrypt genera una DEK nueva por llamada, así que sin guarda
// la fila se reescribiría cada 30 s. Con el mismo número —escrito de otra forma— la fila queda
// byte a byte como estaba (mismo sobre, misma DEK envuelta, updated_at sin tocar); con OTRO número
// la guarda deja pasar y la fila cambia, que es lo que demuestra que la primera mitad no pasa en
// verde porque SetSelfPn no haga nada.
func TestFleetContract_SetSelfPn_SameNumber_DoesNotRewriteTheRow(t *testing.T) {
	keyring := fleetContractKeyID + ":" + clavesSecretoB64(t)
	rig := newFleetContractRig(t, newFleetContractDB(t), keyring, fleetContractKeyID, clavesSecretoB64(t))
	tenant, edge, session := newFleetContractSession(t, rig)

	setFleetContractSelfPn(t, rig, tenant, edge, session, fleetContractSelfPn)
	freezeFleetContractRow(t, rig.db, fleetContractFreezeUpdatedAt, tenant, edge, session)
	first := readFleetContractRow(t, rig.db, tenant, edge, session)

	for _, same := range []string{fleetContractSelfPn, fleetContractSelfPnSpelled} {
		setFleetContractSelfPn(t, rig, tenant, edge, session, same)
		again := readFleetContractRow(t, rig.db, tenant, edge, session)
		if !bytes.Equal(again.enc, first.enc) || !bytes.Equal(again.dek, first.dek) || !again.updatedAt.Equal(fleetContractFrozenAt) {
			t.Errorf("SetSelfPn(%q) con el mismo número reescribió la fila: updated_at %v (quería %v), sobre igual %v, DEK igual %v",
				same, again.updatedAt, fleetContractFrozenAt, bytes.Equal(again.enc, first.enc), bytes.Equal(again.dek, first.dek))
		}
	}

	setFleetContractSelfPn(t, rig, tenant, edge, session, fleetContractOtherSelfPn)
	changed := readFleetContractRow(t, rig.db, tenant, edge, session)
	if bytes.Equal(changed.enc, first.enc) || changed.bidx.String == first.bidx.String || changed.updatedAt.Equal(fleetContractFrozenAt) {
		t.Errorf("SetSelfPn con otro número no reescribió la fila: updated_at %v, bidx %q (antes %q)", changed.updatedAt, changed.bidx.String, first.bidx.String)
	}
	requireFleetContractSelfPn(t, rig, tenant, edge, session, fleetContractOtherSelfPn)
}

// TestFleetContract_SetSelfPn_RotatedKEK_HealsTheRowOnce es la rotación de KEK, con dos KEK y tres
// despliegues del servidor sobre la misma base y la misma clave de índice ciego:
//
//   - before: el keyring solo tiene la KEK vieja. Escribe la fila.
//   - rotated: la rotación bien hecha, las dos KEK y la nueva como current. Lee la fila con la KEK
//     que la envolvió (self_pn_kek_id), no con la current.
//   - lost: la rotación MAL cerrada, sin la KEK vieja. El sobre no abre: Get y List sirven la
//     sesión con el número vacío y SIN error (un campo ilegible no tumba el listado), y el conteo
//     por índice ciego sigue dando 1, porque el índice no depende de la KEK.
//
// Y el latido siguiente de ese despliegue la AUTO-SANA: SetSelfPn con el mismo número reescribe el
// sobre con la KEK current —la rama `self_pn_kek_id IS DISTINCT FROM` de la guarda— sin necesitar
// la vieja, y converge en UNA escritura: el latido de después ya no toca la fila.
func TestFleetContract_SetSelfPn_RotatedKEK_HealsTheRowOnce(t *testing.T) {
	db := newFleetContractDB(t)
	oldKEK, newKEK, index := clavesSecretoB64(t), clavesSecretoB64(t), clavesSecretoB64(t)
	oldEntry, newEntry := fleetContractOldKeyID+":"+oldKEK, fleetContractNewKeyID+":"+newKEK
	before := newFleetContractRig(t, db, oldEntry, fleetContractOldKeyID, index)
	rotated := newFleetContractRig(t, db, oldEntry+","+newEntry, fleetContractNewKeyID, index)
	lost := newFleetContractRig(t, db, newEntry, fleetContractNewKeyID, index)

	tenant, edge, session := newFleetContractSession(t, before)
	setFleetContractSelfPn(t, before, tenant, edge, session, fleetContractSelfPn)
	freezeFleetContractRow(t, db, fleetContractFreezeUpdatedAt, tenant, edge, session)
	written := readFleetContractRow(t, db, tenant, edge, session)
	if written.kekID.String != fleetContractOldKeyID {
		t.Fatalf("self_pn_kek_id = %q; quería el de la KEK vieja %q", written.kekID.String, fleetContractOldKeyID)
	}

	requireFleetContractSelfPn(t, rotated, tenant, edge, session, fleetContractSelfPn)
	requireFleetContractSelfPn(t, lost, tenant, edge, session, "")
	listed, err := lost.m.Repository.List(t.Context(), tenant)
	if err != nil || len(listed) != 1 || listed[0].SelfPn != "" {
		t.Errorf("List sin la KEK vieja = (%d sesiones, err %v); quería la sesión, con el número vacío y sin error", len(listed), err)
	}
	requireFleetContractCount(t, lost, tenant, fleetContractSelfPn, 1)
	if untouched := readFleetContractRow(t, db, tenant, edge, session); !untouched.updatedAt.Equal(fleetContractFrozenAt) {
		t.Errorf("leer una fila ilegible la reescribió: updated_at = %v", untouched.updatedAt)
	}

	// El latido siguiente, ya sin la KEK vieja.
	setFleetContractSelfPn(t, lost, tenant, edge, session, fleetContractSelfPn)
	healed := readFleetContractRow(t, db, tenant, edge, session)
	if healed.kekID.String != fleetContractNewKeyID || bytes.Equal(healed.enc, written.enc) || healed.updatedAt.Equal(fleetContractFrozenAt) {
		t.Fatalf("la fila no se auto-sanó: self_pn_kek_id %q (quería %q), sobre igual %v, updated_at %v",
			healed.kekID.String, fleetContractNewKeyID, bytes.Equal(healed.enc, written.enc), healed.updatedAt)
	}
	if healed.bidx.String != written.bidx.String {
		t.Errorf("rotar la KEK movió el índice ciego: %q → %q", written.bidx.String, healed.bidx.String)
	}
	requireFleetContractSelfPn(t, lost, tenant, edge, session, fleetContractSelfPn)

	// Y el de después no hace nada.
	freezeFleetContractRow(t, db, fleetContractFreezeUpdatedAt, tenant, edge, session)
	setFleetContractSelfPn(t, lost, tenant, edge, session, fleetContractSelfPn)
	settled := readFleetContractRow(t, db, tenant, edge, session)
	if !bytes.Equal(settled.enc, healed.enc) || !settled.updatedAt.Equal(fleetContractFrozenAt) {
		t.Errorf("tras sanar, el latido siguiente volvió a reescribir la fila: updated_at %v, sobre igual %v", settled.updatedAt, bytes.Equal(settled.enc, healed.enc))
	}
}
