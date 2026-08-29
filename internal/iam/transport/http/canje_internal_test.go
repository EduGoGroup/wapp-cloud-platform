package iamhttp

// canje_internal_test.go — EL CANDADO DE INV-04 EN EL CANJE
// (Plan 047 · Ola A · T-A3).
//
// 🔴 POR QUÉ ESTE TEST EXISTE Y POR QUÉ ESTÁ DENTRO DEL PAQUETE. La mutación que
// T-A3 declara roja es «leer el tenant_id del cuerpo». Un test de conducta que
// mandara `{"token":"…","tenant_id":"otro"}` y comprobara que la membresía sale
// con el tenant bueno es necesario —y está, en el de integración—, pero llega
// TARDE: cuando alguien añade el campo al struct, el fallo ya vive en el binario
// y solo se ve si el test de integración corre (y sin WAPP_TEST_DB_DSN se salta,
// y un --- SKIP no es un --- PASS).
//
// Esto lo caza ANTES, sin base de datos y en el paquete del ESCRITOR del
// contrato: el conjunto de claves que el cuerpo puede aportar. Es la diferencia
// entre «lo ignoramos» y «no hay dónde ponerlo».
//
// Está en `package iamhttp` (test interno) porque el DTO es privado, y privado
// debe seguir: si fuera exportable, otro paquete podría construirlo y el
// contrato dejaría de estar en un solo sitio.

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestCuerpoDelCanje_SoloPuedeAportarElToken.
func TestCuerpoDelCanje_SoloPuedeAportarElToken(t *testing.T) {
	t.Parallel()

	tipo := reflect.TypeOf(redeemInvitationRequest{})
	var claves []string
	for i := range tipo.NumField() {
		tag := tipo.Field(i).Tag.Get("json")
		nombre, _, _ := strings.Cut(tag, ",")
		if nombre == "-" {
			continue
		}
		if nombre == "" {
			nombre = tipo.Field(i).Name
		}
		claves = append(claves, nombre)
	}
	slices.Sort(claves)

	quiero := []string{"token"}
	if !slices.Equal(claves, quiero) {
		t.Fatalf("el cuerpo de POST /api/v1/invitations/accept acepta %v, y solo puede aceptar %v.\n"+
			"Si acabas de añadir `tenant_id`: la empresa sale de la FILA de la invitación (la eligió quien la "+
			"emitió, con su propio token), NUNCA del cuerpo — es INV-04, y el cuerpo de esta petición lo llena "+
			"quien canjea, que es justo la persona que todavía no pertenece a ninguna empresa.\n"+
			"Si es otro campo: ningún dato del canje viaja por aquí salvo el token; lo demás sale del contexto "+
			"de identidad o de la fila.", claves, quiero)
	}
}

// TestCuerpoDelCanje_UnTenantEnElCuerpoNoSeDecodifica es la otra mitad, y prueba
// la propiedad de VERDAD en vez de la forma del struct: aunque llegue un
// `tenant_id` por el cable, no hay dónde aterrice.
//
// Los dos tests parecen el mismo y no lo son: el de arriba caza que alguien
// AÑADA el campo; este caza que el decodificador no lo esté aceptando por otra
// vía (un `map[string]any`, un UnmarshalJSON a mano, un campo embebido).
func TestCuerpoDelCanje_UnTenantEnElCuerpoNoSeDecodifica(t *testing.T) {
	t.Parallel()

	var req redeemInvitationRequest
	const cuerpo = `{"token":"WAPP-INV-abc","tenant_id":"11111111-1111-1111-1111-111111111111"}`
	if err := json.Unmarshal([]byte(cuerpo), &req); err != nil {
		t.Fatalf("decodificar: %v", err)
	}
	if req.Token != "WAPP-INV-abc" {
		t.Fatalf("token = %q, quiero WAPP-INV-abc", req.Token)
	}

	// Se vuelve a serializar y se mira lo que SOBREVIVIÓ. Un round-trip enseña
	// exactamente lo que el tipo es capaz de retener, sin depender de que este
	// test conozca los nombres de los campos.
	vuelta, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("serializar: %v", err)
	}
	if strings.Contains(string(vuelta), "tenant") {
		t.Errorf("el cuerpo retuvo algo de tenant tras el round-trip: %s", vuelta)
	}
}
