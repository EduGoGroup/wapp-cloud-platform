package reanalisis_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/reanalisis"
)

// reanalisis_test.go — LOS CRITERIOS DE T4.6 sobre el caso de uso, uno por test.
//
// Los contract tests de los SEIS códigos HTTP viven en
// internal/publicapi/reanalyze_test.go (allí se mira el CUERPO que sale por el
// cable); aquí se mira la DECISIÓN y, sobre todo, lo que se escribe y lo que no.
//
// 🔴 NINGUNO DE ESTOS TESTS SE SALTA. Los `TestPostgres_*` de este repo se saltan
// enteros sin `WAPP_TEST_DB_DSN` —hay 89 así— y un criterio cubierto solo por uno de
// ellos no lo está probando nadie. Todo lo de este fichero corre con dobles.

// textoDelCliente es la frase del hilo. Es literal y no un fixture generado porque
// varios tests afirman que NO aparece en el log, y para eso hay que poder buscarla.
const textoDelCliente = "quiero 1 hamburguesa con queso y cebolla"

// ---------------------------------------------------------------------------
// EL CAMINO FELIZ
// ---------------------------------------------------------------------------

// TestReanalizar_AbreElJobConTodoSuContexto es el 200 del §8.1 y, de paso, la
// afirmación de que las CUATRO columnas de la 0080 se escriben con lo que toca.
func TestReanalizar_AbreElJobConTodoSuContexto(t *testing.T) {
	t.Parallel()
	b := bancoDe(t)

	out, err := b.pide(t, reanalisis.Solicitud{})
	require.NoError(t, err)

	require.Equal(t, intakeDePrueba, out.IntakeID)
	require.Equal(t, jobDePrueba, out.JobID)
	require.Equal(t, "local", out.Via, "sin fila en tenant_llm la vía efectiva es local (D-044.48 §4)")
	require.Equal(t, reanalisis.EstadoEnCurso, out.Status)
	require.Equal(t, 2, out.RevisionNo, "la solicitud iba por la 1: la nueva será la 2")

	require.Len(t, b.jobs.abiertos, 1)
	abierto := b.jobs.abiertos[0]
	require.Equal(t, intake.WindowKey{
		TenantID: tenantDePrueba, SessionID: sesionDePrueba,
		ContactID: contactoDePrueba, EventID: eventoDePrueba,
	}, abierto.Key, "la clave de ventana sale de la solicitud, no del cuerpo")
	require.Equal(t, intakeDePrueba, abierto.IntakeID, "el job nace apuntando al intake que ya existe")
	require.Equal(t, intake.Reanalisis{
		RequestedBy: intake.RequestedByOwner,
		Via:         "local",
		Source:      stages.OrigenHiloDelEvento,
		From:        1,
	}, abierto.Contexto)

	require.Equal(t, []intake.WindowKey{abierto.Key}, b.compositor.claves,
		"el sobre se compone para la MISMA ventana del job recién abierto")
}

// TestReanalizar_ElHiloSePideConElLimiteDelCompositor sostiene que la comprobación
// de fuente mira EXACTAMENTE las entradas que se van a componer.
//
// Si divergieran, un hilo largo podría pasar la comprobación y componerse vacío —o al
// revés— y el job nacería sin sobre para morir después sin que nadie sepa por qué.
func TestReanalizar_ElHiloSePideConElLimiteDelCompositor(t *testing.T) {
	t.Parallel()
	b := bancoDe(t)

	_, err := b.pide(t, reanalisis.Solicitud{})
	require.NoError(t, err)
	require.Equal(t, runtime.DefaultThreadLimit, b.hilo.limite)
}

// ---------------------------------------------------------------------------
// 🔴 EL CANDADO DEL ORDEN — «el orden de los chequeos ES CONTRATO» (§8.1)
// ---------------------------------------------------------------------------
//
// # POR QUÉ ESTE BLOQUE EXISTE, Y ES UNA LECCIÓN CARA
//
// Estos tres tests nacieron de una MUTACIÓN QUE NO SE CAZABA: mover `objetivoDe` y
// `origenDelMaterial` DELANTE de `autorizar` —o sea, invertir el orden que el propio
// fichero de producción documenta como contrato— dejaba los 71 tests en verde, con
// `vet` y `test` a cero.
//
// El motivo es instructivo: los candados que había protegían la parte FINA (dónde cae
// cada mitad del 400 partido) y ninguno la GRUESA (el gate gana a la SOLICITUD y a la
// FUENTE). Con el orden invertido, un tenant SIN `llm_intake` recibe un `422
// source_unavailable` o un `404` en vez de un `403`: se le confirma si el evento tiene
// material, o si esa solicitud es suya, sin que tenga derecho a preguntar. Es una fuga
// de existencia, no un código mal puesto, y ningún assert sobre «el error que devuelve
// la función» la ve.
//
// # POR QUÉ UN CANDADO DE LA SECUENCIA Y NO DOS TESTS SUELTOS
//
// Porque el orden son NUEVE escalones y una mutación solo prueba uno. Dos tests
// sueltos dejarían las otras ocho permutaciones sin vigilar, y la próxima tarea que
// toque `Reanalizar` reordenaría sin enterarse. Es el mismo criterio con el que se
// custodia C2 con un test de AST: se protege LA REGLA, no una instancia de la regla.
//
// El mecanismo es una BITÁCORA compartida por los seis dobles (ver dobles_test.go):
// cada puerto anota su escalón al ser preguntado, y aquí se afirma la lista entera.

// TestReanalizar_LaSecuenciaDeEscalonesEsELCONTRATO afirma el orden COMPLETO del §8.1
// sobre el camino que recorre todos los escalones: vía `api` configurada y completa
// (para que aparezca el gate de la vía) más `text` pegado (para que aparezcan el
// dedupe y la escritura del hilo).
//
// La lista se compara ENTERA y en orden: `require.Equal` sobre el slice, no un
// `Contains` por elemento. Un `Contains` pasaría con cualquier permutación, que es
// exactamente lo que este test existe para impedir.
func TestReanalizar_LaSecuenciaDeEscalonesEsELCONTRATO(t *testing.T) {
	t.Parallel()
	b := bancoDe(t, configAPI, func(b *banco) {
		b.features.tiene[entitlements.FeatureAPILLM] = true
	})

	_, err := b.pide(t, reanalisis.Solicitud{Via: "api", Text: "son 30 tequeños crudos"})
	require.NoError(t, err)

	require.Equal(t, []string{
		// autorizar: el NIVEL primero, después la vía, después el gate de la vía.
		pasoGateNivel,
		pasoVia,
		pasoGateVia,
		// (la credencial no toca ningún puerto: sale de la Config que ya se leyó)
		// objetivoDe: la solicitud y su concurrencia.
		pasoSolicitud,
		pasoJobVivo,
		// la FUENTE, la última de las lecturas (§8.1: «Comprobar la fuente al final
		// evita filtrar si el evento existe a quien ni siquiera tiene la feature»).
		pasoFuente,
		// y solo entonces se ESCRIBE.
		pasoDedupe,
		pasoEscribeHilo,
		pasoAbreJob,
		pasoCompone,
	}, b.pasos())
}

// TestReanalizar_SinNivel_NiSeMiraLaFUENTE es el caso 1 de la fuga: el tenant no tiene
// `llm_intake` y su evento no tiene material.
//
// La respuesta tiene que ser 403 y NO `422 source_unavailable`. Con el orden invertido
// sería 422 — o sea, se le confirmaría que ese evento no tiene original guardado a
// quien ni siquiera ha comprado la capacidad de mirarlo.
//
// El assert fuerte es la bitácora: al hilo NO SE LE PREGUNTÓ. No basta con mirar el
// código de error, porque una implementación podría leer el hilo y descartar el
// resultado — y la lectura ya habría ocurrido.
func TestReanalizar_SinNivel_NiSeMiraLaFUENTE(t *testing.T) {
	t.Parallel()
	b := bancoDe(t, func(b *banco) {
		b.features.tiene = map[string]bool{}
		b.hilo.entradas = nil // el evento NO tiene material: con el orden invertido, 422
	})

	_, err := b.pide(t, reanalisis.Solicitud{})

	var feat reanalisis.FeatureAusenteError
	require.ErrorAs(t, err, &feat)
	require.Equal(t, entitlements.FeatureLLMIntake, feat.Feature)
	var fuente reanalisis.FuenteAusenteError
	require.False(t, errors.As(err, &fuente), "sin la capacidad no se contesta si hay material")

	require.Equal(t, []string{pasoGateNivel}, b.pasos(),
		"el gate del nivel corta ANTES de tocar ningún otro puerto")
}

// TestReanalizar_SinNivel_NiSeMiraLaSOLICITUD es el caso 2: el tenant no tiene
// `llm_intake` y el intake es de OTRO tenant.
//
// 403 y NO 404. Con el orden invertido sería 404, que suena inocuo y no lo es: 403 y
// 404 son dos respuestas distintas a dos preguntas distintas, y quien no tiene la
// capacidad no debe poder distinguir «esa solicitud no es tuya» de «no tienes esta
// función». Recibir 404 para unos ids y 403 para otros convierte el endpoint en un
// oráculo de qué solicitudes existen en OTROS tenants (INV-8).
func TestReanalizar_SinNivel_NiSeMiraLaSOLICITUD(t *testing.T) {
	t.Parallel()
	b := bancoDe(t, func(b *banco) {
		b.features.tiene = map[string]bool{}
		b.solicitudes.err = intakes.ErrNotFound // de otro tenant: con el orden invertido, 404
	})

	_, err := b.pide(t, reanalisis.Solicitud{})

	var feat reanalisis.FeatureAusenteError
	require.ErrorAs(t, err, &feat)
	require.NotErrorIs(t, err, intakes.ErrNotFound, "sin la capacidad no se contesta si la solicitud existe")

	require.Equal(t, []string{pasoGateNivel}, b.pasos())
	require.Zero(t, b.solicitudes.llamadas, "ni siquiera se fue a buscar la solicitud")
}

// TestReanalizar_LaViaLocal_NiSeMiraElGateDeLaVIA es la tercera cara de lo mismo, por
// el otro lado: con la vía efectiva `local` la secuencia SALTA el gate de la vía, y
// eso también es orden — es el invariante de D-044.28 visto como posición y no como
// ausencia.
func TestReanalizar_LaViaLocal_NiSeMiraElGateDeLaVIA(t *testing.T) {
	t.Parallel()
	b := bancoDe(t)

	_, err := b.pide(t, reanalisis.Solicitud{})
	require.NoError(t, err)

	require.Equal(t, []string{
		pasoGateNivel, pasoVia,
		pasoSolicitud, pasoJobVivo, pasoFuente,
		pasoAbreJob, pasoCompone,
	}, b.pasos(), "sin `text` no hay dedupe ni escritura del hilo; y en local no hay gate de vía")
}

// ---------------------------------------------------------------------------
// 400 · invalid_via
// ---------------------------------------------------------------------------

func TestReanalizar_ViaFueraDelVocabulario_400(t *testing.T) {
	t.Parallel()
	b := bancoDe(t)

	_, err := b.pide(t, reanalisis.Solicitud{Via: "chatgpt"})

	var via reanalisis.ViaInvalidaError
	require.ErrorAs(t, err, &via)
	require.Equal(t, "chatgpt", via.Via)
	require.Empty(t, via.Configurada, "el rechazo de VOCABULARIO no habla de la vía configurada")
	b.exigeCeroEscrituras(t)
}

// TestReanalizar_LaFormaGanaAlGate es el ORDEN del §8.1 escrito como test: «400,
// validación de forma, ANTES de cualquier gate».
//
// 🔴 ES LA RAZÓN POR LA QUE EL GATE NO ES UN MIDDLEWARE. Con `RequireFeature` en la
// cadena de la ruta, este caso respondería 403 —el middleware corre antes que el
// handler, por definición— y el orden del contrato dejaría de ser verdad sin que
// ningún test lo viera. La mutación que lo caza: mover el gate a publicapi.go.
func TestReanalizar_LaFormaGanaAlGate(t *testing.T) {
	t.Parallel()
	b := bancoDe(t, func(b *banco) { b.features.tiene = map[string]bool{} })

	_, err := b.pide(t, reanalisis.Solicitud{Via: "chatgpt"})

	var via reanalisis.ViaInvalidaError
	require.ErrorAs(t, err, &via, "un tenant SIN llm_intake y con vía inválida recibe 400, no 403")
	require.Empty(t, b.features.preguntas, "ni siquiera se preguntó por las features")
}

// TestReanalizar_SinLLMIntake_ElGateGanaAlaViaQueNoCoincide fija la ÚNICA desviación
// del orden del §8.1, para que sea una decisión y no una casualidad.
//
// El contrato pone todo el 400 «antes de cualquier gate». Aquí ese 400 está PARTIDO:
// la mitad de vocabulario va delante (TestReanalizar_LaFormaGanaAlGate), pero la de
// COINCIDENCIA no —hay que leer `tenant_llm` para saberla, y su cuerpo publica
// `configured_via`, o sea la configuración LLM del tenant—. Contestársela a quien no
// tiene la capacidad sería responder antes de gatear, que es exactamente lo que el
// contrato evita dejando la FUENTE para el final.
func TestReanalizar_SinLLMIntake_ElGateGanaAlaViaQueNoCoincide(t *testing.T) {
	t.Parallel()
	b := bancoDe(t, configAPI, func(b *banco) { b.features.tiene = map[string]bool{} })

	// `local` contradice la vía configurada (`api`), así que con la feature saldría 400.
	_, err := b.pide(t, reanalisis.Solicitud{Via: "local"})

	var feat reanalisis.FeatureAusenteError
	require.ErrorAs(t, err, &feat, "sin llm_intake no se contesta la configuración del tenant")
	require.Equal(t, entitlements.FeatureLLMIntake, feat.Feature)
	var via reanalisis.ViaInvalidaError
	require.False(t, errors.As(err, &via))
	b.exigeCeroEscrituras(t)
}

// TestReanalizar_ViaQueContradiceLaConfigurada_400 es la mitad de REQ-33 que T4.6
// tenía que ratificar: a quien YA eligió vía no se le conmuta por parámetro.
func TestReanalizar_ViaQueContradiceLaConfigurada_400(t *testing.T) {
	t.Parallel()
	b := bancoDe(t, configAPI)

	_, err := b.pide(t, reanalisis.Solicitud{Via: "local"})

	var via reanalisis.ViaInvalidaError
	require.ErrorAs(t, err, &via)
	require.Equal(t, "local", via.Via)
	require.Equal(t, "api", via.Configurada, "el cuerpo dice cuál SÍ vale, para que la UI no mande a otra pantalla")
	b.exigeCeroEscrituras(t)
}

// TestReanalizar_SinFila_ViaAPI_400_YNoLlegaALosGates es EL CASO QUE CORRE EN CAMPO,
// y por eso tiene test propio: los TRES tenants de UAT no tienen fila en `tenant_llm`
// —esa tabla está vacía— y D-044.48 §4 dice que ese es «el estado por defecto de todo
// tenant nuevo, no una anomalía del entorno».
//
// 🔴 SIN FILA LA VÍA EFECTIVA ES `local`, QUE ES UN VALOR REAL Y NO UNA AUSENCIA
// (D-044.48 §4). Así que `{"via":"api"}` la CONTRADICE igual que la contradiría con
// fila, y la respuesta es 400 — una regla, no dos.
//
// 🔧 Este test nació de un DEFECTO: `resolverVia` llevaba un `hayFila &&` en la
// comparación, que desactivaba la regla exactamente aquí. El caso salía por el 422 de
// credencial, o sea un código distinto y más abajo justo para el único estado que
// existe hoy. Los otros casos seguían verdes; sin este test el `hayFila &&` puede
// volver mañana y nadie se entera.
//
// El espía de features es la mitad importante: no basta con que el código sea 400 —
// hay que afirmar que la petición MUERE ANTES de los dos gates de la rama API. Si
// llegara a preguntarlos, el rechazo estaría ocurriendo por otro motivo y el orden del
// §8.1 dejaría de ser verdad.
func TestReanalizar_SinFila_ViaAPI_400_YNoLlegaALosGates(t *testing.T) {
	t.Parallel()
	b := bancoDe(t, func(b *banco) {
		// El tenant lo tiene TODO: la capacidad, el add-on de la vía API. Lo único que
		// no tiene es fila en `tenant_llm`. Aun así son 400, no 403 ni 422.
		b.features.tiene[entitlements.FeatureAPILLM] = true
	})

	_, err := b.pide(t, reanalisis.Solicitud{Via: "api"})

	var via reanalisis.ViaInvalidaError
	require.ErrorAs(t, err, &via, "sin fila, `api` contradice la vía efectiva (`local`): es 400")
	require.Equal(t, "api", via.Via)
	require.Equal(t, "local", via.Configurada, "el cuerpo dice cuál SÍ vale")

	require.False(t, b.features.preguntoPor(entitlements.FeatureAPILLM),
		"el 400 es de FORMA/configuración y muere antes del gate de la vía")
	require.False(t, b.features.preguntoPor(entitlements.FeatureLLMIntake) && len(b.jobs.abiertos) > 0,
		"y desde luego antes de escribir nada")
	b.exigeCeroEscrituras(t)
}

// TestReanalizar_ConFilaLocal_ViaAPI_400 es el mismo rechazo con el tenant que SÍ
// eligió: a quien ya configuró su vía no se le conmuta por parámetro (REQ-33).
func TestReanalizar_ConFilaLocal_ViaAPI_400(t *testing.T) {
	t.Parallel()
	b := bancoDe(t, configLocal, func(b *banco) {
		b.features.tiene[entitlements.FeatureAPILLM] = true
	})

	_, err := b.pide(t, reanalisis.Solicitud{Via: "api"})

	var via reanalisis.ViaInvalidaError
	require.ErrorAs(t, err, &via)
	require.Equal(t, "local", via.Configurada)
	require.False(t, b.features.preguntoPor(entitlements.FeatureAPILLM))
	b.exigeCeroEscrituras(t)
}

// TestReanalizar_ViaQueCOINCIDE_Pasa: afirmar la vía correcta no es un rechazo. Es la
// otra mitad de «afirmar»: el campo existe para poder decir «sé que voy por local y
// quiero ir por local», y eso tiene que pasar.
func TestReanalizar_ViaQueCOINCIDE_Pasa(t *testing.T) {
	t.Parallel()
	for nombre, c := range map[string]struct {
		ajuste func(*banco)
		via    string
	}{
		"sin fila, afirmando local":       {ajuste: func(*banco) {}, via: "local"},
		"con fila local, afirmando local": {ajuste: configLocal, via: "local"},
	} {
		t.Run(nombre, func(t *testing.T) {
			t.Parallel()
			b := bancoDe(t, c.ajuste)

			out, err := b.pide(t, reanalisis.Solicitud{Via: c.via})

			require.NoError(t, err)
			require.Equal(t, c.via, out.Via)
		})
	}
}

// ---------------------------------------------------------------------------
// 403 · feature_not_enabled (llm_intake) y (api_llm)
// ---------------------------------------------------------------------------

func TestReanalizar_SinLLMIntake_403(t *testing.T) {
	t.Parallel()
	b := bancoDe(t, func(b *banco) { b.features.tiene = map[string]bool{} })

	_, err := b.pide(t, reanalisis.Solicitud{})

	var feat reanalisis.FeatureAusenteError
	require.ErrorAs(t, err, &feat)
	require.Equal(t, entitlements.FeatureLLMIntake, feat.Feature)
	b.exigeCeroEscrituras(t)
}

// TestReanalizar_ResolverCaido_FailClosed: la política del middleware, aplicada en
// código. Un resolver caído responde «no la tienes» y no 500 — el llamante no debe
// poder distinguir «no lo tienes» de «no pude averiguarlo», porque un 5xx invita a
// reintentar hasta colarse.
func TestReanalizar_ResolverCaido_FailClosed(t *testing.T) {
	t.Parallel()
	b := bancoDe(t, func(b *banco) { b.features.err = errInfra })

	_, err := b.pide(t, reanalisis.Solicitud{})

	var feat reanalisis.FeatureAusenteError
	require.ErrorAs(t, err, &feat)
	require.Equal(t, entitlements.FeatureLLMIntake, feat.Feature)
	require.NotErrorIs(t, err, errInfra, "el fallo de infraestructura NO se propaga: sería un 500")
}

// TestReanalizar_ViaAPISinAPILLM_403 es el 403 de la VÍA, y solo puede ocurrir con la
// vía efectiva `api`: un tenant que configuró la API y después perdió el add-on.
func TestReanalizar_ViaAPISinAPILLM_403(t *testing.T) {
	t.Parallel()
	b := bancoDe(t, configAPI)

	_, err := b.pide(t, reanalisis.Solicitud{})

	var feat reanalisis.FeatureAusenteError
	require.ErrorAs(t, err, &feat)
	require.Equal(t, entitlements.FeatureAPILLM, feat.Feature)
	b.exigeCeroEscrituras(t)
}

// TestReanalizar_LaViaLocalNoPreguntaPorAPILLM es el INVARIANTE de D-044.28 /
// ADR-0044 aplicado a esta puerta: `api_llm` gatea LA VÍA, no la capacidad.
//
// Un tenant con `llm_intake` y sin `api_llm` es un tenant VÁLIDO en vía local, y su
// re-análisis tiene que funcionar entero. El espía afirma que por esa clave no se
// pregunta ni una vez — misma forma que
// internal/flujos/runtime/via_local_sin_api_llm_test.go.
func TestReanalizar_LaViaLocalNoPreguntaPorAPILLM(t *testing.T) {
	t.Parallel()
	for nombre, ajuste := range map[string]func(*banco){
		"sin fila en tenant_llm": func(*banco) {},
		"con fila via=local":     configLocal,
	} {
		t.Run(nombre, func(t *testing.T) {
			t.Parallel()
			b := bancoDe(t, ajuste)

			_, err := b.pide(t, reanalisis.Solicitud{})
			require.NoError(t, err)

			require.True(t, b.features.preguntoPor(entitlements.FeatureLLMIntake))
			require.False(t, b.features.preguntoPor(entitlements.FeatureAPILLM),
				"la vía local NO puede preguntar por api_llm (ADR-0044 · D-044.28)")
		})
	}
}

// ---------------------------------------------------------------------------
// 422 · llm_credentials_missing
// ---------------------------------------------------------------------------

// TestReanalizar_ViaAPISinCredencial_422 recorre los DOS estados de fila incompleta:
// sin clave, y con clave sin consentimiento.
//
// 🔴 ESTE TEST FABRICA UN ESTADO QUE LA BASE NO PUEDE PRODUCIR, y hay que decirlo en
// vez de disimularlo. Desde que `via` AFIRMA (D-044.51), a este 422 solo se llega con
// la vía efectiva en `api`, y eso exige una fila `via='api'` — que el CHECK
// `tenant_llm_via_api_completa_check` de la 0073 garantiza COMPLETA (proveedor,
// modelo, sobre entero y `consented_at`). O sea que `llm_credentials_missing` es
// DEFENSA EN PROFUNDIDAD: lo alcanza un restore parcial o un `UPDATE` a mano, no un
// cliente bien formado.
//
// Se escribe igual, y por dos razones: el contrato §8.1 lo publica, y una fila
// corrupta tiene que salir por un error NOMBRADO y no por un 500 desnudo.
//
// ⚠️ Y desfasa un criterio de T4.6 que pedía «tenant CON feature y SIN fila ⇒ 422,
// nunca 403»: con la regla ratificada ese caso responde **400** antes de mirar
// ninguna feature (ver TestReanalizar_SinFila_ViaAPI_400_YNoLlegaALosGates).
//
// 🔴 EL CONSENTIMIENTO CUENTA IGUAL QUE LA CLAVE (ADR-0030 D-01/§4): una fila con
// credencial y sin `consented_at` es un tenant que dejó la clave preparada y NO
// autorizó que el texto de sus clientes salga hacia un tercero.
func TestReanalizar_ViaAPISinCredencial_422(t *testing.T) {
	t.Parallel()
	casos := map[string]func(*banco){
		"con fila api sin clave": func(b *banco) {
			configAPI(b)
			b.config.cfg.HasAPIKey = false
		},
		"con fila api sin consentimiento": func(b *banco) {
			configAPI(b)
			b.config.cfg.ConsentedAt = timeCero
		},
	}
	for nombre, roto := range casos {
		t.Run(nombre, func(t *testing.T) {
			t.Parallel()
			b := bancoDe(t, roto, func(b *banco) {
				b.features.tiene[entitlements.FeatureAPILLM] = true
			})

			_, err := b.pide(t, reanalisis.Solicitud{})

			var cred reanalisis.CredencialAusenteError
			require.ErrorAs(t, err, &cred)
			require.Equal(t, "api", cred.Via)
			var feat reanalisis.FeatureAusenteError
			require.False(t, errors.As(err, &feat), "es 422 de credencial, NUNCA el 403 del paywall")
			b.exigeCeroEscrituras(t)
		})
	}
}

// ---------------------------------------------------------------------------
// 404 · la solicitud no es del tenant
// ---------------------------------------------------------------------------

func TestReanalizar_SolicitudAjena_404(t *testing.T) {
	t.Parallel()
	b := bancoDe(t, func(b *banco) { b.solicitudes.err = intakes.ErrNotFound })

	_, err := b.pide(t, reanalisis.Solicitud{})

	require.ErrorIs(t, err, intakes.ErrNotFound)
	b.exigeCeroEscrituras(t)
}

// ---------------------------------------------------------------------------
// 422 · reanalysis_in_progress
// ---------------------------------------------------------------------------

// TestReanalizar_JobVivo_422 cubre a la vez la concurrencia de D-044.15 y la CARRERA
// con la ventana viva del cliente: `aggregating` es un estado no terminal, así que un
// re-análisis pedido en mitad de una ráfaga encuentra ese job y sale por aquí.
func TestReanalizar_JobVivo_422(t *testing.T) {
	t.Parallel()
	b := bancoDe(t, func(b *banco) { b.jobs.vivo = jobDePrueba })

	_, err := b.pide(t, reanalisis.Solicitud{})

	var enCurso reanalisis.EnCursoError
	require.ErrorAs(t, err, &enCurso)
	require.Equal(t, jobDePrueba, enCurso.JobID, "el cuerpo lleva el job para poder seguirlo")
	b.exigeCeroEscrituras(t)
}

// ---------------------------------------------------------------------------
// 422 · source_unavailable, con sus DOS razones
// ---------------------------------------------------------------------------

// TestReanalizar_SinMaterial_422 es la frontera entre `purged` y `never_stored`, que
// es lo que decide qué se le dice al dueño.
func TestReanalizar_SinMaterial_422(t *testing.T) {
	t.Parallel()
	casos := map[string]struct {
		entradas []events.ThreadEntry
		razon    string
	}{
		"ni una fila message": {
			entradas: []events.ThreadEntry{
				// Solo CONTEXTO: un resumen del sistema y un saliente fuera de turno.
				// Ninguno de los dos es material del cliente (REQ-10b, D-044.24) y un
				// `source_text` hecho solo de esto es el accidente que D-044.24 describe.
				{Seq: 1, Role: events.RoleSystem, Kind: events.KindSummary, Text: "resumen"},
				{Seq: 2, Role: events.RoleBusiness, Kind: events.KindMessageOutOfTurn, Text: "seguimos aquí"},
			},
			razon: reanalisis.RazonNuncaGuardada,
		},
		"hilo vacío del todo": {
			entradas: nil,
			razon:    reanalisis.RazonNuncaGuardada,
		},
		"filas message con el cuerpo vaciado": {
			entradas: []events.ThreadEntry{
				{Seq: 1, Role: events.RoleClient, Kind: events.KindMessage, Text: ""},
				{Seq: 2, Role: events.RoleBusiness, Kind: events.KindMessage, Text: ""},
			},
			razon: reanalisis.RazonPurgada,
		},
	}
	for nombre, c := range casos {
		t.Run(nombre, func(t *testing.T) {
			t.Parallel()
			b := bancoDe(t, func(b *banco) { b.hilo.entradas = c.entradas })

			_, err := b.pide(t, reanalisis.Solicitud{})

			var fuente reanalisis.FuenteAusenteError
			require.ErrorAs(t, err, &fuente)
			require.Equal(t, c.razon, fuente.Reason)
			b.exigeCeroEscrituras(t)
		})
	}
}

// TestReanalizar_SolicitudLegadaSinEvento_422 es el pedido pre-0054: existe, el dueño
// lo está mirando, y no cuelga de ningún evento. Es «no hay original guardado» y NO un
// 404 — devolver 404 de algo que la bandeja está enseñando sería mentir.
func TestReanalizar_SolicitudLegadaSinEvento_422(t *testing.T) {
	t.Parallel()
	b := bancoDe(t, func(b *banco) { b.solicitudes.objetivo.EventID = "" })

	_, err := b.pide(t, reanalisis.Solicitud{})

	var fuente reanalisis.FuenteAusenteError
	require.ErrorAs(t, err, &fuente)
	require.Equal(t, reanalisis.RazonNuncaGuardada, fuente.Reason)
	b.exigeCeroEscrituras(t)
}

// ---------------------------------------------------------------------------
// EL `text` PEGADO (REQ-32 / D-044.17, cierre de MD-044.2)
// ---------------------------------------------------------------------------

// TestReanalizar_TextoPegado_UnaFilaYOrigenBoth cubre la primera mitad del criterio
// extra: una fila nueva, y el origen pasa a `both`.
func TestReanalizar_TextoPegado_UnaFilaYOrigenBoth(t *testing.T) {
	t.Parallel()
	b := bancoDe(t)

	_, err := b.pide(t, reanalisis.Solicitud{Text: "  son 30  tequeños\ncrudos "})
	require.NoError(t, err)

	require.Equal(t, []string{"son 30 tequeños crudos"}, b.hilo.escritas,
		"se guarda el texto SANEADO por cart.SanitizeNote: saltos a espacio y espacios colapsados")
	require.Equal(t, stages.OrigenAmbos, b.jobs.abiertos[0].Contexto.Source)
}

// TestReanalizar_TextoPegadoRepetido_SigueHabiendoUNA es la segunda mitad, y el
// criterio literal: «repetir la llamada con el mismo `text` ⇒ sigue habiendo una».
//
// El dedupe compara el hash del texto SANEADO, así que la segunda llamada con espacios
// distintos tiene que reconocerse igual: comparar el crudo dejaría entrar el mismo
// texto con un espacio de más.
func TestReanalizar_TextoPegadoRepetido_SigueHabiendoUNA(t *testing.T) {
	t.Parallel()
	b := bancoDe(t)

	_, err := b.pide(t, reanalisis.Solicitud{Text: "son 30 tequeños crudos"})
	require.NoError(t, err)
	_, err = b.pide(t, reanalisis.Solicitud{Text: "son 30   tequeños crudos  "})
	require.NoError(t, err)

	require.Len(t, b.hilo.escritas, 1, "el segundo re-análisis con el mismo texto NO duplica la fila")
	require.Len(t, b.jobs.abiertos, 2, "pero SÍ abre su job: dos re-análisis son dos actos")
}

// TestReanalizar_SegundaVezSinTexto_VuelveALeerLaFila es el resto del criterio: «el
// segundo re-análisis (sin `text`) la vuelve a leer como parte del origen».
//
// Se comprueba por el ORIGEN, que es donde eso se puede afirmar: la fila pegada quedó
// en el hilo, así que en la segunda pasada es material del evento y el origen vuelve a
// ser `event_thread` — sin `text` en el cuerpo no hay nada «pegado» en ESTA llamada.
func TestReanalizar_SegundaVezSinTexto_VuelveALeerLaFila(t *testing.T) {
	t.Parallel()
	b := bancoDe(t)

	_, err := b.pide(t, reanalisis.Solicitud{Text: "son 30 tequeños crudos"})
	require.NoError(t, err)
	// La fila pegada ya vive en el hilo del evento, como cualquier otra.
	b.hilo.entradas = append(b.hilo.entradas, events.ThreadEntry{
		Seq: 2, Role: events.RoleClient, Kind: events.KindMessage, Text: "son 30 tequeños crudos",
	})

	_, err = b.pide(t, reanalisis.Solicitud{})
	require.NoError(t, err)

	require.Equal(t, stages.OrigenHiloDelEvento, b.jobs.abiertos[1].Contexto.Source)
	require.Len(t, b.hilo.escritas, 1)
}

// TestReanalizar_SoloTextoPegado_OrigenPastedText: sin hilo pero CON transcripción hay
// material, y es solo el del dueño. Es el ÚNICO camino por el que `pasted_text` se
// escribe — sin él, ese valor del contrato §7.4 sería decorativo.
func TestReanalizar_SoloTextoPegado_OrigenPastedText(t *testing.T) {
	t.Parallel()
	b := bancoDe(t, func(b *banco) { b.hilo.entradas = nil })

	_, err := b.pide(t, reanalisis.Solicitud{Text: "son 30 tequeños crudos"})
	require.NoError(t, err)

	require.Equal(t, stages.OrigenTextoPegado, b.jobs.abiertos[0].Contexto.Source)
}

// TestReanalizar_TextoQueSaneaAVacio_EsComoNoMandarlo: un `text` de puros invisibles
// no es un error y no escribe nada. Equivale a «sin indicación» (REQ-33e).
func TestReanalizar_TextoQueSaneaAVacio_EsComoNoMandarlo(t *testing.T) {
	t.Parallel()
	b := bancoDe(t)

	// Dos zero-width spaces (U+200B) y espacios de maquetación, en ESCAPE y no como
	// carácter literal: un invisible pegado en el fuente sería invisible también para
	// quien revise este test, que es justo el problema que cart.SanitizeNote existe
	// para resolver (y lo que ST1018 avisa).
	_, err := b.pide(t, reanalisis.Solicitud{Text: "\u200b\u200b   \n\t"})
	require.NoError(t, err)

	require.Empty(t, b.hilo.escritas)
	require.Equal(t, stages.OrigenHiloDelEvento, b.jobs.abiertos[0].Contexto.Source)
}

// ---------------------------------------------------------------------------
// LO QUE NO SALE POR NINGÚN LADO
// ---------------------------------------------------------------------------

// TestReanalizar_ElLiteralNoAparaceEnElLog es el criterio de REQ-10c / ADR-0034 sobre
// esta puerta: el literal del cliente vive SOLO en memoria.
//
// Se comprueba sobre el camino que MÁS texto maneja —con transcripción pegada y con
// hilo—, y se busca tanto el texto del cliente como el del dueño.
func TestReanalizar_ElLiteralNoAparaceEnElLog(t *testing.T) {
	t.Parallel()
	const pegado = "el cliente dijo por instagram que quiere 30 tequeños"
	b := bancoDe(t)

	_, err := b.pide(t, reanalisis.Solicitud{Text: pegado})
	require.NoError(t, err)

	salida := b.log.String()
	require.NotContains(t, salida, textoDelCliente, "el literal del hilo se coló en el log")
	require.NotContains(t, salida, pegado, "la transcripción del dueño se coló en el log")
	require.Contains(t, salida, eventoDePrueba, "pero el log SÍ lleva identificadores, que es lo útil")
}

// TestServicio_NingunPuertoHablaConElCliente es INV-1 / INV-12 sostenido en los
// TIPOS: el re-análisis es una operación interna del dueño y el cliente no se entera.
//
// 🔴 SE MIRA LA FIRMA DEL CONSTRUCTOR, NO UNA EJECUCIÓN. Un test de conducta solo
// prueba los caminos que recorre; esto prueba que NO HAY POR DÓNDE — ningún puerto de
// este servicio puede mandar un mensaje, así que ninguna rama futura podrá tampoco.
// La mutación que lo caza: añadir un Gateway/Notifier al constructor.
func TestServicio_NingunPuertoHablaConElCliente(t *testing.T) {
	t.Parallel()
	identificadores := identificadoresDe(t, "reanalisis.go")

	for _, prohibido := range []string{"Sender", "Notifier", "SendText", "Gateway", "NotifyStatus"} {
		require.NotContains(t, identificadores, prohibido,
			"apareció %q en el caso de uso del re-análisis: INV-1/INV-12 dice que ningún camino le responde al cliente", prohibido)
	}
}

// TestServicio_NoPuedeLeerElSourceTextViejo sostiene el criterio «el prompt de P2 se
// arma con el literal del EVENTO y no con `intake_jobs.source_text`».
//
// 🔴 EL CRITERIO NO SE PUEDE PROBAR MUTANDO ESE CAMPO, Y NO POR PEREZA: en un job
// terminal ese campo es SIEMPRE NULL. INV-13 vacía las tres columnas del sobre EN LA
// MISMA SENTENCIA de los DOS terminales —`Finish` y `Fail`, machine.go— y está medido
// en campo: de los 34 jobs terminales de UAT (19 `done`, 15 `failed`) ninguno conserva
// `source_text_enc`. Un test que le pusiera un valor al sobre de un job `done` estaría
// FABRICANDO un estado que el sistema no puede producir, y un test sobre un estado
// imposible no prueba nada.
//
// Así que se afirma por donde sí se puede, y es más fuerte: este servicio NO TIENE
// NINGÚN PUERTO que devuelva el sobre de un job. Su puerto de jobs sabe preguntar si
// hay uno vivo y abrir uno nuevo, y nada más. La consecuencia real es la que importa
// —leer `conversation_event_messages` no es una preferencia de estilo del plan, es LO
// ÚNICO QUE FUNCIONA: cualquier atajo que quisiera reusar el sobre del job viejo se
// encontraría un NULL siempre— y el lado POSITIVO lo cubre
// TestReanalizar_AbreElJobConTodoSuContexto, que afirma que se compone la ventana del
// job nuevo leyendo el hilo.
func TestServicio_NoPuedeLeerElSourceTextViejo(t *testing.T) {
	t.Parallel()
	identificadores := identificadoresDe(t, "reanalisis.go")

	require.NotContains(t, identificadores, "SourceText",
		"el re-análisis no puede tocar el sobre de un job: el material se reconstruye desde el hilo del evento")
	require.NotContains(t, identificadores, "ClaimNext",
		"esta puerta no reclama jobs: solo abre el suyo")
}

// TestReanalizar_ElCompositorCaido_NoTumbaLaPeticion: el job YA existe cuando se
// compone, así que un fallo del sobre no puede devolver un error — un 500 le diría al
// dueño que no pasó nada mientras la cola tiene trabajo. Se avisa y se sigue.
func TestReanalizar_ElCompositorCaido_NoTumbaLaPeticion(t *testing.T) {
	t.Parallel()
	b := bancoDe(t, func(b *banco) { b.compositor.err = errInfra })

	out, err := b.pide(t, reanalisis.Solicitud{})

	require.NoError(t, err)
	require.Equal(t, jobDePrueba, out.JobID)
	require.Contains(t, b.log.String(), "SIN literal", "el hueco no puede ser mudo")
}

// TestReanalizar_ElTextoNoCabe_400: el saneo comparte tope con la indicación del
// cliente (280 runas) y RECHAZA en vez de truncar — recortar «…y sin maní» pierde el
// final, y el final es donde va el alérgeno (REQ-33e).
func TestReanalizar_ElTextoNoCabe_400(t *testing.T) {
	t.Parallel()
	b := bancoDe(t)

	_, err := b.pide(t, reanalisis.Solicitud{Text: strings.Repeat("a", 281)})

	require.Error(t, err)
	require.Contains(t, err.Error(), "281")
	b.exigeCeroEscrituras(t)
}

// TestNewServicio_SinUnaPieza_NoSeConstruye: seis dependencias obligatorias. Un
// servicio a medias abriría jobs que nadie puede completar, y el fallo no se vería
// hasta mirar la tabla.
func TestNewServicio_SinUnaPieza_NoSeConstruye(t *testing.T) {
	t.Parallel()
	b := bancoDe(t)
	log := logDeDescarte()

	casos := map[string]func() (*reanalisis.Servicio, error){
		"sin log": func() (*reanalisis.Servicio, error) {
			return reanalisis.NewServicio(nil, b.solicitudes, b.hilo, b.jobs, b.compositor, b.features, b.config)
		},
		"sin solicitudes": func() (*reanalisis.Servicio, error) {
			return reanalisis.NewServicio(log, nil, b.hilo, b.jobs, b.compositor, b.features, b.config)
		},
		"sin hilo": func() (*reanalisis.Servicio, error) {
			return reanalisis.NewServicio(log, b.solicitudes, nil, b.jobs, b.compositor, b.features, b.config)
		},
		"sin jobs": func() (*reanalisis.Servicio, error) {
			return reanalisis.NewServicio(log, b.solicitudes, b.hilo, nil, b.compositor, b.features, b.config)
		},
		"sin compositor": func() (*reanalisis.Servicio, error) {
			return reanalisis.NewServicio(log, b.solicitudes, b.hilo, b.jobs, nil, b.features, b.config)
		},
		"sin features": func() (*reanalisis.Servicio, error) {
			return reanalisis.NewServicio(log, b.solicitudes, b.hilo, b.jobs, b.compositor, nil, b.config)
		},
		"sin config": func() (*reanalisis.Servicio, error) {
			return reanalisis.NewServicio(log, b.solicitudes, b.hilo, b.jobs, b.compositor, b.features, nil)
		},
	}
	for nombre, construir := range casos {
		t.Run(nombre, func(t *testing.T) {
			t.Parallel()
			svc, err := construir()
			require.ErrorIs(t, err, reanalisis.ErrSinCablear)
			require.Nil(t, svc)
		})
	}
}
