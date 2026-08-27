package quotetext_test

// fewshot_test.go — EL FEW-SHOT DE D-044.11: historial aprobado + semilla de
// `tenant_content` ref `quote_style_examples`.

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes/quotetext"
)

// TestSemilla_SolaAlimentaElFewShot: un tenant SIN historial pero CON semilla sí tiene
// voz que imitar, y por tanto sí se llama al modelo. Es el caso del arranque en frío
// y la única razón por la que la ref existe.
func TestSemilla_SolaAlimentaElFewShot(t *testing.T) {
	blob, err := json.Marshal(herminias())
	if err != nil {
		t.Fatalf("armando el blob de la semilla: %v", err)
	}
	sem := &semillaFake{blob: blob}
	e := nuevaEscena(t, artefactoP5(t, textoDelModelo), quotetext.ConSemilla(sem))

	out, err := e.svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba)
	if err != nil {
		t.Fatalf("Sugerir: %v", err)
	}
	if out.Origen != quotetext.OrigenLLM {
		t.Fatalf("origen = %q motivo = %q; con semilla hay voz que imitar", out.Origen, out.Motivo)
	}
	if len(sem.refs) != 1 || sem.refs[0] != quotetext.RefEstiloSemilla {
		t.Fatalf("se leyó tenant_content con refs %v; se esperaba solo %q",
			sem.refs, quotetext.RefEstiloSemilla)
	}
	in := e.prov.ultima(t)
	if len(in.Examples) != 2 {
		t.Fatalf("el few-shot llevó %d ejemplos; se esperaban 2 de la semilla", len(in.Examples))
	}
}

// TestSemilla_RefAusente_NoEsUnProblema: hoy NINGÚN tenant tiene esta ref escrita, así
// que su ausencia es el caso normal y no puede romper nada.
func TestSemilla_RefAusente_NoEsUnProblema(t *testing.T) {
	sem := &semillaFake{err: errors.New("tenant content not found")}
	e := nuevaEscena(t, artefactoP5(t, textoDelModelo), quotetext.ConSemilla(sem)).
		conHistorialDeHerminia(t)

	out, err := e.svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba)
	if err != nil {
		t.Fatalf("Sugerir: %v", err)
	}
	if out.Origen != quotetext.OrigenLLM {
		t.Fatalf("origen = %q motivo = %q; el historial solo basta", out.Origen, out.Motivo)
	}
	if n := len(e.prov.ultima(t).Examples); n != 2 {
		t.Fatalf("el few-shot llevó %d ejemplos; se esperaban los 2 del historial", n)
	}
}

// TestSemilla_BlobIlegible_SeIgnoraSinRomper: un blob mal formado no puede dejar sin
// cotización a nadie.
func TestSemilla_BlobIlegible_SeIgnoraSinRomper(t *testing.T) {
	sem := &semillaFake{blob: []byte(`{"otra_cosa": 3}`)}
	e := nuevaEscena(t, artefactoP5(t, textoDelModelo), quotetext.ConSemilla(sem)).
		conHistorialDeHerminia(t)

	out, err := e.svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba)
	if err != nil {
		t.Fatalf("Sugerir: %v", err)
	}
	if n := len(e.prov.ultima(t).Examples); n != 2 {
		t.Fatalf("el few-shot llevó %d ejemplos; se esperaban los 2 del historial", n)
	}
	if out.Origen != quotetext.OrigenLLM {
		t.Errorf("origen = %q motivo = %q", out.Origen, out.Motivo)
	}
}

func TestParseSemilla_LasDosFormas(t *testing.T) {
	casos := map[string]string{
		"array pelado": `["uno","dos"]`,
		"envuelto":     `{"examples":["uno","dos"]}`,
	}
	for nombre, blob := range casos {
		t.Run(nombre, func(t *testing.T) {
			ex, err := quotetext.ParseSemilla([]byte(blob))
			if err != nil {
				t.Fatalf("ParseSemilla: %v", err)
			}
			if len(ex) != 2 || ex[0] != "uno" || ex[1] != "dos" {
				t.Fatalf("ejemplos = %v", ex)
			}
		})
	}
	t.Run("cualquier otra forma es error", func(t *testing.T) {
		for _, blob := range []string{`{"nope":1}`, `42`, `"texto suelto"`, `no es json`} {
			if _, err := quotetext.ParseSemilla([]byte(blob)); err == nil {
				t.Errorf("ParseSemilla(%q) no devolvió error", blob)
			}
		}
	})
}

// TestFewShot_RepartoDelCupo: con historial LLENO y semilla, la semilla no desaparece
// —tiene reservada la mitad del cupo— y el historial no se queda sin sitio.
//
// Es una decisión de T5.1 (D-044.11 no la fija), y por eso está probada: sin este test
// el reparto sería una frase en un comentario.
func TestFewShot_RepartoDelCupo(t *testing.T) {
	store := intakes.NewMemoryStore()
	store.Add(tenantDePrueba, intakes.Intake{
		ID: intakeDePrueba, Status: intakes.StatusPendingApproval,
		SessionID: sesionDePrueba, Total: totalFusion,
	}, lineasFusion...)
	// Seis aprobadas: más que el cupo por defecto (5).
	for i, texto := range []string{"h1", "h2", "h3", "h4", "h5", "h6"} {
		aprobadaEnOtraSolicitud(t, store, tenantDePrueba, "viejo-"+texto, texto,
			hoy.Add(-time.Duration(i)*time.Hour))
	}
	blob, err := json.Marshal([]string{"semilla A", "semilla B", "semilla C"})
	if err != nil {
		t.Fatalf("blob: %v", err)
	}

	prov := &provFake{respuesta: artefactoP5(t, textoDelModelo)}
	svc, err := quotetext.NewServicio(logger.New(logger.WithWriter(&strings.Builder{})),
		store, store, &selFake{prov: prov},
		quotetext.ConSemilla(&semillaFake{blob: blob}))
	if err != nil {
		t.Fatalf("NewServicio: %v", err)
	}
	if _, err := svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba); err != nil {
		t.Fatalf("Sugerir: %v", err)
	}

	ex := prov.ultima(t).Examples
	if len(ex) != quotetext.EjemplosPorDefecto {
		t.Fatalf("el few-shot llevó %d ejemplos; el cupo es %d", len(ex), quotetext.EjemplosPorDefecto)
	}
	var deSemilla, deHistorial int
	for _, e := range ex {
		if strings.HasPrefix(e, "semilla ") {
			deSemilla++
		} else {
			deHistorial++
		}
	}
	if deSemilla == 0 {
		t.Error("con el historial lleno la semilla desapareció; tiene reservada la mitad del cupo")
	}
	if deHistorial == 0 {
		t.Error("la semilla se comió el cupo entero; el historial es la voz REAL del tenant")
	}
	// El primero es del historial: lo que escribió de verdad la última vez.
	if strings.HasPrefix(ex[0], "semilla ") {
		t.Errorf("el primer ejemplo tendría que ser del historial y fue %q", ex[0])
	}
}

// TestFewShot_EjemploDesbocado_SeDescarta: `tenant_content` admite 1 MiB y el blob lo
// escribe el dueño por API. Un ejemplo gigante no puede acabar en el prompt.
func TestFewShot_EjemploDesbocado_SeDescarta(t *testing.T) {
	gigante := strings.Repeat("a", quotetext.MaxRunasEjemplo+1)
	blob, err := json.Marshal([]string{gigante, "  ", herminia1, herminia1})
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	e := nuevaEscena(t, artefactoP5(t, textoDelModelo),
		quotetext.ConSemilla(&semillaFake{blob: blob}))

	if _, err := e.svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba); err != nil {
		t.Fatalf("Sugerir: %v", err)
	}
	ex := e.prov.ultima(t).Examples
	// Queda UNO: el gigante fuera, el vacío fuera y el repetido deduplicado.
	if len(ex) != 1 || ex[0] != herminia1 {
		t.Fatalf("ejemplos = %#v; se esperaba solo la cotización buena", ex)
	}
}

// TestPuerto_ExamplesVacio_EsValido comprueba contra el MÓDULO PUBLICADO que el puerto
// admite un few-shot vacío, que es lo que su contrato promete («Puede venir vacío»).
//
// 🔴 SE PRUEBA AQUÍ Y NO A TRAVÉS DEL SERVICIO A PROPÓSITO: este paquete NUNCA llama
// con `Examples` vacío —corta antes y devuelve el determinista—, así que un test que
// lo intentara por el servicio no llegaría al puerto y no probaría nada. Lo que se
// afirma es lo del puerto: sin ejemplos el prompt se arma igual, y con ellos el
// prefijo que el proveedor cachea (ADR-0046) SIGUE SIENDO el mismo.
func TestPuerto_ExamplesVacio_EsValido(t *testing.T) {
	quote := json.RawMessage(`{"version":1,"lines":[],"total":0}`)

	sin := llm.BuildGenerateQuoteTextPrompt(llm.GenerateQuoteTextInput{Quote: quote})
	if strings.TrimSpace(sin) == "" {
		t.Fatal("un few-shot vacío dejó el prompt vacío")
	}
	con := llm.BuildGenerateQuoteTextPrompt(llm.GenerateQuoteTextInput{
		Quote: quote, Examples: herminias(),
	})
	if con == sin {
		t.Fatal("el few-shot no cambió el prompt: no se está enviando")
	}
	for _, ex := range herminias() {
		if !strings.Contains(con, ex) {
			t.Errorf("el prompt no lleva el ejemplo:\n%q", ex)
		}
	}
}

// --- LAS DOS COTAS DEL FEW-SHOT (T5.1) --------------------------------------

// textoDe fabrica un ejemplo de `runas` runas, distinguible por su marca. Distinto en
// cada llamada para que el dedupe de `sanear` no lo confunda con otro.
func textoDe(marca string, runas int) string {
	if runas <= len(marca) {
		return marca[:runas]
	}
	return marca + strings.Repeat("x", runas-len(marca))
}

// escenaConHistorial siembra en el tenant las cotizaciones aprobadas dadas, de la
// PRIMERA (más reciente) a la última (más antigua), y devuelve el servicio armado.
func escenaConHistorial(t *testing.T, textos ...string) *escena {
	t.Helper()
	e := nuevaEscena(t, artefactoP5(t, textoDelModelo))
	for i, texto := range textos {
		aprobadaEnOtraSolicitud(t, e.store, tenantDePrueba, "viejo-"+strconv.Itoa(i), texto,
			hoy.Add(-time.Duration(i)*time.Hour))
	}
	return e
}

// TestFewShot_PresupuestoAgregado_RecortaPorLaCola es la cota de MaxRunasFewShot.
//
// Cuatro ejemplos de 1000 runas: los tres primeros suman justo el presupuesto y el
// cuarto se queda fuera. El assert no es solo el número: son LOS TRES PRIMEROS, que es
// lo que hace predecible el recorte.
//
// 🔴 EL CONTROL DEL FINAL ES LO QUE IMPIDE QUE ESTE TEST SEA VACUO: con ejemplos de
// tamaño normal los CINCO entran, así que la cota no está recortando por casualidad ni
// hay un tope escondido en otro sitio.
func TestFewShot_PresupuestoAgregado_RecortaPorLaCola(t *testing.T) {
	largos := []string{
		textoDe("uno-", 1000), textoDe("dos-", 1000),
		textoDe("tres-", 1000), textoDe("cuatro-", 1000),
	}
	e := escenaConHistorial(t, largos...)

	if _, err := e.svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba); err != nil {
		t.Fatalf("Sugerir: %v", err)
	}

	ex := e.prov.ultima(t).Examples
	if len(ex) != 3 {
		t.Fatalf("el few-shot llevó %d ejemplos; con 4 de 1000 runas y un presupuesto de %d solo caben 3",
			len(ex), quotetext.MaxRunasFewShot)
	}
	for i := range ex {
		if ex[i] != largos[i] {
			t.Errorf("[%d] no es el ejemplo esperado: el recorte tiene que dejar LOS K PRIMEROS", i)
		}
	}
	if n := runasDe(ex); n > quotetext.MaxRunasFewShot {
		t.Errorf("el few-shot usó %d runas; el presupuesto es %d", n, quotetext.MaxRunasFewShot)
	}

	// CONTROL: con ejemplos normales entran los cinco.
	normales := []string{
		textoDe("a-", 150), textoDe("b-", 150), textoDe("c-", 150),
		textoDe("d-", 150), textoDe("e-", 150),
	}
	e2 := escenaConHistorial(t, normales...)
	if _, err := e2.svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba); err != nil {
		t.Fatalf("Sugerir (control): %v", err)
	}
	if n := len(e2.prov.ultima(t).Examples); n != quotetext.EjemplosPorDefecto {
		t.Fatalf("con ejemplos de tamaño normal entraron %d de %d: la cota está recortando de más",
			n, quotetext.EjemplosPorDefecto)
	}
}

// TestFewShot_ElRecorteParaYNoSalta: en cuanto uno no cabe se descartan él Y LOS
// SIGUIENTES, aunque alguno posterior cupiera de sobra.
//
// Sin esta regla, la lista final dependería de las longitudes de los ejemplos
// posteriores y dos tenants con el mismo historial se llevarían few-shots distintos por
// razones que solo se ven releyendo los cinco textos.
func TestFewShot_ElRecorteParaYNoSalta(t *testing.T) {
	textos := []string{
		textoDe("uno-", 1200), textoDe("dos-", 1200),
		textoDe("tres-", 1200), // no cabe: 1200+1200+1200 = 3600 > 3000
		textoDe("corto-", 50),  // cabría (2400+50), pero YA se paró
	}
	e := escenaConHistorial(t, textos...)

	if _, err := e.svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba); err != nil {
		t.Fatalf("Sugerir: %v", err)
	}

	ex := e.prov.ultima(t).Examples
	if len(ex) != 2 {
		t.Fatalf("el few-shot llevó %d ejemplos; se esperaban 2 (se para en el tercero)", len(ex))
	}
	for _, e := range ex {
		if strings.HasPrefix(e, "corto-") {
			t.Fatal("el ejemplo corto de después del que no cabía entró igual: el recorte SALTA en vez de parar")
		}
	}
}

// TestFewShot_EjemploLargoDelHistorial_SeDescartaEntero es la cota por ejemplo
// (MaxRunasEjemplo) sobre el HISTORIAL, que es el material que no tiene tope en ningún
// otro sitio: `intake_revisions.rendered_text` es un TEXT pelado y el POST de
// aprobación no acota su cuerpo.
//
// Se descarta ENTERO: lo que NO puede pasar es que llegue truncado, porque un ejemplo
// cortado a media frase le enseña al modelo a cortar frases.
func TestFewShot_EjemploLargoDelHistorial_SeDescartaEntero(t *testing.T) {
	desbocado := textoDe("desbocado-", quotetext.MaxRunasEjemplo+1)
	e := escenaConHistorial(t, desbocado, herminia1)

	if _, err := e.svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba); err != nil {
		t.Fatalf("Sugerir: %v", err)
	}

	ex := e.prov.ultima(t).Examples
	if len(ex) != 1 || ex[0] != herminia1 {
		t.Fatalf("ejemplos = %d (%v truncado a 40): se esperaba solo la cotización buena",
			len(ex), primerasRunas(ex, 40))
	}
	for _, e := range ex {
		if strings.HasPrefix(e, "desbocado-") {
			t.Fatal("el ejemplo desbocado llegó al prompt (entero o truncado); tiene que descartarse")
		}
	}
}

// runasDe suma las runas de todos los ejemplos.
func runasDe(ex []string) int {
	total := 0
	for _, e := range ex {
		total += utf8.RuneCountInString(e)
	}
	return total
}

// primerasRunas recorta los ejemplos para que un fallo no vuelque miles de runas al log
// del test.
func primerasRunas(ex []string, n int) []string {
	out := make([]string, 0, len(ex))
	for _, e := range ex {
		r := []rune(e)
		if len(r) > n {
			r = r[:n]
		}
		out = append(out, string(r))
	}
	return out
}
