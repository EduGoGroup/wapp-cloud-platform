package iamhttp_test

// canje_test.go — LA PUERTA HTTP DEL CANJE (Plan 047 · Ola A · T-A3).
//
// El test que importa aquí es el ANTI-ORÁCULO: que el 404 y el 410 sean
// indistinguibles salvo por su código. Y se comprueba comparando los BYTES, no
// leyendo los dos mensajes y decidiendo que «dicen lo mismo»: un test que
// afirmara sobre el contenido de cada cuerpo por separado seguiría verde el día
// que alguien le añadiera a uno de los dos una coma, un «(caducada)» o un campo
// extra en el JSON.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	iamhttp "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/transport/http"
)

// redeemerFalso devuelve siempre el mismo error (o nil) y guarda lo que recibió.
// No valida nada: aquí se prueba el TRANSPORTE, y el transporte no decide
// desenlaces.
type redeemerFalso struct {
	err           error
	tokenRecibido string
	llamadas      int
}

// compile-time: el doble satisface el MISMO puerto que el servicio real, así
// que un cambio de firma en in.InvitationRedeemer rompe aquí y no en silencio.
var _ in.InvitationRedeemer = (*redeemerFalso)(nil)

func (r *redeemerFalso) RedeemInvitation(_ context.Context, token string) error {
	r.llamadas++
	r.tokenRecibido = token
	return r.err
}

// pedir ejecuta una petición contra el handler con el cuerpo dado.
func pedir(t *testing.T, redeemer *redeemerFalso, cuerpo string) *httptest.ResponseRecorder {
	t.Helper()
	h := iamhttp.NewInvitationRedeemHandler(redeemer).Accept()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/invitations/accept", strings.NewReader(cuerpo))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestCanje_NoExisteYCaducadaSonIndISTINGUIBLES es el criterio anti-oráculo de
// T-A3. Sin él, quien tenga una lista de tokens puede sondear cuáles EXISTIERON.
//
// 🔴 SE COMPARAN LOS BYTES EXACTOS, no «el sentido». Y también el Content-Type:
// un cuerpo idéntico servido con dos tipos distintos vuelve a ser un oráculo.
//
// ⚠️ LO QUE ESTE TEST NO AFIRMA, dicho para que nadie lo lea de más: NO afirma
// que los dos casos sean indistinguibles del todo. El CÓDIGO sigue siendo
// distinto (404 frente a 410) porque el criterio de T-A3 lo pide así de forma
// explícita. Lo que se fija aquí es que el cuerpo no añada una segunda señal.
func TestCanje_NoExisteYCaducadaSonIndistinguibles(t *testing.T) {
	t.Parallel()

	recNoExiste := pedir(t, &redeemerFalso{err: domain.ErrNotFound}, `{"token":"WAPP-INV-0123"}`)
	recCaducada := pedir(t, &redeemerFalso{err: domain.ErrInvitationExpired}, `{"token":"WAPP-INV-0123"}`)

	if recNoExiste.Code != http.StatusNotFound {
		t.Errorf("no existe: code = %d, quiero 404", recNoExiste.Code)
	}
	if recCaducada.Code != http.StatusGone {
		t.Errorf("caducada: code = %d, quiero 410", recCaducada.Code)
	}

	cuerpoA, cuerpoB := recNoExiste.Body.Bytes(), recCaducada.Body.Bytes()
	if string(cuerpoA) != string(cuerpoB) {
		t.Errorf("los cuerpos DIFIEREN y eso es un oráculo: quien sondee tokens sabrá cuáles existieron.\n"+
			"  404: %s\n  410: %s", cuerpoA, cuerpoB)
	}

	ctA := recNoExiste.Header().Get("Content-Type")
	ctB := recCaducada.Header().Get("Content-Type")
	if ctA != ctB {
		t.Errorf("Content-Type distinto entre 404 (%q) y 410 (%q): mismo cuerpo servido de dos formas sigue distinguiendo", ctA, ctB)
	}

	// GUARDA ANTI-HUECO: dos cuerpos VACÍOS también serían «iguales» y este test
	// pasaría vigilando una pared. Se exige que haya cuerpo y que no delate.
	if len(cuerpoA) == 0 {
		t.Fatal("el cuerpo compartido está VACÍO: la comparación de arriba no probaría nada")
	}
	for _, delator := range []string{"caduc", "expir", "no encontr", "not found", "vencid", "revocad"} {
		if strings.Contains(strings.ToLower(string(cuerpoA)), delator) {
			t.Errorf("el cuerpo compartido dice %q: nombra la causa que no debe distinguirse (%s)", delator, cuerpoA)
		}
	}
}

// TestCanje_LosDesenlacesRestantes fija los otros tres códigos del criterio.
func TestCanje_LosDesenlacesRestantes(t *testing.T) {
	t.Parallel()

	casos := []struct {
		nombre string
		err    error
		cuerpo string
		quiero int
	}{
		{"canjeada", nil, `{"token":"WAPP-INV-ok"}`, http.StatusNoContent},
		{"ya usada o de otra empresa", domain.ErrConflict, `{"token":"WAPP-INV-x"}`, http.StatusConflict},
		{"token vacío", domain.ErrInvalidInput, `{"token":""}`, http.StatusBadRequest},
		{"json roto", nil, `{`, http.StatusBadRequest},
		{"fallo de infra", errors.New("la base se cayó"), `{"token":"WAPP-INV-x"}`, http.StatusInternalServerError},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			t.Parallel()
			rec := pedir(t, &redeemerFalso{err: c.err}, c.cuerpo)
			if rec.Code != c.quiero {
				t.Errorf("code = %d, quiero %d (cuerpo: %s)", rec.Code, c.quiero, rec.Body.String())
			}
		})
	}
}

// TestCanje_ElConflictoNoComparteCuerpoConElPar comprueba lo contrario del
// primer test: el 409 SÍ tiene voz propia.
//
// No es simetría cosmética. Si el 409 acabara compartiendo el cuerpo mudo del
// par, quien canjea con una cuenta que ya pertenece a otra empresa —el caso de
// T-A5, y el único de los cuatro que la persona PUEDE arreglar— se quedaría sin
// saber qué le pasa.
func TestCanje_ElConflictoNoComparteCuerpoConElPar(t *testing.T) {
	t.Parallel()

	recPar := pedir(t, &redeemerFalso{err: domain.ErrNotFound}, `{"token":"WAPP-INV-x"}`)
	recConflicto := pedir(t, &redeemerFalso{err: domain.ErrConflict}, `{"token":"WAPP-INV-x"}`)

	if recPar.Body.String() == recConflicto.Body.String() {
		t.Errorf("el 409 responde el cuerpo mudo del par 404/410 (%s): quien ya pertenece a otra empresa "+
			"no se entera de lo único que puede resolver", recConflicto.Body.String())
	}
}

// TestCanje_ElTokenViajaTalCualAlUsecase fija la frontera: el transporte NO
// normaliza ni recorta.
//
// 🔴 Es lo que evita la divergencia que HashInvitationToken vino a cerrar. La
// normalización vive DENTRO de esa función, en el único sitio que hashea; si
// además se recortara aquí, habría dos normalizaciones que mantener alineadas y
// la primera en cambiar rompería el canje sin que ningún test unitario de
// ninguno de los dos lados lo viera.
func TestCanje_ElTokenViajaTalCualAlUsecase(t *testing.T) {
	t.Parallel()

	const conRuido = "  wapp-inv-abc123  "
	doble := &redeemerFalso{}
	if rec := pedir(t, doble, `{"token":"  wapp-inv-abc123  "}`); rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, quiero 204", rec.Code)
	}
	if doble.llamadas != 1 {
		t.Fatalf("llamadas al usecase = %d, quiero 1", doble.llamadas)
	}
	if doble.tokenRecibido != conRuido {
		t.Errorf("el transporte tocó el token: recibido %q, quiero %q tal cual", doble.tokenRecibido, conRuido)
	}
}
