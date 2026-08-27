package casebank_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/casebank"
)

// casebank_test.go — EL GUARD DE GO, probado SIN BASE.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 POR QUÉ ESTE FICHERO NO ABRE NI UNA CONEXIÓN
// ════════════════════════════════════════════════════════════════════════════
//
// Porque el consentimiento lo defienden DOS cosas —este guard y el CHECK
// `intake_case_bank_consented_check` de la 0082— y una defensa duplicada solo
// vale si cada mitad tiene un test que la otra NO puede salvar. Si este test
// corriera contra Postgres, borrar el guard de Go lo dejaría VERDE (el CHECK
// rechazaría igual) y la mitad de arriba quedaría sin red.
//
// Aquí el store es un doble que CUENTA LLAMADAS, así que la aserción no es «dio
// error» sino «no llegó a la base», que es lo que el guard promete.
//
// 💥 LAS DOS MUTACIONES, una por mitad:
//
//   - borrar `if !c.Consented { return ErrSinConsentimiento }` de `validar` ⇒ el
//     doble recibe la llamada y ESTE fichero se pone rojo. El test del CHECK
//     (postgres_integration_test.go) seguiría VERDE;
//   - borrar el `ADD CONSTRAINT` de la 0082 ⇒ el test del CHECK se pone rojo y
//     ESTE fichero sigue VERDE.

// storeEspia cuenta y guarda. `Insertar` NO valida nada a propósito: si el doble
// repitiera la regla del servicio, el test comprobaría al doble.
type storeEspia struct {
	insertados []casebank.Caso
	consultas  [][2]string
	// ya es lo que devuelve `Existe`.
	ya bool
	// errInsertar hace fallar la escritura.
	errInsertar error
	// siguienteID es el id que devuelve la próxima inserción.
	siguienteID int64
}

func (s *storeEspia) Insertar(_ context.Context, c casebank.Caso) (int64, error) {
	s.insertados = append(s.insertados, c)
	if s.errInsertar != nil {
		return 0, s.errInsertar
	}
	s.siguienteID++
	return s.siguienteID, nil
}

func (s *storeEspia) Existe(_ context.Context, tenantID, sourceText string) (bool, error) {
	s.consultas = append(s.consultas, [2]string{tenantID, sourceText})
	return s.ya, nil
}

func servicio(t *testing.T) (*casebank.Servicio, *storeEspia) {
	t.Helper()
	espia := &storeEspia{}
	s, err := casebank.NewServicio(espia, anon())
	if err != nil {
		t.Fatalf("NewServicio: %v", err)
	}
	return s, espia
}

// casoValido es el molde del que parten los tests, para que cada uno estropee UNA
// cosa y se vea cuál.
func casoValido() casebank.Caso {
	return casebank.Caso{
		TenantID:   "t-casebank",
		Consented:  true,
		SourceText: "quiero una torta de 10 porciones",
	}
}

// ---------------------------------------------------------------------------
// EL CRITERIO LITERAL DE T5.3
// ---------------------------------------------------------------------------

// TestInsertar_SinConsentimiento_NoLlegaALaBase es el criterio «insert sin
// consentimiento ⇒ error», y afirma la mitad que de verdad importa: no es que la
// base lo rechace, es que NO SE INTENTA.
func TestInsertar_SinConsentimiento_NoLlegaALaBase(t *testing.T) {
	s, espia := servicio(t)
	c := casoValido()
	c.Consented = false

	id, err := s.Insertar(context.Background(), c)
	if !errors.Is(err, casebank.ErrSinConsentimiento) {
		t.Fatalf("Insertar sin consentimiento devolvió %v; se esperaba ErrSinConsentimiento", err)
	}
	if id != 0 {
		t.Errorf("Insertar devolvió id %d con error; se esperaba 0", id)
	}
	if len(espia.insertados) != 0 {
		t.Errorf("el store recibió %d escrituras; el guard tiene que rechazar ANTES de tocar la base", len(espia.insertados))
	}
}

// TestInsertar_ConConsentimiento_Persiste es la mitad complementaria, sin la cual
// el test de arriba lo pasaría también un `Insertar` que no insertara nunca.
func TestInsertar_ConConsentimiento_Persiste(t *testing.T) {
	s, espia := servicio(t)
	id, err := s.Insertar(context.Background(), casoValido())
	if err != nil {
		t.Fatalf("Insertar: %v", err)
	}
	if id != 1 {
		t.Errorf("id = %d; se esperaba el que devolvió el store (1)", id)
	}
	if len(espia.insertados) != 1 {
		t.Fatalf("el store recibió %d escrituras; se esperaba 1", len(espia.insertados))
	}
	if !espia.insertados[0].Consented {
		t.Error("el caso llegó al store con consented=false: el servicio no puede aflojar lo que validó")
	}
}

// TestInsertar_LosOtrosDosGuards cubre tenant y texto, que también rechazan sin
// tocar la base.
func TestInsertar_LosOtrosDosGuards(t *testing.T) {
	casos := []struct {
		nombre string
		romper func(*casebank.Caso)
		quiero error
	}{
		{"sin tenant", func(c *casebank.Caso) { c.TenantID = "  " }, casebank.ErrSinTenant},
		{"sin texto", func(c *casebank.Caso) { c.SourceText = "\n\t " }, casebank.ErrSinTexto},
	}
	for _, tc := range casos {
		t.Run(tc.nombre, func(t *testing.T) {
			s, espia := servicio(t)
			c := casoValido()
			tc.romper(&c)
			if _, err := s.Insertar(context.Background(), c); !errors.Is(err, tc.quiero) {
				t.Fatalf("devolvió %v; se esperaba %v", err, tc.quiero)
			}
			if len(espia.insertados) != 0 {
				t.Errorf("el store recibió %d escrituras; se esperaba 0", len(espia.insertados))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LA ANONIMIZACIÓN NO ES OPCIONAL
// ---------------------------------------------------------------------------

// TestInsertar_AnonimizaAntesDePersistir es el candado de la otra mitad de T5.3:
// el literal que sale de este proceso NO puede llevar PII. Se afirma sobre lo que
// RECIBIÓ EL STORE, no sobre lo que devolvió `Anonimizar` — que es lo que
// distingue «la función existe» de «la función está cableada».
//
// 💥 Mutación: quitar la línea `c.SourceText = s.anon.Anonimizar(c.SourceText)`
// de `Insertar` ⇒ el store recibe el teléfono y el nombre y esto se pone rojo.
func TestInsertar_AnonimizaAntesDePersistir(t *testing.T) {
	s, espia := servicio(t)
	c := casoValido()
	c.SourceText = "Ambar escribió al +58 412 123 4567 desde 584121234567@s.whatsapp.net pidiendo 10 porciones"

	if _, err := s.Insertar(context.Background(), c); err != nil {
		t.Fatalf("Insertar: %v", err)
	}
	if len(espia.insertados) != 1 {
		t.Fatalf("el store recibió %d escrituras; se esperaba 1", len(espia.insertados))
	}
	got := espia.insertados[0].SourceText

	for _, prohibido := range []string{"Ambar", "412 123 4567", "s.whatsapp.net"} {
		if strings.Contains(got, prohibido) {
			t.Errorf("el store recibió %q, que sigue conteniendo %q", got, prohibido)
		}
	}
	for _, marca := range []string{casebank.MarcaNombre, casebank.MarcaTelefono, casebank.MarcaJID} {
		if !strings.Contains(got, marca) {
			t.Errorf("el store recibió %q, sin la marca %q", got, marca)
		}
	}
	// Y la mitad que impide que el «arreglo» sea vaciar el texto: lo que NO es
	// PII tiene que sobrevivir, o el caso deja de servir como material de eval.
	if !strings.Contains(got, "pidiendo 10 porciones") {
		t.Errorf("el store recibió %q; el contenido del pedido tenía que sobrevivir", got)
	}
}

// ---------------------------------------------------------------------------
// LA SIEMBRA
// ---------------------------------------------------------------------------

// TestSembrar_EsIdempotente: la segunda corrida no escribe.
func TestSembrar_EsIdempotente(t *testing.T) {
	s, espia := servicio(t)
	ctx := context.Background()

	id, sembro, err := s.Sembrar(ctx, casoValido())
	if err != nil {
		t.Fatalf("Sembrar (1.ª): %v", err)
	}
	if !sembro || id == 0 {
		t.Fatalf("la primera siembra devolvió (%d, %t); se esperaba haber escrito", id, sembro)
	}

	espia.ya = true // ahora la base dice que ya está
	id2, sembro2, err := s.Sembrar(ctx, casoValido())
	if err != nil {
		t.Fatalf("Sembrar (2.ª): %v", err)
	}
	if sembro2 || id2 != 0 {
		t.Errorf("la segunda siembra devolvió (%d, %t); se esperaba (0, false)", id2, sembro2)
	}
	if len(espia.insertados) != 1 {
		t.Errorf("se escribieron %d filas; se esperaba 1", len(espia.insertados))
	}
}

// TestSembrar_PreguntaPorElTextoYaAnonimizado es el defecto que este método tiene
// que evitar: en la base está el texto ANONIMIZADO, así que preguntar por el
// crudo daría «no existe» siempre y sembraría un duplicado en cada corrida.
//
// 💥 Mutación: mover `c.SourceText = s.anon.Anonimizar(...)` DESPUÉS del
// `Existe` ⇒ la consulta lleva el crudo y esto se pone rojo.
func TestSembrar_PreguntaPorElTextoYaAnonimizado(t *testing.T) {
	s, espia := servicio(t)
	c := casoValido()
	c.SourceText = "Ambar quiere 10 porciones"

	if _, _, err := s.Sembrar(context.Background(), c); err != nil {
		t.Fatalf("Sembrar: %v", err)
	}
	if len(espia.consultas) != 1 {
		t.Fatalf("se hicieron %d consultas; se esperaba 1", len(espia.consultas))
	}
	consultado := espia.consultas[0][1]
	if strings.Contains(consultado, "Ambar") {
		t.Errorf("se consultó por %q, que lleva el nombre en claro: la base no guarda eso", consultado)
	}
	if !strings.Contains(consultado, casebank.MarcaNombre) {
		t.Errorf("se consultó por %q; se esperaba el texto anonimizado", consultado)
	}
}

// TestSembrar_SinConsentimiento_NoConsultaNiEscribe: el guard manda también en
// esta puerta, y por eso `validar` está extraída y no duplicada.
func TestSembrar_SinConsentimiento_NoConsultaNiEscribe(t *testing.T) {
	s, espia := servicio(t)
	c := casoValido()
	c.Consented = false

	if _, _, err := s.Sembrar(context.Background(), c); !errors.Is(err, casebank.ErrSinConsentimiento) {
		t.Fatalf("devolvió %v; se esperaba ErrSinConsentimiento", err)
	}
	if len(espia.consultas)+len(espia.insertados) != 0 {
		t.Errorf("hubo %d consultas y %d escrituras; se esperaba cero de las dos",
			len(espia.consultas), len(espia.insertados))
	}
}

func TestNewServicio_SinStore(t *testing.T) {
	if _, err := casebank.NewServicio(nil, anon()); err == nil {
		t.Fatal("NewServicio(nil) no falló: un servicio a medias panica en la primera llamada")
	}
}

// ---------------------------------------------------------------------------
// LA SEMILLA DEL CASO AMBAR
// ---------------------------------------------------------------------------

// TestSemilla_PasaElBarridoDeAnonimizacion es el criterio literal de T5.3 («el
// caso sembrado pasa el barrido de anonimización»).
//
// 🔴 NO ES UNA TAUTOLOGÍA, y conviene decir por qué: `TextoCasoAmbar` está
// ESCRITO A MANO y NO pasó por `Anonimizar`, así que el barrido responde una
// pregunta abierta. (Si se barriera el resultado de `Anonimizar`, el test valdría
// cero: los dos comparten detectores.) Su control negativo es
// `TestRestos_DelataLasTresClases`: sin él, un `Restos` que devolviera `nil`
// dejaría este test verde.
func TestSemilla_PasaElBarridoDeAnonimizacion(t *testing.T) {
	a := casebank.NuevoAnonimizador(casebank.NombresDelCaso()...)
	if restos := a.Restos(casebank.TextoCasoAmbar); len(restos) != 0 {
		t.Errorf("el caso sembrado tiene %d restos identificables: %v", len(restos), restos)
	}
}

// TestSemilla_ElAnonimizadorLaDejaINTACTA es la otra mitad, y es la que de verdad
// vigila al anonimizador: si se volviera goloso con los números, «10 o 12
// porciones», «25 o 30» y «tequeños congelados de 30» —las cantidades sobre las
// que este caso evalúa a P4— saldrían tapadas y el dataset dejaría de medir nada.
func TestSemilla_ElAnonimizadorLaDejaINTACTA(t *testing.T) {
	a := casebank.NuevoAnonimizador(casebank.NombresDelCaso()...)
	if got := a.Anonimizar(casebank.TextoCasoAmbar); got != casebank.TextoCasoAmbar {
		t.Errorf("el anonimizador CAMBIÓ la semilla.\n got: %q\nwant: %q", got, casebank.TextoCasoAmbar)
	}
}

// TestSemilla_LaProcedenciaViajaDENTRODeLaFila: el aviso de calidad C no puede
// quedarse en un comentario de Go. Quien lea la fila dentro de seis meses ve el
// `source_text` y el `expected`, y en ninguno de los dos puede parecer que esto
// es la transcripción real de un cliente.
func TestSemilla_LaProcedenciaViajaDENTRODeLaFila(t *testing.T) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(casebank.EsperadoCasoAmbar(), &doc); err != nil {
		t.Fatalf("el `expected` de la semilla no es JSON válido: %v", err)
	}
	crudo, ok := doc["_procedencia"]
	if !ok {
		t.Fatal("el `expected` de la semilla no lleva `_procedencia`: la fila sembrada no declara que es material REDACTADO")
	}
	var proc struct {
		Calidad string `json:"calidad"`
		EsReal  *bool  `json:"es_texto_real_del_cliente"`
		Aviso   string `json:"aviso"`
	}
	if err := json.Unmarshal(crudo, &proc); err != nil {
		t.Fatalf("`_procedencia` no decodifica: %v", err)
	}
	if proc.Calidad != "C" {
		t.Errorf("calidad = %q; se esperaba \"C\"", proc.Calidad)
	}
	if proc.EsReal == nil || *proc.EsReal {
		t.Error("`es_texto_real_del_cliente` tiene que estar y ser false")
	}
	if proc.Aviso == "" {
		t.Error("`_procedencia` sin aviso: la clave sola no explica la consecuencia")
	}
}

// TestCasoAmbar_LlevaConsentimientoYPasaElGuard cierra el círculo: la semilla
// entra por la MISMA puerta que cualquier otro caso, no por un atajo.
//
// 🔴 ESTE TEST **NO** ES LA RED DE LA ANONIMIZACIÓN, Y NO PUEDE SERLO. Sobre el
// texto de Ambar, anonimizar es LA IDENTIDAD —el fixture está escrito sin
// nombres, sin teléfonos y sin JID a propósito—, así que quitar la llamada a
// `Anonimizar` de `Servicio.Insertar` deja este test VERDE: la última aserción
// («el texto sembrado es el de la constante») se cumple igual con el paso puesto
// que sin él. Comprobado ejecutando la mutación.
//
// Quien caza esa mutación es `TestInsertar_AnonimizaAntesDePersistir`, que usa
// otro fixture —uno CON nombre, teléfono y JID— precisamente porque hace falta un
// texto que la anonimización cambie. Si algún día alguien recorta la suite, este
// test no cubre a aquél.
func TestCasoAmbar_LlevaConsentimientoYPasaElGuard(t *testing.T) {
	s, espia := servicio(t)
	if _, sembro, err := s.Sembrar(context.Background(), casebank.CasoAmbar("t-casebank")); err != nil || !sembro {
		t.Fatalf("Sembrar el caso Ambar devolvió (%t, %v); se esperaba haber sembrado", sembro, err)
	}
	if len(espia.insertados) != 1 {
		t.Fatalf("se escribieron %d filas; se esperaba 1", len(espia.insertados))
	}
	if espia.insertados[0].SourceText != casebank.TextoCasoAmbar {
		t.Error("el texto sembrado NO es el de la constante: el anonimizador lo modificó por el camino")
	}
}
