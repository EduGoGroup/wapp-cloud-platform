//go:build integracion

package procesos

import (
	"bytes"
	"testing"
)

// Caso propio de la AUTO-SANACIÓN del sobre incompleto del self_pn de fleet.PostgresRepository
// (D-F3-12): la otra mitad de fleet_contrato_greeting_test.go. Aquel fichero afirma qué SIRVE cada
// lector mientras el sobre está a medias; este, que la fila no se queda así: el latido siguiente
// trae el número en claro y SetSelfPn la re-cifra. Parte de fleet_contrato_test.go (05 E-13) y no
// importa nada de internal/ (R9.4.d, regla 1): el adaptador le llega por el fleetContractRig, y las
// sentencias de rotura son las de aquel fichero más la de abajo.
//
// 🔴 EL NUEVO SE APARTA DEL VIEJO A PROPÓSITO (D-F3-12): la guarda del viejo
// (internal/gateway/fleet) solo mira el índice ciego y el key_id, así que una fila sin enc o sin
// dek —con esos dos intactos— no entra nunca en el UPDATE y queda atrapada para siempre.

// fleetContractEmptyDek es la sexta forma de romper el sobre: la DEK envuelta vacía en vez de NULL.
// Es la gemela de fleetContractEmptyEnc, y existe porque «incompleto» se define con len()==0, que no
// distingue NULL de vacío: la guarda tampoco puede distinguirlos.
const fleetContractEmptyDek = `UPDATE public.fleet_sessions SET self_pn_dek = ''::bytea WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`

// requireFleetContractHealedEnvelope afirma, sobre la fila leída por SQL tras el latido, que el
// sobre que se rompió (written es la fila ANTES de romperla) quedó sanado: entero, escrito de nuevo
// —updated_at ya no es el centinela y ni el envelope ni la DEK envuelta repiten los de antes—, con
// la KEK current, abriendo a la forma canónica del número y con el índice ciego donde estaba. Si el
// sobre sigue a medias falla el test ahí: lo demás no tendría qué mirar.
func requireFleetContractHealedEnvelope(t *testing.T, rig fleetContractRig, written, healed fleetContractRow) {
	t.Helper()
	if len(healed.enc) == 0 || len(healed.dek) == 0 || !healed.kekID.Valid || healed.updatedAt.Equal(fleetContractFrozenAt) {
		t.Fatalf("la fila no se auto-sanó: enc %d bytes, dek %d bytes, self_pn_kek_id válido %v, updated_at %v (el centinela es %v)",
			len(healed.enc), len(healed.dek), healed.kekID.Valid, healed.updatedAt, fleetContractFrozenAt)
	}
	if healed.kekID.String != rig.kp.CurrentKeyID() {
		t.Errorf("self_pn_kek_id tras sanar = %q; quería el de la KEK current %q", healed.kekID.String, rig.kp.CurrentKeyID())
	}
	if opened, err := rig.cipher.Decrypt(healed.enc, healed.dek, healed.kekID.String); err != nil || opened != fleetContractSelfPn {
		t.Errorf("el sobre sanado abierto da (%q, %v); quería la forma canónica %q", opened, err, fleetContractSelfPn)
	}
	if bytes.Equal(healed.enc, written.enc) || bytes.Equal(healed.dek, written.dek) {
		t.Error("el sobre sanado repite bytes del que se rompió: sanar es cifrar de nuevo, con DEK fresca")
	}
	if !healed.bidx.Valid || healed.bidx.String != written.bidx.String {
		t.Errorf("sanar el sobre movió el índice ciego: %q → %q (válido %v)", written.bidx.String, healed.bidx.String, healed.bidx.Valid)
	}
}

// TestFleetContract_SetSelfPn_BrokenEnvelope_HealsOnTheNextHeartbeat: una fila cuyo sobre del
// self_pn no está entero se AUTO-SANA con el latido siguiente. SetSelfPn lo escribe entero y aquí se
// rompe por SQL, de seis maneras; la fila conserva su índice ciego en todas, que es justo lo que la
// atrapaba: el número «no cambió».
//
// Tras un SetSelfPn con el MISMO número:
//
//   - el sobre vuelve a estar entero y es NUEVO: abre con la KEK current y da la forma canónica, y
//     el índice ciego no se movió;
//   - Get y List vuelven a servir el número, y PendingGreeting vuelve a (número, true, nil);
//   - y CONVERGE EN UNA ESCRITURA: un segundo SetSelfPn idéntico ya no toca la fila (mismo sobre,
//     misma DEK envuelta, updated_at en el centinela). Sin esto la rama de la guarda que sana sería
//     la reescritura perpetua de cada 30 s que la guarda existe para impedir.
//
// «kek_id missing» y «blind index without envelope» ya sanaban antes de D-F3-12 —un key_id NULL es
// DISTINTO del current, la rama de la KEK rotada—; están aquí para que las seis formas queden
// afirmadas juntas y ninguna dependa de una rama que mañana se reescriba.
func TestFleetContract_SetSelfPn_BrokenEnvelope_HealsOnTheNextHeartbeat(t *testing.T) {
	cases := []struct {
		name      string
		statement string
	}{
		{"enc missing", fleetContractDropEnc},
		{"dek missing", fleetContractDropDek},
		{"kek_id missing", fleetContractDropKekID},
		{"empty enc", fleetContractEmptyEnc},
		{"empty dek", fleetContractEmptyDek},
		{"blind index without envelope", fleetContractDropEnvelope},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keyring := fleetContractKeyID + ":" + clavesSecretoB64(t)
			rig := newFleetContractRig(t, newFleetContractDB(t), keyring, fleetContractKeyID, clavesSecretoB64(t))
			tenant, edge, session := newFleetContractSession(t, rig)
			setFleetContractSelfPn(t, rig, tenant, edge, session, fleetContractSelfPn)
			written := readFleetContractRow(t, rig.db, tenant, edge, session)

			execFleetContractRow(t, rig.db, c.statement, tenant, edge, session)
			freezeFleetContractRow(t, rig.db, fleetContractFreezeUpdatedAt, tenant, edge, session)
			requireFleetContractSelfPn(t, rig, tenant, edge, session, "")

			// El latido siguiente: el mismo número, que es lo que el Edge reporta cada 30 s.
			setFleetContractSelfPn(t, rig, tenant, edge, session, fleetContractSelfPn)
			healed := readFleetContractRow(t, rig.db, tenant, edge, session)
			requireFleetContractHealedEnvelope(t, rig, written, healed)

			requireFleetContractSelfPn(t, rig, tenant, edge, session, fleetContractSelfPn)
			listed, err := rig.m.Repository.List(t.Context(), tenant)
			if err != nil || len(listed) != 1 || listed[0].SelfPn != fleetContractSelfPn {
				t.Errorf("List tras sanar = (%d sesiones, err %v); quería la sesión con su número", len(listed), err)
			}
			requireFleetContractCount(t, rig, tenant, fleetContractSelfPn, 1)
			requireFleetContractPending(t, rig, tenant, edge, session, fleetContractSelfPn, "de una sesión con el sobre ya sanado")

			// Y el de después no hace nada: la rama que sana se apaga con su propia escritura.
			freezeFleetContractRow(t, rig.db, fleetContractFreezeUpdatedAt, tenant, edge, session)
			setFleetContractSelfPn(t, rig, tenant, edge, session, fleetContractSelfPn)
			settled := readFleetContractRow(t, rig.db, tenant, edge, session)
			if !bytes.Equal(settled.enc, healed.enc) || !bytes.Equal(settled.dek, healed.dek) || !settled.updatedAt.Equal(fleetContractFrozenAt) {
				t.Errorf("tras sanar, el latido siguiente volvió a reescribir la fila: updated_at %v (quería %v), sobre igual %v, DEK igual %v",
					settled.updatedAt, fleetContractFrozenAt, bytes.Equal(settled.enc, healed.enc), bytes.Equal(settled.dek, healed.dek))
			}
		})
	}
}
