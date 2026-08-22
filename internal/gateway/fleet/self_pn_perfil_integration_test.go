package fleet_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
)

// PIEZA 2 de T5.2 (Plan 046 · Ola 5, REQ-21): el anti-self-loop CON UNA SESIÓN PASIVA
// DELANTE, por el camino real y con el número escrito de otra forma.
//
// ── QUÉ FALTABA, EXACTAMENTE, Y POR QUÉ NO LO CUBRÍA NADA ─────────────────────────
// Las dos mitades existían por separado y ninguna se cruzaba con la otra:
//
//   · La semántica del PERFIL la prueban las suites de runtime
//     (self_numbers_integration_test.go: _ExcluyePassive, _SoloPassiveNoBloqueaNada,
//     _PassiveVivaConZombiActivoDelMismoNumero). Pero allí el índice ciego lo fabrica
//     el propio test con bidxDe — un lazo cerrado entre dos helpers que no ejecuta ni
//     una línea del escritor, como el fichero hermano ya documenta.
//   · La simetría ESCRITOR↔LECTOR con grafías adornadas la prueba
//     self_pn_cifrado_integration_test.go, con SetSelfPn y IsSelfNumber de verdad.
//     Pero su helper de sembrado fija el perfil en ACTIVE SIEMPRE, y lo dice: «una
//     pasiva no bloquea, y el caso quedaría probando otra cosa».
//
// ⇒ Nadie comprobaba la conjunción: que el veredicto correcto por perfil se mantiene
// cuando el bidx lo escribió el ESCRITOR DE PRODUCCIÓN a partir de un número
// ADORNADO. Y esa conjunción es la que corre en campo: el `self_pn` llega en el
// Heartbeat tal y como lo mande WhatsApp, no normalizado por nadie.
//
// 🔴 POR QUÉ IMPORTA QUE FALLE HACIA «NO BLOQUEA». Los dos veredictos son asimétricos
// en consecuencias. Si el bidx de una sesión ACTIVA sale mal, la guarda no bloquea y
// vuelve el bucle sesión↔sesión del Plan 019 —un mensaje cada 2,6 s—. Si sale mal el
// de una PASIVA, se bloquea al número personal del dueño y la sesión activa deja de
// atenderlo. Los dos fallos son MUDOS: no hay error, no hay log, y el valor en claro
// con el que comparar ya no existe (la 0070 borró la columna).
//
// Vive en fleet_test por las mismas tres razones que su fichero hermano, y sobre todo
// por la primera: aquí NO existe bidxDe, así que la única forma de poner un índice en
// la tabla es llamar a SetSelfPn de verdad. La disciplina la impone el paquete.

// sembrarSesionConPerfilYNumero es el gemelo de sembrarSesionActivaConNumero con el
// perfil ABIERTO como parámetro. No se generaliza aquel —se añade este— porque su
// docstring afirma «la deja en perfil ACTIVE» y un parámetro lo convertiría en una
// afirmación condicional en todos sus llamantes.
//
// Los tres pasos van por el repositorio REAL. En particular SetSelfPn, que es quien
// normaliza e indexa: es el punto entero de este fichero.
func sembrarSesionConPerfilYNumero(
	ctx context.Context, t *testing.T, repo *fleet.PostgresRepository,
	tenantID, edgeID, sessionID string, perfil fleet.Profile, numeroEscrito string,
) {
	t.Helper()
	if err := repo.MarkOnline(ctx, tenantID, edgeID, sessionID); err != nil {
		t.Fatalf("MarkOnline(%s): %v", sessionID, err)
	}
	if _, err := repo.SetProfile(ctx, tenantID, sessionID, perfil); err != nil {
		t.Fatalf("SetProfile(%s, %s): %v", sessionID, perfil, err)
	}
	if err := repo.SetSelfPn(ctx, tenantID, edgeID, sessionID, numeroEscrito); err != nil {
		t.Fatalf("SetSelfPn(%s): %v", sessionID, err)
	}
}

// TestIntegration_SelfPn_PerfilDecideConElNumeroAdornado recorre las TRES grafías del
// mismo teléfono contra los DOS perfiles, con un tenant por combinación.
//
// 🔴 UN TENANT POR COMBINACIÓN, y no seis sesiones bajo el mismo. IsSelfNumber agrega
// con bool_or sobre TODAS las filas que comparten bidx: la sesión activa de una
// grafía taparía a la pasiva de otra y el test se quedaría verde con la mutación
// puesta. Es la misma razón que su fichero hermano ya documenta, y aquí muerde el
// doble porque las dos mitades esperan veredictos OPUESTOS.
//
// 💥 MUTACIÓN QUE LO PONE ROJO POR EL LADO DEL PERFIL: cambiar el predicado
// `profile <> 'passive'` de self_numbers.go por `TRUE` (o borrar el HAVING) ⇒ las
// tres vueltas pasivas empiezan a bloquear.
//
// 💥 MUTACIÓN QUE LO PONE ROJO POR EL LADO DEL ÍNDICE: quitar la llamada a
// normalizeSelfPn de selfPnEnvelope (repository_postgres.go) ⇒ las dos grafías
// adornadas de la mitad ACTIVA dejan de casar. La mitad pasiva NO se entera —seguiría
// devolviendo false, que es lo que espera—, y ahí está la lección: un test que solo
// mirara el caso pasivo pasaría con el índice roto. Por eso las dos mitades van
// juntas en el mismo bucle y no en dos tests separados.
func TestIntegration_SelfPn_PerfilDecideConElNumeroAdornado(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo, kp := repoDePrueba(t, db)
	// Mismo KeyProvider para escritor y lector: es la situación de producción (un
	// keyring por proceso) y deja la NORMALIZACIÓN como único punto de divergencia.
	checker := runtime.NewPostgresSelfNumbers(db, kp)

	casos := []struct {
		perfil  fleet.Profile
		bloquea bool
		porque  string
	}{
		{fleet.ProfileActive, true,
			"una sesión ACTIVA sí auto-responde, así que un entrante desde su número puede " +
				"realimentar el bucle sesión↔sesión del Plan 019"},
		{fleet.ProfilePassive, false,
			"una pasiva NUNCA auto-responde (reactiveBlocked la corta antes), así que su número " +
				"no puede cerrar ningún bucle; bloquearlo solo impediría atender al teléfono " +
				"personal del dueño"},
	}

	for _, c := range casos {
		for _, g := range grafiasDelMismoNumero {
			tenantID := seedTenant(t, db)
			sembrarSesionConPerfilYNumero(ctx, t, repo, tenantID,
				"edge-perfil", "s-perfil", c.perfil, g.escrito)

			propio, err := checker.IsSelfNumber(ctx, tenantID, numeroPropioNormalizado)
			if err != nil {
				t.Fatalf("perfil %s · grafía %s: IsSelfNumber: %v", c.perfil, g.nombre, err)
			}
			if propio != c.bloquea {
				t.Fatalf("perfil %s · grafía %s: IsSelfNumber = %v, quiero %v — %s.\n"+
					"El número lo escribió SetSelfPn REAL a partir de la grafía adornada, así que "+
					"un veredicto equivocado aquí significa una de dos cosas, y las dos son mudas: "+
					"o el predicado por perfil dejó de filtrar, o escritor y lector dejaron de "+
					"normalizar igual y el bidx no casa consigo mismo",
					c.perfil, g.nombre, propio, c.bloquea, c.porque)
			}
		}
	}
}

// TestIntegration_SelfPn_PasivaYActivaDelMismoNumeroBloquean es el caso MIXTO, y es el
// que decide la semántica cuando las dos existen a la vez: el MISMO número emparejado
// dos veces en el mismo tenant, una sesión en pasiva y otra en activa.
//
// Tiene que BLOQUEAR. El predicado es `bool_or(profile <> 'passive')` y no un
// «todas»: basta UNA sesión activa con ese número para que un entrante desde él pueda
// realimentarse. La sesión pasiva no lo redime.
//
// 🔴 ESTE ES EL CASO QUE UN TEST POR SEPARADO NO PUEDE DAR. Con el número solo en
// pasiva el veredicto es false; con el número solo en activa, true. Que la mezcla dé
// true es una decisión de diseño —conservadora hacia NO auto-responder— y sin este
// caso, cambiar el bool_or por un bool_and pasaría los dos tests anteriores.
//
// Existe un hermano en runtime (_PassiveVivaConZombiActivoDelMismoNumero) que afirma
// lo mismo sobre la QUERY con bidx fabricado; este lo afirma sobre el camino real, y
// además con las dos sesiones sembradas desde grafías DISTINTAS del mismo teléfono:
// si el escritor no normalizara, serían dos números diferentes y el caso mixto no
// llegaría a formarse.
func TestIntegration_SelfPn_PasivaYActivaDelMismoNumeroBloquean(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo, kp := repoDePrueba(t, db)
	checker := runtime.NewPostgresSelfNumbers(db, kp)
	tenantID := seedTenant(t, db)

	// La pasiva con la grafía canónica y la activa con la adornada: si el escritor
	// dejara de normalizar, las dos filas tendrían bidx distintos y la mezcla no
	// existiría — el test caería por el lado correcto igualmente.
	sembrarSesionConPerfilYNumero(ctx, t, repo, tenantID, "edge-mixto", "s-pasiva",
		fleet.ProfilePassive, grafiasDelMismoNumero[0].escrito)
	sembrarSesionConPerfilYNumero(ctx, t, repo, tenantID, "edge-mixto", "s-activa",
		fleet.ProfileActive, grafiasDelMismoNumero[2].escrito)

	propio, err := checker.IsSelfNumber(ctx, tenantID, numeroPropioNormalizado)
	if err != nil {
		t.Fatalf("IsSelfNumber en el caso mixto: %v", err)
	}
	if !propio {
		t.Fatal("el número está emparejado en una sesión PASIVA y en una ACTIVA del mismo tenant, " +
			"y el veredicto fue «no es propio». Con una sesión activa viva ese número SÍ puede " +
			"realimentar el bucle: el predicado es bool_or(profile <> 'passive'), no un «todas». " +
			"Si esto sale en verde con un bool_and, la pasiva estaría redimiendo a la activa")
	}
}
