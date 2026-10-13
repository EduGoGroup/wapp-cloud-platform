package grpc

// 🔒 El literal AVISO_SESION_PASIVA_V1, vigilado por tres lados: sus BYTES (golden), su
// CONTENIDO («las tres cosas y nada más») y su FUENTE (el .md de este repo). Trozo de
// greeting_test.go, partido por tema.

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// goldenPassiveSessionNoticeV1 es la copia INDEPENDIENTE del literal, tecleada aquí para que el
// test tenga con qué comparar sin importar la constante que vigila. Si alguien toca
// passiveSessionNoticeV1 y no toca esto, el test se pone rojo — que es exactamente lo que se
// quiere: el texto es contrato, no una cadena más.
//
//nolint:gosec // G101: es el texto de un aviso público, no una credencial («passive» lleva «pass» dentro).
const goldenPassiveSessionNoticeV1 = `Tu WhatsApp quedó vinculado a wApp, y esta sesión nació en perfil PASIVA.

Qué significa: por esta sesión SOLO SE ENVÍAN mensajes. Lo que te escriban NO SALE
DE ESTE EQUIPO: se queda aquí y no sube a la nube, así que wApp todavía no responde
solo.

Para que responda, cambia el perfil de la sesión a ACTIVA desde el panel de wApp, o
llama a POST /api/v1/sessions/{id}/profile con {"profile":"active"}.`

// Los BYTES del aviso y de su ID. Cualquier cambio —una tilde, un salto de línea movido, una
// palabra «mejorada»— pone este test rojo, y esa es su única función: el texto viaja por DOS
// canales (este y la pantalla de wapp-ctl) que tienen que decir lo mismo.
func TestPassiveSessionNoticeGolden(t *testing.T) {
	t.Parallel()
	if passiveSessionNoticeV1 != goldenPassiveSessionNoticeV1 {
		t.Fatalf("el literal cambió sin actualizar su golden.\ntengo:\n%q\nquiero:\n%q",
			passiveSessionNoticeV1, goldenPassiveSessionNoticeV1)
	}
	if passiveSessionNoticeID != "AVISO_SESION_PASIVA_V1" { //nolint:gosec // G101: nombre de un aviso público, no una credencial
		t.Fatalf("el ID del literal = %q; si el texto cambia, el ID sube a _V2 y este test se actualiza con él",
			passiveSessionNoticeID)
	}
}

// El aviso dice las tres cosas y nada más, comprobado por contenido y no por parecido: (1)
// nació pasiva, (2) solo envía y lo entrante no sale de este equipo, (3) las dos vías de
// cambiarla (el panel y la API). Y viaja SIN MARCADO: un '*' sería negrita en WhatsApp y un
// asterisco literal en la pantalla de wapp-ctl, dos textos distintos por el mismo literal.
func TestPassiveSessionNoticeSaysTheThreeThingsAndNothingElse(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		"PASIVA",
		"SOLO SE ENVÍAN",
		"NO SALE",
		"DE ESTE EQUIPO",
		"ACTIVA",
		"POST /api/v1/sessions/{id}/profile",
	} {
		if !strings.Contains(passiveSessionNoticeV1, want) {
			t.Errorf("el aviso no dice %q", want)
		}
	}
	for _, forbidden := range []string{"*", "_", "#", "<b>", "**"} {
		if strings.Contains(passiveSessionNoticeV1, forbidden) {
			t.Errorf("el aviso lleva marcado %q: el contrato lo prohíbe (texto plano)", forbidden)
		}
	}
}

// noticeSource es la FUENTE ÚNICA del literal, leída por ruta relativa desde este paquete:
// con un nivel más que el paquete viejo (modulos/edge/grpc) son CUATRO `../` (T-11).
const noticeSource = "../../../../documentations/literal-aviso-sesion-pasiva.md"

// La constante coincide con el .md de este repo: cierra la única grieta que un golden en Go no
// puede cerrar solo, que la constante y su fuente diverjan.
//
// 🔴 SI EL FICHERO NO ESTÁ, ESTE TEST FALLA; NO SALTA. Mientras la fuente vivió fuera del repo,
// en un checkout suelto la lectura fallaba y el test se saltaba en silencio: el invariante
// quedaba sin vigilar justo donde no había nada más que lo vigilara. El fichero viaja con este
// git, así que su ausencia es el defecto.
func TestPassiveSessionNoticeMatchesItsSourceDocument(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(noticeSource)
	if err != nil {
		t.Fatalf("no se pudo leer %s: %v — es la fuente única del literal y vive en este repo, así que su ausencia es el defecto",
			noticeSource, err)
	}
	fromDocument, err := noticeBlock(string(raw))
	if err != nil {
		t.Fatalf("leyendo el literal de %s: %v", noticeSource, err)
	}
	if fromDocument != passiveSessionNoticeV1 {
		t.Fatalf("la constante y su documento DIVERGEN (el documento manda).\ndocumento:\n%q\nconstante:\n%q",
			fromDocument, passiveSessionNoticeV1)
	}
}

// noticeBlock extrae del documento el bloque ```text que sigue a la línea del ID del literal.
// Es un parser mínimo a propósito: si el documento cambia de forma, su error es más útil que
// una comparación que pasa por accidente contra el bloque equivocado.
func noticeBlock(md string) (string, error) {
	lines := strings.Split(md, "\n")
	i := 0
	for ; i < len(lines); i++ {
		if strings.Contains(lines[i], "ID del literal") && strings.Contains(lines[i], passiveSessionNoticeID) {
			break
		}
	}
	if i == len(lines) {
		return "", errors.New("el documento ya no declara el ID " + passiveSessionNoticeID)
	}
	for ; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "```text" {
			break
		}
	}
	if i == len(lines) {
		return "", errors.New("tras el ID del literal no hay ningún bloque ```text")
	}
	var body []string
	for j := i + 1; j < len(lines); j++ {
		if strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
			return strings.Join(body, "\n"), nil
		}
		body = append(body, lines[j])
	}
	return "", errors.New("el bloque ```text del aviso no se cierra")
}

// El parser no se conforma con cualquier bloque: sin la línea del ID, sin bloque tras ella o
// con el bloque sin cerrar, da error en vez de devolver un texto con el que comparar.
func TestNoticeBlockRejectsADocumentWithoutTheLiteral(t *testing.T) {
	t.Parallel()
	const idLine = "**ID del literal:** `AVISO_SESION_PASIVA_V1`\n"
	cases := map[string]string{
		"no id line":       "```text\nhola\n```\n",
		"id of another":    "**ID del literal:** `OTRO_V1`\n```text\nhola\n```\n",
		"no block":         idLine + "texto suelto\n",
		"block not closed": idLine + "```text\nhola\n",
	}
	for name, md := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got, err := noticeBlock(md); err == nil {
				t.Errorf("noticeBlock devolvió %q sin error", got)
			}
		})
	}
	got, err := noticeBlock("otro bloque\n```text\nno es este\n```\n" + idLine + "\n```text\nlínea 1\n\nlínea 3\n```\nresto\n")
	if err != nil || got != "línea 1\n\nlínea 3" {
		t.Errorf("noticeBlock = (%q, %v), se esperaba el bloque que SIGUE al ID", got, err)
	}
}
