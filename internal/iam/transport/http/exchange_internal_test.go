package iamhttp

// exchange_internal_test.go — EL CANDADO DE INV-8 EN EL CANJE
// (Plan 047 · Ola 5 · T5.1, D-047.14).
//
// 🔴 POR QUÉ ESTE TEST NACE JUSTO AHORA, que es lo que hay que entender antes de
// borrarlo. T5.1 abre el multi-empresa: a partir de hoy hay una empresa que
// ELEGIR, y la forma obvia —y equivocada— de resolverlo es añadir un `tenant_id`
// al cuerpo del canje. Es la tentación que este candado existe para hacer
// imposible en silencio.
//
// El motivo no es doctrinal. Los tres consumidores web RE-CANJEAN SOLOS cada
// ~13 min, sin nadie delante (el Context Token dura 15 min por defecto, ver
// usecase/config.go), así que un tenant que viajara en el canje viajaría en cada
// uno de esos refrescos DESATENDIDOS. La elección entra por una puerta aparte
// (POST /api/v1/auth/active-tenant) y se guarda en el SERVIDOR, precisamente para
// que el refresco no pueda cambiarla.
//
// Está en `package iamhttp` (test interno) por lo mismo que su gemelo
// canje_internal_test.go: el DTO es privado y debe seguir siéndolo. Y es el
// hermano estructural de ese test, con el mismo método —el conjunto EXACTO de
// claves JSON— sobre el otro cuerpo de este paquete.

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestCuerpoDelCanje_SoloPuedeAportarElIdentityToken.
func TestCuerpoDelCanje_SoloPuedeAportarElIdentityToken(t *testing.T) {
	t.Parallel()

	tipo := reflect.TypeOf(exchangeRequest{})
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

	quiero := []string{"identity_token"}
	if !slices.Equal(claves, quiero) {
		t.Fatalf("el cuerpo de POST /api/v1/auth/exchange acepta %v, y solo puede aceptar %v.\n"+
			"Si acabas de añadir `tenant_id` para resolver el multi-empresa: NO va aquí (D-047.14). "+
			"Este endpoint lo llaman TRES consumidores web que re-canjean SOLOS cada ~13 min sin nadie "+
			"delante, así que un tenant en este cuerpo viajaría en cada refresco desatendido — que es "+
			"exactamente lo que INV-8 prohíbe. La empresa se elige en POST /api/v1/auth/active-tenant y "+
			"se guarda en el servidor; el canje la LEE de ahí y la contrasta contra tenant_members.",
			claves, quiero)
	}
}

// TestCuerpoDelCanje_UnTenantEnElCuerpoNoSeDecodificaEnElCanje es la otra mitad,
// y prueba la propiedad de VERDAD en vez de la forma del struct: aunque llegue un
// `tenant_id` por el cable, no hay dónde aterrice.
//
// Los dos tests parecen el mismo y no lo son: el de arriba caza que alguien AÑADA
// el campo; este caza que el decodificador no lo esté aceptando por otra vía (un
// `map[string]any`, un UnmarshalJSON a mano, un campo embebido).
func TestCuerpoDelCanje_UnTenantEnElCuerpoNoSeDecodificaEnElCanje(t *testing.T) {
	t.Parallel()

	var req exchangeRequest
	const cuerpo = `{"identity_token":"eyJhb.abc.def","tenant_id":"11111111-1111-1111-1111-111111111111"}`
	if err := json.Unmarshal([]byte(cuerpo), &req); err != nil {
		t.Fatalf("decodificar: %v", err)
	}
	if req.IdentityToken != "eyJhb.abc.def" {
		t.Fatalf("identity_token = %q", req.IdentityToken)
	}

	vuelta, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("serializar: %v", err)
	}
	if strings.Contains(string(vuelta), "tenant") {
		t.Errorf("el cuerpo del canje retuvo algo de tenant tras el round-trip: %s", vuelta)
	}
}
