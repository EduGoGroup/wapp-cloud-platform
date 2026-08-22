package fleet_test

import (
	"bytes"
	"context"
	"database/sql"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
)

// Simetría ESCRITOR↔LECTOR del self_pn cifrado (Plan 046 · T4.1, criterio (c)) y el
// ida y vuelta cifrar→descifrar (criterio (b)). Mismo gate WAPP_TEST_DB_DSN que el
// resto de la integración del paquete: reutiliza openTestDB/seedTenant/repoDePrueba
// de integration_test.go.
//
// ── POR QUÉ ESTE FICHERO EXISTE (y por qué la suite de runtime NO bastaba) ─────────
// El contrato entero de T4.1 es UNA propiedad: quien ESCRIBE el índice ciego
// (selfPnEnvelope, en este paquete) y quien lo INTERROGA (runtime.PostgresSelfNumbers)
// normalizan el número con la MISMA función y en el MISMO orden. Si esa simetría se
// rompe, nada falla ruidosamente: la guarda anti-self-loop deja de bloquear, sin un
// error, sin un log, sin una fila rara que mirar — el bidx no es reversible, así que
// tampoco se puede comparar «a ojo» contra el valor en claro, que ya no existe.
//
// Hasta este fichero, esa propiedad NO la custodiaba NINGÚN test. La suite
// self_numbers_integration_test.go siembra el bidx con su propio helper (bidxDe, que
// normaliza por su cuenta) y lo interroga con el suyo (esPropio, que también
// normaliza por su cuenta): un lazo cerrado entre dos helpers de test que no ejecuta
// ni una línea de selfPnEnvelope. Se comprobó: quitar normalizeSelfPn de
// selfPnEnvelope —calcular el bidx sobre el valor CRUDO— deja aquella suite ENTERA en
// verde, incluido el caso que se anuncia como «el que caza el fallo de no normalizar».
// Ese caso protege que el LLAMANTE normalice, que es otra cosa.
//
// ── POR QUÉ VIVE EN gateway/fleet Y NO EN flujos/runtime ──────────────────────────
// El test cruza dos paquetes (aquí se escribe, allí se lee) y podía alojarse en
// cualquiera de los dos. Se elige ESTE por tres razones, en este orden:
//
//  1. AQUÍ NO EXISTE bidxDe, Y ESO ES ESTRUCTURAL. El lazo cerrado no se abrió por
//     descuido: se abrió porque en runtime_test hay a mano un helper que sabe fabricar
//     un bidx sin pasar por el escritor, y el camino corto siempre se acaba tomando.
//     En fleet_test la ÚNICA forma de poner un bidx en la tabla es llamar a SetSelfPn
//     —el de verdad—. La disciplina la impone el paquete, no el comentario.
//  2. repoDePrueba YA DEVUELVE EL KeyProvider DEL ESCRITOR, y el checker se construye
//     con ESE mismo kp. Que escritor y lector compartan indexKey deja de ser una
//     convención que el test repite y pasa a ser un hecho del código del test: no hay
//     dos construcciones de material de clave que puedan divergir.
//  3. LA MUTACIÓN QUE ESTO CAZA VIVE EN ESTE PAQUETE (repository_postgres.go). El rojo
//     aparece donde se hizo el cambio, no en un paquete vecino que el autor no está
//     mirando — que es justo lo que hace que un test de acoplamiento sirva de algo.
//
// El precio, dicho sin rebajarlo: fleet_test pasa a importar internal/flujos/runtime,
// una flecha que no existe en producción (los dos paquetes se hablan solo a través de
// la TABLA). Es correcto que exista SOLO aquí: el acoplamiento entre ambos es
// precisamente lo que se está custodiando, así que el test que lo custodia es el único
// sitio del árbol donde los dos deben encontrarse.

// numeroPropioNormalizado es la forma CANÓNICA del teléfono que usan todos los casos
// de este fichero: solo dígitos, sin '+' ni separadores.
//
// 🔴 Se escribe como LITERAL y NO se deriva llamando a contact.Normalize. Si el test
// normalizara por su cuenta volvería a cerrar el lazo que este fichero existe para
// abrir: bastaría con que el normalizador cambiara para que test y producción se
// movieran juntos y el rojo no llegara nunca. La forma canónica se AFIRMA aquí, a
// mano, y el código de producción tiene que llegar a ella por su cuenta.
const numeroPropioNormalizado = "56984467443"

// grafiasDelMismoNumero son tres escrituras DISTINTAS del MISMO teléfono, tal y como
// pueden llegar de un Heartbeat, de un backfill o de un endpoint de admin. Las tres
// tienen que acabar en el MISMO índice ciego —el de numeroPropioNormalizado— porque
// el escritor normaliza ANTES de indexar. BlindIndex no normaliza nada: es un HMAC
// sobre los bytes que recibe.
var grafiasDelMismoNumero = []struct {
	nombre  string
	escrito string
}{
	{"canónica (solo dígitos)", "56984467443"},
	{"con '+'", "+56984467443"},
	{"con '+', espacios y guion", "+56 9 8446-7443"},
}

// TestIntegration_SelfPn_SimetriaEscritorLector es el criterio (c) de T4.1 afirmado
// de extremo a extremo y SIN helpers intermedios: escribe con SetSelfPn REAL (el de
// PostgresRepository, que pasa por selfPnEnvelope) y pregunta con IsSelfNumber REAL
// (el de runtime.PostgresSelfNumbers, que consulta self_pn_bidx en SQL). Entre los dos
// no hay ni una línea de test que calcule un HMAC.
//
// 💥 MUTACIÓN QUE LO PONE ROJO: quitar la llamada a normalizeSelfPn de
// selfPnEnvelope (repository_postgres.go:101) y calcular bidx/enc sobre el valor
// CRUDO. Las dos grafías adornadas dejan de casar y el test falla con dos de sus tres
// vueltas. También lo pone rojo la mutación simétrica: quitar la normalización del
// LADO LECTOR (que hoy hace isSelfLoop antes de llamar a IsSelfNumber) no lo toca —eso
// lo cubre la suite de runtime— pero cambiar la indexKey del checker, sí.
func TestIntegration_SelfPn_SimetriaEscritorLector(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo, kp := repoDePrueba(t, db)
	// El checker se construye con EL MISMO KeyProvider que el repositorio: es la
	// situación de producción (un solo keyring por proceso) y lo que hace que el
	// único punto de divergencia posible sea la NORMALIZACIÓN.
	checker := runtime.NewPostgresSelfNumbers(db, kp)

	for _, g := range grafiasDelMismoNumero {
		// 🔴 UN TENANT POR GRAFÍA, y no tres sesiones bajo el mismo. IsSelfNumber agrega
		// con bool_or sobre TODAS las filas que comparten bidx: con las tres juntas, las
		// dos grafías que sí casaran TAPARÍAN a la que no, y el test se quedaría verde
		// con la mutación puesta. Aislada por tenant, cada grafía responde por sí sola.
		tenantID := seedTenant(t, db)
		sembrarSesionActivaConNumero(ctx, t, repo, tenantID, "edge-simetria", "s-simetria", g.escrito)

		propio, err := checker.IsSelfNumber(ctx, tenantID, numeroPropioNormalizado)
		if err != nil {
			t.Fatalf("grafía %s: IsSelfNumber: %v", g.nombre, err)
		}
		if !propio {
			t.Fatalf("grafía %s: el número que escribió SetSelfPn REAL no lo reconoce IsSelfNumber REAL. "+
				"Escritor y lector dejaron de normalizar igual: la guarda anti-self-loop queda MUDA "+
				"—no bloquea y no da error—, que es el modo de fallo que T4.1 existe para cerrar", g.nombre)
		}
	}
}

// TestIntegration_SelfPn_DosGrafiasColapsanEnElMismoIndice es la otra mitad del
// criterio (c), por el lado del conteo: dos sesiones del MISMO tenant cuyo número se
// escribió con grafías DISTINTAS tienen que contar como DOS dispositivos del MISMO
// número (REQ-D4, aviso del tope). El lector aquí es CountLiveBySelfPn, que compara
// por bidx.
//
// 💥 MUTACIÓN QUE LO PONE ROJO: la misma de arriba (quitar normalizeSelfPn de
// selfPnEnvelope) hace que la grafía adornada escriba OTRO bidx y el conteo baje a 1
// — el aviso del tope de dispositivos dejaría de saltar justo cuando el dueño está a
// punto de quedarse sin poder emparejar. También lo pone rojo quitar la normalización
// de CountLiveBySelfPn (repository_postgres.go:240), que daría 0.
func TestIntegration_SelfPn_DosGrafiasColapsanEnElMismoIndice(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	repo, _ := repoDePrueba(t, db)
	const edgeID = "edge-tope"

	sembrarSesionActivaConNumero(ctx, t, repo, tenantID, edgeID, "s-tope-canonica",
		grafiasDelMismoNumero[0].escrito)
	sembrarSesionActivaConNumero(ctx, t, repo, tenantID, edgeID, "s-tope-adornada",
		grafiasDelMismoNumero[2].escrito)

	n, err := repo.CountLiveBySelfPn(ctx, tenantID, numeroPropioNormalizado)
	if err != nil {
		t.Fatalf("CountLiveBySelfPn: %v", err)
	}
	if n != 2 {
		t.Fatalf("dos sesiones del mismo número escrito con grafías distintas deben contar 2: got %d. "+
			"Con 1, las dos grafías produjeron índices ciegos distintos y el tope de dispositivos (REQ-D4) "+
			"cuenta dos teléfonos donde hay uno", n)
	}
}

// TestIntegration_SelfPn_IdaYVueltaYNoLegiblePorSQLDirecto cubre las dos mitades del
// criterio (b) que ningún test tocaba:
//
//  1. IDA Y VUELTA: se escribe con SetSelfPn la grafía ADORNADA y el listado
//     (Get/List, lo que sirve GET /api/v1/sessions) devuelve el número CANÓNICO en
//     claro. El contrato público NO cambió con T4.1, y eso hay que poder afirmarlo.
//  2. LA FILA NO ES LEGIBLE POR SQL DIRECTO: `SELECT self_pn` devuelve NULL y el
//     envelope no contiene los dígitos del teléfono en ningún sitio. Es el criterio
//     (a) afirmado sobre una fila concreta, y es el uso para el que repoDePrueba
//     devuelve el KeyProvider (hasta ahora los cinco call-sites lo descartaban con `_`).
//
// 💥 MUTACIÓN QUE LO PONE ROJO: (i) quitar `self_pn = NULL` del UPDATE de SetSelfPn
// (repository_postgres.go:300) ⇒ el SELECT directo devuelve el teléfono en claro;
// (ii) quitar normalizeSelfPn de selfPnEnvelope ⇒ el bidx guardado deja de casar con
// el esperado Y el Get devuelve la grafía adornada en vez de la canónica; (iii)
// escribir el número en `self_pn_enc` sin cifrar ⇒ bytes.Contains lo encuentra.
func TestIntegration_SelfPn_IdaYVueltaYNoLegiblePorSQLDirecto(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	repo, kp := repoDePrueba(t, db)
	const edgeID, sessionID = "edge-cifrado", "s-cifrado"

	// Se escribe la ADORNADA y se espera leer la CANÓNICA: el ida y vuelta incluye la
	// normalización, no solo el cifrado.
	sembrarSesionActivaConNumero(ctx, t, repo, tenantID, edgeID, sessionID,
		grafiasDelMismoNumero[2].escrito)

	s, found, err := repo.Get(ctx, tenantID, edgeID, sessionID)
	if err != nil || !found {
		t.Fatalf("Get tras SetSelfPn: found=%v err=%v", found, err)
	}
	if s.SelfPn != numeroPropioNormalizado {
		t.Fatalf("el listado debe devolver el número NORMALIZADO en claro: got %q, want %q "+
			"(si viene vacío, el sobre no abrió; si viene adornado, el escritor no normalizó)",
			s.SelfPn, numeroPropioNormalizado)
	}

	verificarFilaCifradaEnReposo(t, leerSobreCrudo(ctx, t, db, tenantID, edgeID, sessionID),
		kp.BlindIndex(tenantID, numeroPropioNormalizado))
}

// sembrarSesionActivaConNumero registra la sesión, la deja en perfil ACTIVE (una
// pasiva no bloquea, y el caso quedaría probando otra cosa) y le fija el número por el
// camino de producción. Los tres pasos van por el repositorio REAL: es el punto entero
// de este fichero.
func sembrarSesionActivaConNumero(
	ctx context.Context, t *testing.T, repo *fleet.PostgresRepository,
	tenantID, edgeID, sessionID, numeroEscrito string,
) {
	t.Helper()
	if err := repo.MarkOnline(ctx, tenantID, edgeID, sessionID); err != nil {
		t.Fatalf("MarkOnline(%s): %v", sessionID, err)
	}
	if _, err := repo.SetProfile(ctx, tenantID, sessionID, fleet.ProfileActive); err != nil {
		t.Fatalf("SetProfile(%s): %v", sessionID, err)
	}
	if err := repo.SetSelfPn(ctx, tenantID, edgeID, sessionID, numeroEscrito); err != nil {
		t.Fatalf("SetSelfPn(%s): %v", sessionID, err)
	}
}

// sobreSelfPn es la fila CRUDA leída por SQL directo, sin pasar por el repositorio:
// las cuatro columnas del sobre.
//
// 🔧 Tuvo una quinta lectura hasta T5.4: la columna en claro que T4.1 dejaba vacía.
// La 0070 la retiró, así que el `SELECT self_pn directo devolvió un valor` que este
// fichero afirmaba dejó de poder escribirse — y de hacer falta. Aquella aserción era
// la prueba de que el escritor vaciaba el claro; hoy la da el esquema, que no tiene
// dónde guardarlo.
type sobreSelfPn struct {
	bidx  sql.NullString
	kekID sql.NullString
	enc   []byte
	dek   []byte
}

// leerSobreCrudo lee la fila SIN el repositorio. Es la única forma de afirmar qué hay
// de verdad en la tabla: cualquier lectura que pase por scanSession ya descifra, y
// entonces el test no distingue «la columna está cifrada» de «la columna está en claro
// y el lector la devuelve tal cual».
func leerSobreCrudo(ctx context.Context, t *testing.T, db *sql.DB, tenantID, edgeID, sessionID string) sobreSelfPn {
	t.Helper()
	var s sobreSelfPn
	if err := db.QueryRowContext(ctx, `
		SELECT self_pn_bidx, self_pn_kek_id, self_pn_enc, self_pn_dek
		FROM public.fleet_sessions
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
	`, tenantID, edgeID, sessionID).Scan(&s.bidx, &s.kekID, &s.enc, &s.dek); err != nil {
		t.Fatalf("leer el sobre crudo de la fila: %v", err)
	}
	return s
}

// verificarFilaCifradaEnReposo afirma las cuatro cosas que hacen verdadero al criterio
// (a) sobre UNA fila: el sobre COMPLETO (las cuatro columnas o ninguna, invariante de
// la 0068 que no tiene CHECK que la vigile), el bidx igual al esperado, el kek_id
// puesto y el envelope sin rastro del teléfono. Eran CINCO hasta T5.4: la primera
// —«la columna en claro vacía»— se fue con la columna (0070).
//
// Va extraída y NOMBRADA por gocyclo (umbral 15, que aplica también a los tests): un
// subtest inline no bajaría la complejidad de la función madre, una función sí.
func verificarFilaCifradaEnReposo(t *testing.T, s sobreSelfPn, bidxEsperado string) {
	t.Helper()
	if !s.bidx.Valid || !s.kekID.Valid || len(s.enc) == 0 || len(s.dek) == 0 {
		t.Fatalf("el sobre tiene que ir ENTERO o no ir (invariante de la 0068, sin CHECK que la vigile): "+
			"bidx=%v kek_id=%v len(enc)=%d len(dek)=%d", s.bidx.Valid, s.kekID.Valid, len(s.enc), len(s.dek))
	}
	if s.bidx.String != bidxEsperado {
		t.Fatal("el índice ciego guardado NO es el del número NORMALIZADO: el escritor indexó otra cosa " +
			"(el valor crudo), así que ninguna consulta por bidx volverá a encontrar esta fila")
	}
	if s.kekID.String != kekIDDePrueba {
		t.Fatalf("self_pn_kek_id = %q, want %q (sin él, el Rekey no sabe qué KEK desenvolver)",
			s.kekID.String, kekIDDePrueba)
	}
	// El envelope es AES-256-GCM: los dígitos no pueden aparecer literales. Es una
	// comprobación tonta y por eso vale — caza el «se me olvidó cifrar» de un futuro
	// refactor mucho mejor que cualquier aserción sobre longitudes.
	if bytes.Contains(s.enc, []byte(numeroPropioNormalizado)) {
		t.Fatal("self_pn_enc contiene los dígitos del teléfono en claro: eso no es un envelope cifrado")
	}
}
