//go:build integracion && edgereal

package procesos

// El test propio del modo «Edge real»: lo que el arnés promete de la escena antes de que ningún
// caso converse por ella.

import (
	"testing"
)

// edgeRealPassivePn es el cliente que escribe con la sesión todavía pasiva.
const edgeRealPassivePn = "573001110072"

// TestEdgeReal_BootsTheRealBinary monta la escena y comprueba lo que da por hecho todo caso con
// Edge real:
//
//   - Corre el binario del repo del Edge, con la palanca del WhatsApp falso gritada y el CloudLink
//     con validador de lease y sellado (lo afirma edgeRealStart, antes que nada).
//   - El Edge arrancó sin device y sin red hacia WhatsApp, y la sesión nació por el emparejamiento
//     de siempre: el servidor la conoce, pasiva, y le mandó a su número propio el aviso de sesión
//     pasiva —el literal congelado—, que el Edge «envió» (quedó capturado, y nada más).
//   - La DEK de la sesión la generó y la custodia el Edge, por el camino de siempre: en el
//     contenedor no hay llavero y cae en su último recurso, un fichero de 32 bytes que solo lee su
//     dueño (D-FW-7: la custodia no se tocó para poder probar). El caso mira el fichero, no su
//     contenido; y la DEK no aparece en ninguna tabla del servidor porque no cruza el contrato.
//   - Con la sesión pasiva, un entrante se corta en la PUERTA del Edge: su contador de descartes
//     por perfil pasivo sube en uno y no sale ningún texto. El inyector lo acusa igual, así que el
//     contador es la única prueba de que el corte existe.
func TestEdgeReal_BootsTheRealBinary(t *testing.T) {
	t.Parallel()
	sc := edgeRealNewScene(t, "erboot", "er-arranque")
	sc.expectText(t, sc.SelfPn, p3PassiveNotice(t))
	edgeEsperarValor(t, sc.DB, "true", "greeted_at tras el Ack del aviso",
		`SELECT (greeted_at IS NOT NULL)::text FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`,
		sc.Tenant, sc.Edge.EdgeID, sc.SessionID)

	stat, err := sc.Edge.run(t.Context(), "stat", "-c", "%a %s", edgeRealData+"/keys/"+sc.SessionID+".key")
	if err != nil || stat != "600 32" {
		t.Errorf("la custodia de la DEK en el contenedor: %q, %v; quería un fichero 600 de 32 bytes", stat, err)
	}

	_, before := sc.health(t)
	sc.say(t, edgeRealPassivePn, "PASSIVE-1", cartKeyword)
	edgeEsperar(t, edgeTopeFila, "que el Edge real descarte el entrante de la sesión pasiva", func() bool {
		_, now := sc.health(t)
		return now == before+1
	})
	sc.expectNoPendingText(t, "entrante con la sesión pasiva")
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.contacts WHERE tenant_id = $1::uuid`, sc.Tenant); n != 0 {
		t.Errorf("con la sesión pasiva hay %d filas en contacts, quería 0", n)
	}
	edgeSinErrores(t, sc.S, nil)
}
