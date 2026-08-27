package quotetext_test

// quotetext_test.go — LAS TRES CONDUCTAS QUE SON EL CRITERIO DE T5.1:
//
//	· con historial          ⇒ se llama al modelo con el few-shot del tenant;
//	· precio alterado        ⇒ se rechaza y sale el render determinista;
//	· tenant sin historial   ⇒ determinista directo, SIN llamar al modelo.
//
// Los asserts de salida son POR ESTRUCTURA (el texto contiene los importes, empieza
// por el saludo, trae el total) y no por igualdad literal, salvo donde la igualdad ES
// lo que se afirma: que el texto devuelto es EXACTAMENTE el del modelo, o EXACTAMENTE
// el del render determinista. Confundir esos dos casos es como un fallback se cuela
// disfrazado de éxito.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes/quotetext"
)

const (
	tenantDePrueba = "tenant-fusion"
	intakeDePrueba = "intake-ambar"
	sesionDePrueba = "sess-fusion"
)

// Las dos marcas de tiempo del historial. Fijas para que el orden del few-shot no
// dependa del reloj de la máquina que corre el test.
var (
	hoy  = time.Date(2026, 8, 27, 17, 24, 0, 0, time.UTC)
	ayer = hoy.Add(-24 * time.Hour)
)

// líneas del caso Fusión, ya con los precios que Herminia puso (design §1).
var lineasFusion = []intakes.Item{
	{SKU: "TORTA-CHOC", Label: "Torta chocolate húmedo + crema choc. — 15 porciones", Qty: 1, UnitPrice: 2100},
	{SKU: "TORTA-VAI", Label: "Torta vainilla, ddl, merengue — 25-30 porciones", Qty: 1, UnitPrice: 2950},
	{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1, UnitPrice: 490},
}

// totalFusion es la suma de las tres. Se escribe a mano y no se calcula para que el
// test no repita la aritmética que está probando.
const totalFusion = 5540.0

// textoDelModelo es una respuesta BUENA: dice los tres precios y el total, con marca
// de dinero, y no trae ningún número inventado.
const textoDelModelo = "Hola! Te paso el presupuesto:\n" +
	"Pastel para 15 personas, chocolate húmedo, relleno chocolate y oreo — $2100. Incluye impresiones no comestibles\n" +
	"El otro para 25-30 personas, vainilla, ddl y merengue — $2950\n" +
	"Envío — $490\n" +
	"Total $5540"

// escena es el montaje completo del generador con sus dobles.
type escena struct {
	svc   *quotetext.Servicio
	store *intakes.MemoryStore
	prov  *provFake
	sel   *selFake
}

// nuevaEscena siembra la solicitud del caso Fusión y arma el servicio. `respuesta` es
// lo que devolverá el modelo si se le llega a llamar.
func nuevaEscena(t *testing.T, respuesta json.RawMessage, opts ...quotetext.Opción) *escena {
	t.Helper()
	store := intakes.NewMemoryStore()
	store.Add(tenantDePrueba, intakes.Intake{
		ID: intakeDePrueba, Status: intakes.StatusPendingApproval,
		SessionID: sesionDePrueba, Total: totalFusion,
	}, lineasFusion...)

	prov := &provFake{respuesta: respuesta}
	sel := &selFake{prov: prov}
	svc, err := quotetext.NewServicio(logger.New(logger.WithWriter(&strings.Builder{})),
		store, store, sel, opts...)
	if err != nil {
		t.Fatalf("NewServicio: %v", err)
	}
	return &escena{svc: svc, store: store, prov: prov, sel: sel}
}

// conHistorialDeHerminia siembra las dos cotizaciones del fixture como revisiones
// `approved` de OTRAS solicitudes del mismo tenant.
func (e *escena) conHistorialDeHerminia(t *testing.T) *escena {
	t.Helper()
	// La 1 es la MÁS ANTIGUA y la 2 la más reciente: así se comprueba de paso que el
	// few-shot llega en el orden del contrato (la última primero) y no en el de
	// inserción.
	aprobadaEnOtraSolicitud(t, e.store, tenantDePrueba, "intake-viejo-1", herminia1, ayer)
	aprobadaEnOtraSolicitud(t, e.store, tenantDePrueba, "intake-viejo-2", herminia2, hoy)
	return e
}

// --- (1) CON HISTORIAL -------------------------------------------------------

// TestSugerir_ConHistorial_UsaElFewShotYDevuelveElTextoDelModelo es la primera
// conducta del criterio. Comprueba TRES cosas y no una:
//
//	(a) que se llamó al modelo UNA vez, por el selector, con el tenant y la SESIÓN DE
//	    ORIGEN de la solicitud —ese segundo dato es el que enruta la inferencia al Edge
//	    correcto y ya se perdió una vez en este plan (T1.7-8)—;
//	(b) que las dos cotizaciones de Herminia viajaron como `Examples`, que es
//	    literalmente el few-shot de D-044.11;
//	(c) que el borrador que viajó lleva los importes de las líneas y NO lleva el SKU.
func TestSugerir_ConHistorial_UsaElFewShotYDevuelveElTextoDelModelo(t *testing.T) {
	e := nuevaEscena(t, artefactoP5(t, textoDelModelo)).conHistorialDeHerminia(t)

	out, err := e.svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba)
	if err != nil {
		t.Fatalf("Sugerir: %v", err)
	}

	if out.Origen != quotetext.OrigenLLM {
		t.Fatalf("origen = %q (motivo %q); se esperaba %q", out.Origen, out.Motivo, quotetext.OrigenLLM)
	}
	if out.Texto != textoDelModelo {
		t.Errorf("el texto devuelto no es el del modelo:\n%q", out.Texto)
	}
	if out.Motivo != "" {
		t.Errorf("un texto del modelo no puede traer motivo de fallback; trajo %q", out.Motivo)
	}

	// (a)
	if got := e.sel.veces(); got != 1 {
		t.Fatalf("se le pidió el provider al selector %d veces; se esperaba 1", got)
	}
	if e.sel.tenants[0] != tenantDePrueba || e.sel.sesiones[0] != sesionDePrueba {
		t.Errorf("el selector recibió (tenant=%q, sesión=%q); se esperaba (%q, %q)",
			e.sel.tenants[0], e.sel.sesiones[0], tenantDePrueba, sesionDePrueba)
	}

	// (b) — el few-shot, ENTERO y con las dos.
	in := e.prov.ultima(t)
	if len(in.Examples) != 2 {
		t.Fatalf("el few-shot llevó %d ejemplos; se esperaban 2", len(in.Examples))
	}
	// El orden es el del contrato: la MÁS RECIENTE primero.
	for i, quería := range []string{herminia2, herminia1} {
		if in.Examples[i] != quería {
			t.Errorf("Examples[%d] no es la cotización de Herminia:\n%q", i, in.Examples[i])
		}
	}

	// (c) — el borrador.
	borrador := string(in.Quote)
	for _, importe := range []string{"2100", "2950", "490", "5540"} {
		if !strings.Contains(borrador, importe) {
			t.Errorf("el borrador que viajó al prompt no trae el importe %s:\n%s", importe, borrador)
		}
	}
	if strings.Contains(borrador, "TORTA-CHOC") || strings.Contains(borrador, intakes.ShippingSKU) {
		t.Errorf("el borrador NO debe llevar los SKU internos al prompt:\n%s", borrador)
	}
}

// --- (2) PRECIO ALTERADO -----------------------------------------------------

// TestSugerir_PrecioAlteradoPorElModelo_CaeAlDeterminista es la segunda conducta.
//
// 🔴 EL ASSERT NEGATIVO NO ES VACUO, Y ASÍ SE DEMUESTRA: el mismo montaje con la
// respuesta BUENA devuelve OrigenLLM (el test de arriba), y aquí se comprueba ADEMÁS
// que al modelo SE LE LLAMÓ (`prov.veces() == 1`). Sin esa segunda comprobación, este
// test pasaría igual si el generador nunca hubiera llegado a llamar —por ejemplo, si
// el few-shot se hubiera quedado vacío por un cambio en el store— y estaría midiendo
// la rama equivocada.
func TestSugerir_PrecioAlteradoPorElModelo_CaeAlDeterminista(t *testing.T) {
	// El modelo se inventa el precio de la segunda torta: 2950 → 3000.
	alterado := strings.Replace(textoDelModelo, "$2950", "$3000", 1)
	if alterado == textoDelModelo {
		t.Fatal("el fixture no se alteró: el test no estaría probando nada")
	}
	e := nuevaEscena(t, artefactoP5(t, alterado)).conHistorialDeHerminia(t)

	out, err := e.svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba)
	if err != nil {
		t.Fatalf("Sugerir: %v", err)
	}

	if got := e.prov.veces(); got != 1 {
		t.Fatalf("se llamó al modelo %d veces; con historial tiene que llamarse 1 "+
			"(si es 0, este test no está mirando la rama que dice)", got)
	}
	if out.Origen != quotetext.OrigenDeterminista {
		t.Fatalf("origen = %q; un precio inventado tiene que caer al determinista", out.Origen)
	}
	if out.Motivo != quotetext.MotivoImporteAjeno {
		t.Errorf("motivo = %q; se esperaba %q", out.Motivo, quotetext.MotivoImporteAjeno)
	}
	if strings.Contains(out.Texto, "3000") {
		t.Errorf("el importe inventado se coló en el texto devuelto:\n%q", out.Texto)
	}
	if out.Texto != quotetext.Render(quotetext.BorradorDe(lineasFusion)) {
		t.Errorf("el texto devuelto no es el render determinista:\n%q", out.Texto)
	}
}

// --- (3) SIN HISTORIAL -------------------------------------------------------

// TestSugerir_SinHistorial_NiSiquieraPideProvider es la tercera conducta, y la que el
// criterio pide con más letra: «tenant sin historial ⇒ render determinista directo
// (sin llamada)».
//
// Se comprueba con DOS contadores en cero —el del modelo y el del SELECTOR— porque
// «no se llamó al LLM» es más fuerte que «no se le pidió que redactara»: pedirle el
// provider al selector ya toca `tenant_llm` y ya puede disparar un aviso de
// degradación. Y el provFake devuelve error a propósito: si alguien lo llamara, el
// motivo sería `llm_fallo` y no `sin_ejemplos`, así que el test fallaría por DOS sitios.
//
// 🔴 LA RAMA SÍ SE RECORRE EN EL ESCENARIO GEMELO, y aquí está la prueba: el sub-test
// de abajo usa EL MISMO montaje con el historial sembrado y exige que entonces el
// selector SÍ se llame. Sin él, este assert negativo sería decorado.
func TestSugerir_SinHistorial_NiSiquieraPideProvider(t *testing.T) {
	t.Run("sin historial: cero llamadas", func(t *testing.T) {
		e := nuevaEscena(t, nil)
		e.prov.err = errProveedorMuerto

		out, err := e.svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba)
		if err != nil {
			t.Fatalf("Sugerir: %v", err)
		}
		if got := e.sel.veces(); got != 0 {
			t.Errorf("se le pidió el provider al selector %d veces; sin ejemplos no se pide ninguna", got)
		}
		if got := e.prov.veces(); got != 0 {
			t.Errorf("se llamó al modelo %d veces; sin ejemplos no se llama ninguna", got)
		}
		if out.Origen != quotetext.OrigenDeterminista || out.Motivo != quotetext.MotivoSinEjemplos {
			t.Fatalf("origen=%q motivo=%q; se esperaba (%q, %q)",
				out.Origen, out.Motivo, quotetext.OrigenDeterminista, quotetext.MotivoSinEjemplos)
		}
		if out.Texto != quotetext.Render(quotetext.BorradorDe(lineasFusion)) {
			t.Errorf("el texto no es el render determinista:\n%q", out.Texto)
		}
	})

	t.Run("control: con historial, el mismo montaje SÍ llama", func(t *testing.T) {
		e := nuevaEscena(t, artefactoP5(t, textoDelModelo)).conHistorialDeHerminia(t)
		if _, err := e.svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba); err != nil {
			t.Fatalf("Sugerir: %v", err)
		}
		if got := e.sel.veces(); got != 1 {
			t.Fatalf("con historial el selector tiene que llamarse 1 vez y se llamó %d "+
				"(si es 0, el assert negativo del sub-test anterior no prueba nada)", got)
		}
	})
}

// --- el resto de los desenlaces ---------------------------------------------

// TestSugerir_ElModeloNoRompeNadaCuandoFalla recorre los tres tropiezos del camino
// LLM y exige el mismo desenlace: 200 lógico, texto determinista y motivo nombrado.
//
// Ninguno devuelve error, y eso es el contrato: el dueño está esperando una sugerencia
// y un proveedor caído no puede convertirse en una pantalla rota.
func TestSugerir_ElModeloNoRompeNadaCuandoFalla(t *testing.T) {
	determinista := quotetext.Render(quotetext.BorradorDe(lineasFusion))
	casos := []struct {
		nombre  string
		prepara func(*escena)
		motivo  string
	}{
		{"el selector no da provider", func(e *escena) { e.sel.err = errProveedorMuerto },
			quotetext.MotivoProveedorNoDisponible},
		{"el provider devuelve error", func(e *escena) { e.prov.err = errProveedorMuerto },
			quotetext.MotivoLLMFallo},
		{"la salida no es el artefacto P5", func(e *escena) { e.prov.respuesta = json.RawMessage(`{"nope":1}`) },
			quotetext.MotivoSalidaIlegible},
		{"el texto no dice ni un precio", func(e *escena) {
			e.prov.respuesta = artefactoP5(t, "Hola! ya te paso el presupuesto, dame un rato")
		}, quotetext.MotivoSinImportesEnTexto},
		{"al texto le falta el total", func(e *escena) {
			e.prov.respuesta = artefactoP5(t, "Torta $2100, la otra $2950 y el envío $490")
		}, quotetext.MotivoFaltaTotal},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			e := nuevaEscena(t, artefactoP5(t, textoDelModelo)).conHistorialDeHerminia(t)
			c.prepara(e)

			out, err := e.svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba)
			if err != nil {
				t.Fatalf("Sugerir devolvió error y no debe: %v", err)
			}
			if out.Origen != quotetext.OrigenDeterminista || out.Motivo != c.motivo {
				t.Fatalf("origen=%q motivo=%q; se esperaba (%q, %q)",
					out.Origen, out.Motivo, quotetext.OrigenDeterminista, c.motivo)
			}
			if out.Texto != determinista {
				t.Errorf("el texto no es el render determinista:\n%q", out.Texto)
			}
		})
	}
}

// TestSugerir_HistorialCaido_SigueDandoElDeterminista: si la BD del historial falla,
// el generador no puede tumbar la petición del dueño.
func TestSugerir_HistorialCaido_SigueDandoElDeterminista(t *testing.T) {
	store := intakes.NewMemoryStore()
	store.Add(tenantDePrueba, intakes.Intake{
		ID: intakeDePrueba, Status: intakes.StatusPendingApproval,
		SessionID: sesionDePrueba, Total: totalFusion,
	}, lineasFusion...)
	prov := &provFake{err: errProveedorMuerto}
	sel := &selFake{prov: prov}
	svc, err := quotetext.NewServicio(logger.New(logger.WithWriter(&strings.Builder{})),
		store, historialRoto{err: errProveedorMuerto}, sel)
	if err != nil {
		t.Fatalf("NewServicio: %v", err)
	}

	out, err := svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba)
	if err != nil {
		t.Fatalf("Sugerir: %v", err)
	}
	if out.Origen != quotetext.OrigenDeterminista || out.Motivo != quotetext.MotivoSinEjemplos {
		t.Fatalf("origen=%q motivo=%q", out.Origen, out.Motivo)
	}
	if sel.veces() != 0 {
		t.Errorf("con el historial caído y sin semilla no hay ejemplos, así que no se pide provider")
	}
}

// TestSugerir_Precondiciones son las dos puertas que se reusan de `Approve`: sin
// líneas de cliente y con líneas sin precio no hay nada que sugerir.
func TestSugerir_Precondiciones(t *testing.T) {
	t.Run("sin líneas de cliente", func(t *testing.T) {
		store := intakes.NewMemoryStore()
		store.Add(tenantDePrueba, intakes.Intake{ID: intakeDePrueba, Status: intakes.StatusPendingApproval},
			intakes.Item{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1, UnitPrice: 490})
		svc := servicioSobre(t, store)

		if _, err := svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba); err == nil ||
			!strings.Contains(err.Error(), "líneas que cotizar") {
			t.Fatalf("err = %v; se esperaba ErrSinLineas", err)
		}
	})

	t.Run("la solicitud no es del tenant", func(t *testing.T) {
		store := intakes.NewMemoryStore()
		store.Add("otro-tenant", intakes.Intake{ID: intakeDePrueba, Status: intakes.StatusPendingApproval},
			lineasFusion...)
		svc := servicioSobre(t, store)

		if _, err := svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba); err == nil {
			t.Fatal("se esperaba ErrNotFound y no hubo error")
		}
	})

	t.Run("hay una línea sin precio en la revisión vigente", func(t *testing.T) {
		store := intakes.NewMemoryStore()
		store.Add(tenantDePrueba, intakes.Intake{ID: intakeDePrueba, Status: intakes.StatusPendingApproval},
			lineasFusion...)
		if _, err := store.InsertRevision(context.Background(), intakes.Revision{
			IntakeID: intakeDePrueba, Kind: intakes.RevisionKindInterpreted,
			Payload: json.RawMessage(`{"version":1,"lines":[{"label":"Torta vainilla","unit_price":null}]}`),
		}); err != nil {
			t.Fatalf("sembrando la revisión interpretada: %v", err)
		}
		svc := servicioSobre(t, store)

		var pend *intakes.PendingPriceError
		_, err := svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba)
		if !asPendingPrice(err, &pend) {
			t.Fatalf("err = %v; se esperaba *intakes.PendingPriceError", err)
		}
		if len(pend.Lines) != 1 {
			t.Errorf("el error trae %d líneas sin precio; se esperaba 1", len(pend.Lines))
		}
	})
}

// TestSugerir_TodasLasLineasPorConfirmar_NoLlamaAlModelo: sin ni un importe, no hay
// nada que el modelo pueda copiar ni que se le pueda verificar.
func TestSugerir_TodasLasLineasPorConfirmar_NoLlamaAlModelo(t *testing.T) {
	store := intakes.NewMemoryStore()
	store.Add(tenantDePrueba, intakes.Intake{ID: intakeDePrueba, Status: intakes.StatusPendingApproval},
		intakes.Item{SKU: "TORTA", Label: "Torta por presupuestar", Qty: 1})
	aprobadaEnOtraSolicitud(t, store, tenantDePrueba, "intake-viejo-1", herminia1, hoy)

	prov := &provFake{err: errProveedorMuerto}
	sel := &selFake{prov: prov}
	svc, err := quotetext.NewServicio(logger.New(logger.WithWriter(&strings.Builder{})), store, store, sel)
	if err != nil {
		t.Fatalf("NewServicio: %v", err)
	}

	out, err := svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba)
	if err != nil {
		t.Fatalf("Sugerir: %v", err)
	}
	if out.Motivo != quotetext.MotivoSinImportes {
		t.Fatalf("motivo = %q; se esperaba %q", out.Motivo, quotetext.MotivoSinImportes)
	}
	if sel.veces() != 0 {
		t.Errorf("no hay importes: no se pide provider (se pidió %d veces)", sel.veces())
	}
}

// servicioSobre arma un servicio mínimo sobre el store dado, con un provider que
// revienta si lo llaman: los tests de precondición no deben llegar nunca a él.
func servicioSobre(t *testing.T, store *intakes.MemoryStore) *quotetext.Servicio {
	t.Helper()
	svc, err := quotetext.NewServicio(logger.New(logger.WithWriter(&strings.Builder{})),
		store, store, &selFake{err: errProveedorMuerto})
	if err != nil {
		t.Fatalf("NewServicio: %v", err)
	}
	return svc
}
