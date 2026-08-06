package publicapi_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// intakes_discard_test.go cubre POST /api/v1/intakes/discard (Plan 041 · T4.8,
// REQ-32 / D-041.18): el DESCARTE MANUAL por lotes del pedido huérfano.
//
// Cada una de las cuatro razones del `skipped` se produce con una PETICIÓN REAL
// sobre una bandeja sembrada, no comprobando que la constante existe.

// ids de la bandeja del descarte. Aparte de los del fixture común: descartar es
// destructivo y una bandeja compartida con el resto de los tests haría que un
// descarte de aquí cambiara el conteo de allí.
const (
	descarteOpen      = "d0000001-0000-4000-8000-000000000001" // open, huérfana
	descarteExpired   = "d0000002-0000-4000-8000-000000000002" // expired (legado del reloj derogado)
	descarteAbandoned = "d0000003-0000-4000-8000-000000000003" // ya descartada
	descarteConfirmed = "d0000004-0000-4000-8000-000000000004" // confirmada: se cancela, no se descarta
	descarteVivo      = "d0000005-0000-4000-8000-000000000005" // open CON conversación viva
	descarteAjeno     = "d0000006-0000-4000-8000-000000000006" // del tenant B
	descarteFantasma  = "d0000009-0000-4000-8000-000000000009" // no existe en ninguna parte
)

// contactoVivo es el contacto de la solicitud con conversación viva. Es OTRO que el
// de las demás a propósito: la ligadura con la conversación es por
// (tenant, sesión, contacto), así que si el fixture compartiera contacto, la vida de
// una conversación taparía a todas las solicitudes de la bandeja.
const contactoVivo = "9f1c0a7e-0000-4000-8000-000000000abd"

// discardResponseDTO espeja el contrato del 200.
type discardResponseDTO struct {
	Discarded []string `json:"discarded"`
	Skipped   []struct {
		IntakeID string `json:"intake_id"`
		Reason   string `json:"reason"`
	} `json:"skipped"`
}

// seedDescarte siembra una solicitud por cada caso que el lote tiene que saber
// contestar, más una del tenant B para el aislamiento.
func seedDescarte() *intakes.MemoryStore {
	st := intakes.NewMemoryStore()
	add := func(tenant, id, status, contacto string) {
		st.Add(tenant, intakes.Intake{
			ID: id, ContactID: contacto, SessionID: "sess-a", Status: status,
			Total: 18000, CreatedAt: día(1), UpdatedAt: día(1),
		}, intakes.Item{SKU: "torta-v1", Label: "Torta", Qty: 1, UnitPrice: 18000})
	}
	add(tenantA, descarteOpen, intakes.StatusOpen, contactoOpaco)
	add(tenantA, descarteExpired, intakes.StatusExpired, contactoOpaco)
	add(tenantA, descarteAbandoned, intakes.StatusAbandoned, contactoOpaco)
	add(tenantA, descarteConfirmed, intakes.StatusConfirmed, contactoOpaco)
	add(tenantA, descarteVivo, intakes.StatusOpen, contactoVivo)
	add(tenantB, descarteAjeno, intakes.StatusOpen, contactoOpaco)
	st.SetLiveCart(tenantA, "sess-a", contactoVivo)
	return st
}

// discardBody arma el cuerpo del POST con los ids dados.
func discardBody(t *testing.T, ids ...string) string {
	t.Helper()
	raw, err := json.Marshal(map[string][]string{"intake_ids": ids})
	if err != nil {
		t.Fatalf("serializando el cuerpo: %v", err)
	}
	return string(raw)
}

func decodeDiscard(t *testing.T, body []byte) discardResponseDTO {
	t.Helper()
	var out discardResponseDTO
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal del descarte: %v; body=%s", err, body)
	}
	return out
}

// razónWire busca la razón con la que la respuesta rechazó un id.
func razónWire(res discardResponseDTO, id string) string {
	for _, s := range res.Skipped {
		if s.IntakeID == id {
			return s.Reason
		}
	}
	return ""
}

// estadoDe lee el estado que la API publica hoy para una solicitud.
func estadoDe(t *testing.T, api *testAPI, credential, id string) string {
	t.Helper()
	rec := call(api, credential, http.MethodGet, "/api/v1/intakes/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: code=%d; body=%s", id, rec.Code, rec.Body.String())
	}
	var dto intakeDetailDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal del detalle: %v", err)
	}
	return dto.Status
}

// TestIntakesDiscard_200_LoteMixto_LasCuatroRazones es el criterio (a) del plan
// ampliado a los cuatro motivos: un lote con TODOS los casos a la vez sale con lo
// descartable descartado y cada rechazo con su razón exacta. Un rechazo no revierte
// a los demás: eso es lo que hace usable la bandeja de un dueño real, que marca
// veinte pedidos sin mirar el estado de cada uno.
func TestIntakesDiscard_200_LoteMixto_LasCuatroRazones(t *testing.T) {
	api := newAPI(intakesDeps(seedDescarte()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/discard",
		discardBody(t, descarteOpen, descarteExpired, descarteAbandoned,
			descarteConfirmed, descarteVivo, descarteAjeno, descarteFantasma))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}

	res := decodeDiscard(t, rec.Body.Bytes())
	if !slices.Equal(res.Discarded, []string{descarteOpen, descarteExpired}) {
		t.Fatalf("discarded=%v, quiero la open y la expired", res.Discarded)
	}
	for id, quiero := range map[string]string{
		descarteAbandoned: intakes.DiscardSkipAlreadyDiscarded,
		descarteConfirmed: intakes.DiscardSkipNotOpen,
		descarteVivo:      intakes.DiscardSkipLiveEvent,
		descarteAjeno:     intakes.DiscardSkipNotFound,
		descarteFantasma:  intakes.DiscardSkipNotFound,
	} {
		if got := razónWire(res, id); got != quiero {
			t.Fatalf("razón de %s = %q, quiero %q; body=%s", id, got, quiero, rec.Body.String())
		}
	}
	if len(res.Skipped) != 5 {
		t.Fatalf("skipped=%d, quiero 5; body=%s", len(res.Skipped), rec.Body.String())
	}

	// El lote mixto deja a cada solicitud donde le toca, no donde le tocó a la de al
	// lado.
	for id, quiero := range map[string]string{
		descarteOpen:      intakes.StatusAbandoned,
		descarteExpired:   intakes.StatusAbandoned,
		descarteAbandoned: intakes.StatusAbandoned,
		descarteConfirmed: intakes.StatusConfirmed,
		descarteVivo:      intakes.StatusOpen,
	} {
		if got := estadoDe(t, api, keyAIntakes, id); got != quiero {
			t.Fatalf("%s quedó en %q, quiero %q", id, got, quiero)
		}
	}
}

// TestIntakesDiscard_200_TenantCruzado es el criterio (c): un id de OTRO tenant
// vuelve como `not_found`, INDISTINGUIBLE de uno inexistente. Un `forbidden` —o
// cualquier razón propia— confirmaría que el id existe, y el aislamiento entre
// tenants no puede filtrar ni eso (INV-8).
func TestIntakesDiscard_200_TenantCruzado(t *testing.T) {
	store := seedDescarte()
	api := newAPI(intakesDeps(store), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/discard",
		discardBody(t, descarteAjeno, descarteFantasma))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}

	res := decodeDiscard(t, rec.Body.Bytes())
	if len(res.Discarded) != 0 {
		t.Fatalf("discarded=%v; el tenant A no puede descartar nada del B", res.Discarded)
	}
	ajeno, fantasma := razónWire(res, descarteAjeno), razónWire(res, descarteFantasma)
	if ajeno != intakes.DiscardSkipNotFound || fantasma != intakes.DiscardSkipNotFound {
		t.Fatalf("ajeno=%q fantasma=%q, quiero %q los dos",
			ajeno, fantasma, intakes.DiscardSkipNotFound)
	}
	// Y el cuerpo entero de las dos entradas es idéntico salvo el id: si una trajera
	// un campo de más, la diferencia SERÍA la filtración.
	if strings.Count(rec.Body.String(), intakes.DiscardSkipNotFound) != 2 {
		t.Fatalf("las dos razones tienen que ser la misma cadena; body=%s", rec.Body.String())
	}

	// La solicitud del tenant B sigue intacta: el 404 opaco no es solo el código.
	if got := estadoDe(t, api, keyBIntakes, descarteAjeno); got != intakes.StatusOpen {
		t.Fatalf("la solicitud del tenant B quedó en %q; nadie la tocó", got)
	}
}

// TestIntakesDiscard_200_Idempotente es el criterio (b): repetir la MISMA llamada
// deja el mismo estado final y NO crea revisiones nuevas. Es lo que hace seguro
// reintentar un lote que se cortó a medias.
func TestIntakesDiscard_200_Idempotente(t *testing.T) {
	store := seedDescarte()
	api := newAPI(intakesDeps(store), intakesKeys())
	cuerpo := discardBody(t, descarteOpen)

	primera := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/discard", cuerpo)
	if primera.Code != http.StatusOK {
		t.Fatalf("primera: code=%d; body=%s", primera.Code, primera.Body.String())
	}
	if got := decodeDiscard(t, primera.Body.Bytes()).Discarded; !slices.Equal(got, []string{descarteOpen}) {
		t.Fatalf("primera: discarded=%v, quiero [%s]", got, descarteOpen)
	}
	revsTrasLaPrimera := len(store.Revisions(descarteOpen))
	if revsTrasLaPrimera != 1 {
		t.Fatalf("revisiones tras el primer descarte=%d, quiero 1", revsTrasLaPrimera)
	}

	segunda := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/discard", cuerpo)
	if segunda.Code != http.StatusOK {
		t.Fatalf("segunda: code=%d; body=%s", segunda.Code, segunda.Body.String())
	}
	res := decodeDiscard(t, segunda.Body.Bytes())
	if len(res.Discarded) != 0 {
		t.Fatalf("segunda: discarded=%v; ya no quedaba nada por descartar", res.Discarded)
	}
	if got := razónWire(res, descarteOpen); got != intakes.DiscardSkipAlreadyDiscarded {
		t.Fatalf("segunda: razón=%q, quiero %q", got, intakes.DiscardSkipAlreadyDiscarded)
	}
	if got := len(store.Revisions(descarteOpen)); got != revsTrasLaPrimera {
		t.Fatalf("revisiones=%d tras repetir, quiero %d: dos llamadas iguales son UN acto",
			got, revsTrasLaPrimera)
	}
	if got := estadoDe(t, api, keyAIntakes, descarteOpen); got != intakes.StatusAbandoned {
		t.Fatalf("estado final=%q, quiero abandoned", got)
	}
}

// TestIntakesDiscard_200_ElDescartadoSaleEnLaBandeja es la mitad del criterio (d)
// que esta ola sí entrega: lo descartado NO desaparece — sigue en la bandeja
// filtrada por `abandoned`, con sus líneas y su revisión. No borra nada.
func TestIntakesDiscard_200_ElDescartadoSaleEnLaBandeja(t *testing.T) {
	api := newAPI(intakesDeps(seedDescarte()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/discard",
		discardBody(t, descarteOpen))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d; body=%s", rec.Code, rec.Body.String())
	}

	lista := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes?status=abandoned", "")
	if lista.Code != http.StatusOK {
		t.Fatalf("lista: code=%d; body=%s", lista.Code, lista.Body.String())
	}
	ids := idsDe(decodeList(t, lista.Body.Bytes()))
	if !slices.Contains(ids, descarteOpen) {
		t.Fatalf("ids=%v; la descartada tiene que salir en ?status=abandoned", ids)
	}

	detalle := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/"+descarteOpen, "")
	var dto intakeDetailDTO
	if err := json.Unmarshal(detalle.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal del detalle: %v", err)
	}
	if len(dto.Items) != 1 || dto.Items[0].SKU != "torta-v1" {
		t.Fatalf("items=%+v; el descarte no borra las líneas", dto.Items)
	}
	if len(dto.Revisions) != 1 || dto.Revisions[0].Kind != intakes.RevisionKindDiscarded {
		t.Fatalf("revisiones=%+v; quiero una `discarded`", dto.Revisions)
	}
	if dto.Revisions[0].CreatedBy != intakes.RevisionByOwner {
		t.Fatalf("created_by=%q, quiero %q", dto.Revisions[0].CreatedBy, intakes.RevisionByOwner)
	}
	if len(dto.AllowedTransitions) != 0 {
		t.Fatalf("allowed_transitions=%v; abandoned es TERMINAL", dto.AllowedTransitions)
	}
}

// TestIntakesDiscard_400_CuerposInválidos: los tres únicos 400 del endpoint. El
// criterio (f) del plan —201 ids ⇒ 400— es el tercero.
func TestIntakesDiscard_400_CuerposInválidos(t *testing.T) {
	api := newAPI(intakesDeps(seedDescarte()), intakesKeys())

	demasiados := make([]string, intakes.MaxDiscardBatch+1)
	for i := range demasiados {
		demasiados[i] = descarteFantasma
	}

	casos := []struct {
		nombre string
		cuerpo string
	}{
		{"cuerpo que no es JSON", "esto-no-es-json"},
		{"sin la clave intake_ids", `{}`},
		{"lista vacía", `{"intake_ids":[]}`},
		{"un id de más", discardBody(t, demasiados...)},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			rec := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/discard", c.cuerpo)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}

	// El tope entra justo: 200 ids es una petición válida, 201 no.
	justo := make([]string, intakes.MaxDiscardBatch)
	for i := range justo {
		justo[i] = descarteFantasma
	}
	rec := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/discard", discardBody(t, justo...))
	if rec.Code != http.StatusOK {
		t.Fatalf("con %d ids: code=%d, quiero 200; body=%s",
			intakes.MaxDiscardBatch, rec.Code, rec.Body.String())
	}
	// Ojo: 200 ids REPETIDOS se colapsan a uno en la respuesta (el tope mide el
	// cuerpo que llega, no las filas que se tocan).
	if got := len(decodeDiscard(t, rec.Body.Bytes()).Skipped); got != 1 {
		t.Fatalf("skipped=%d, quiero 1: el mismo id repetido se contesta una vez", got)
	}
}

// TestIntakesDiscard_ExpiredSoloPorEstaPuerta es la decisión de Jhoan del
// 2026-08-06 verificada extremo a extremo: la MISMA pareja de estados
// (`expired → abandoned`) se acepta por el descarte manual y se rechaza con 422 por
// el cambio de estado. Si alguien "unifica" el descarte sobre SetStatus, esta
// segunda mitad se cae.
func TestIntakesDiscard_ExpiredSoloPorEstaPuerta(t *testing.T) {
	api := newAPI(intakesDeps(seedDescarte()), intakesKeys())

	// Por la puerta del ciclo de vida: 422 con el estado actual y los destinos.
	status := call(api, keyAIntakes, http.MethodPost,
		"/api/v1/intakes/"+descarteExpired+"/status", `{"status":"abandoned"}`)
	if status.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST /status: code=%d, quiero 422; body=%s", status.Code, status.Body.String())
	}
	var invalid invalidTransitionDTO
	if err := json.Unmarshal(status.Body.Bytes(), &invalid); err != nil {
		t.Fatalf("unmarshal del 422: %v", err)
	}
	if invalid.Status != intakes.StatusExpired || len(invalid.Allowed) != 0 {
		t.Fatalf("422=%+v; expired es terminal y no ofrece destinos", invalid)
	}

	// Por la puerta del descarte: sí.
	rec := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/discard",
		discardBody(t, descarteExpired))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /discard: code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeDiscard(t, rec.Body.Bytes()).Discarded; !slices.Equal(got, []string{descarteExpired}) {
		t.Fatalf("discarded=%v, quiero [%s]", got, descarteExpired)
	}
	if got := estadoDe(t, api, keyAIntakes, descarteExpired); got != intakes.StatusAbandoned {
		t.Fatalf("estado=%q, quiero abandoned", got)
	}
}

// TestIntakesDiscard_403_SinLaFeature: descartar es operar la bandeja, así que va
// con el MISMO gate comercial que la bandeja (cart_basic). Sin plan no hay puerta,
// y el corte es fail-closed (403, nunca 500).
func TestIntakesDiscard_403_SinLaFeature(t *testing.T) {
	fake := entitlements.NewFake() // ninguna feature encendida
	deps := publicapi.Deps{Intakes: intakes.NewService(seedDescarte()), Entitlements: fake}
	api := newAPI(deps, intakesKeys())

	rec := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/discard",
		discardBody(t, descarteOpen))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), string(entitlements.FeatureCartBasic)) {
		t.Fatalf("el 403 tiene que decir qué feature falta; body=%s", rec.Body.String())
	}
}

// TestIntakesDiscard_401_SinCredencial: sin identidad no hay tenant del que sacar
// la bandeja, y el descarte es lo último que puede correr sin dueño.
func TestIntakesDiscard_401_SinCredencial(t *testing.T) {
	api := newAPI(intakesDeps(seedDescarte()), intakesKeys())

	rec := call(api, "", http.MethodPost, "/api/v1/intakes/discard", discardBody(t, descarteOpen))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401; body=%s", rec.Code, rec.Body.String())
	}
}

// TestIntakesDiscard_LaRutaLiteralConviveConElComodín fija cómo resuelve el mux la
// convivencia de /intakes/discard (literal) con /intakes/{id} (comodín), que no es
// obvia y conviene tenerla escrita:
//
//   - POST /intakes/discard entra al descarte. No compite con nada: no existe
//     ningún POST /intakes/{id} (el cambio de estado cuelga de …/{id}/status).
//   - GET /intakes/discard NO da 405 sino 404, porque SÍ hay un patrón GET que casa
//     —el comodín—, y "discard" se interpreta como el id de una solicitud que no
//     existe. Es inofensivo y es la respuesta correcta; queda anotado para que
//     nadie lo lea como un bug de enrutado.
func TestIntakesDiscard_LaRutaLiteralConviveConElComodín(t *testing.T) {
	api := newAPI(intakesDeps(seedDescarte()), intakesKeys())

	post := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/discard",
		discardBody(t, descarteFantasma))
	if post.Code != http.StatusOK {
		t.Fatalf("POST /intakes/discard: code=%d, quiero 200; body=%s", post.Code, post.Body.String())
	}
	if razónWire(decodeDiscard(t, post.Body.Bytes()), descarteFantasma) != intakes.DiscardSkipNotFound {
		t.Fatalf("el POST no llegó al handler del descarte; body=%s", post.Body.String())
	}

	get := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/discard", "")
	if get.Code != http.StatusNotFound {
		t.Fatalf("GET /intakes/discard: code=%d, quiero 404 (lo atiende el comodín); body=%s",
			get.Code, get.Body.String())
	}
	// Y el comodín sigue sirviendo lo suyo.
	if got := estadoDe(t, api, keyAIntakes, descarteConfirmed); got != intakes.StatusConfirmed {
		t.Fatalf("estado=%q; /intakes/{id} sigue vivo", got)
	}
}
