package stages_test

// Trozo de p4_test.go (E-13): LA FECHA — la calcula Go contra el `message_ts` en la zona
// de la etapa, antes de llamar al modelo y aunque no haya ítems; la del modelo no se usa.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
)

// TestP4Run_ZoneGovernsTheDay_AndTodayItIsUTC deja CLAVADO el hueco: no hay zona horaria
// por tenant, así que hoy manda DefaultZone = UTC, y con UTC un mensaje de la noche en
// UTC−3 se fecha con el día siguiente. El test NO dice que eso esté bien: dice qué hace
// el sistema HOY, para que un cambio de zona no pase en silencio.
func TestP4Run_ZoneGovernsTheDay_AndTodayItIsUTC(t *testing.T) {
	minus3 := time.FixedZone("-03", -3*60*60)
	cases := []struct {
		name      string
		messageTS time.Time
		hint      string
		wantBasis string
		wantDate  string
	}{
		{"a morning message falls on the same day in both zones",
			ambarMessageTS, ambarHintText, "message_ts=2026-07-13", "2026-07-22"},
		{"the gap: monday 22:00 in UTC-3 is already tuesday in UTC",
			time.Date(2026, 7, 13, 22, 0, 0, 0, minus3), "mañana", "message_ts=2026-07-14", "2026-07-15"},
		{"the gap costs seven days on a sunday night",
			time.Date(2026, 7, 12, 22, 0, 0, 0, minus3), "el lunes de la semana que viene", "message_ts=2026-07-13", "2026-07-20"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newP4Bench(t, ambarP4Output, nil)
			job := ambarP4Job()
			job.MessageTS = c.messageTS
			art, err := b.stage.Run(context.Background(), job, ambarText, ambarSpecs(), &llm.Hint{Text: c.hint, Evidence: c.hint})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if art.DeliveryDateBasis != c.wantBasis || art.DeliveryDate != c.wantDate {
				t.Fatalf("fecha = %q con base %q; se esperaba %q con base %q",
					art.DeliveryDate, art.DeliveryDateBasis, c.wantDate, c.wantBasis)
			}
		})
	}
}

// TestP4Run_AnotherZone_CountsTheDayThere: la zona entra por el constructor y es la que
// decide en qué día cae el mensaje. Con UTC−3, el mensaje del lunes a las 22:00 sigue
// siendo lunes.
func TestP4Run_AnotherZone_CountsTheDayThere(t *testing.T) {
	minus3 := time.FixedZone("-03", -3*60*60)
	selector := &fakeSelector{provider: &fakeProvider{}}
	store := &fakeStore{}
	stage, err := stages.NewP4(captureLog(&bytes.Buffer{}), selector, store, minus3)
	if err != nil {
		t.Fatalf("NewP4: %v", err)
	}
	job := ambarP4Job()
	job.MessageTS = time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC) // lunes 13 a las 22:00 en UTC−3

	art, err := stage.Run(context.Background(), job, ambarText, nil, &llm.Hint{Text: "mañana", Evidence: "mañana"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if art.DeliveryDateBasis != "message_ts=2026-07-13" || art.DeliveryDate != "2026-07-14" {
		t.Fatalf("fecha = %q con base %q; en UTC−3 el mensaje es del lunes 13", art.DeliveryDate, art.DeliveryDateBasis)
	}
}

// TestP4Run_ModelDate_IsNotUsed_GoRules es la otra mitad de «la aritmética es de Go»: el
// prompt SÍ le pide una fecha al modelo y el parser SÍ la decodifica, así que hace falta
// una prueba de que no se cuela.
func TestP4Run_ModelDate_IsNotUsed_GoRules(t *testing.T) {
	b := newP4Bench(t, `{"version":1,"delivery_date":"2026-09-01",
		"delivery_date_basis":"message_ts=2026-08-25","items":[
		{"product":"tequeños congelados","qty":1,"unit_kind":"package","package_size":30,
		 "evidence":"un paquete de tequeños congelados de 30"}]}`, nil)

	art, err := b.stage.Run(context.Background(), ambarP4Job(), ambarText, ambarSpecs()[2:], ambarDeliveryHint())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if art.DeliveryDate != "2026-07-22" || art.DeliveryDateBasis != "message_ts=2026-07-13" {
		t.Fatalf("se coló la fecha del modelo: %q / %q", art.DeliveryDate, art.DeliveryDateBasis)
	}
	if strings.Contains(string(b.store.saved[0].Payload), "2026-09-01") || strings.Contains(string(b.store.saved[0].Payload), "2026-08-25") {
		t.Fatalf("la fecha del modelo llegó a la base: %s", b.store.saved[0].Payload)
	}
	if !strings.Contains(b.log.String(), "no coincide con la que calculó Go") {
		t.Fatalf("la discrepancia no dejó aviso en el log:\n%s", b.log.String())
	}
}

// TestP4Run_ModelDateWithoutAGoDate_IsNotUsedEither: si Go no pudo fechar, el artefacto
// sale SIN fecha aunque el modelo proponga una; y cuando las dos coinciden no hay aviso.
func TestP4Run_ModelDateWithoutAGoDate_IsNotUsedEither(t *testing.T) {
	t.Run("the model proposes a date and Go has none", func(t *testing.T) {
		b := newP4Bench(t, ambarP4Output, nil)
		art, err := b.stage.Run(context.Background(), ambarP4Job(), ambarText, ambarSpecs(), nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if art.DeliveryDate != "" || art.DeliveryDateBasis != "" {
			t.Fatalf("se coló la fecha del modelo: %q / %q", art.DeliveryDate, art.DeliveryDateBasis)
		}
		if !strings.Contains(b.log.String(), "no coincide con la que calculó Go") {
			t.Fatalf("la discrepancia no dejó aviso en el log:\n%s", b.log.String())
		}
	})

	t.Run("both dates agree", func(t *testing.T) {
		b, _ := runAmbarP4(t)
		if strings.Contains(b.log.String(), "no coincide con la que calculó Go") {
			t.Fatalf("las dos fechas coinciden y hubo aviso:\n%s", b.log.String())
		}
	})
}

// TestP4Run_NoDate_TheThreeReasons fija los tres desenlaces sin fecha, que NO son
// fallos: el artefacto se persiste igual y sin `delivery_date`. Un presupuesto sin fecha
// lo arregla el dueño preguntando; uno con una fecha inventada no lo arregla nadie.
func TestP4Run_NoDate_TheThreeReasons(t *testing.T) {
	cases := []struct {
		name          string
		hint          *llm.Hint
		withoutTS     bool
		wantLogged    string
		wantNotLogged string
	}{
		{"the customer did not say when", nil, false, "", "sin fecha"},
		{"the hint is not recognized", &llm.Hint{Text: "cuando puedas", Evidence: "cuando puedas"}, false,
			"la pista de entrega no se pudo resolver a una fecha", "cuando puedas"},
		{"the job has no message_ts", ambarDeliveryHint(), true,
			"el job no trae message_ts", ambarHintText},
		{"neither message_ts nor hint", nil, true, "", "message_ts"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newP4Bench(t, tequenosP4Output, nil)
			job := ambarP4Job()
			if c.withoutTS {
				job.MessageTS = time.Time{}
			}
			art, err := b.stage.Run(context.Background(), job, ambarText, ambarSpecs()[2:], c.hint)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if art.DeliveryDate != "" || art.DeliveryDateBasis != "" {
				t.Fatalf("salió fecha donde no la hay: %q / %q", art.DeliveryDate, art.DeliveryDateBasis)
			}
			if len(b.store.saved) != 1 {
				t.Fatalf("el artefacto sin fecha tiene que persistirse igual (guardados: %d)", len(b.store.saved))
			}
			assertExactKeys(t, "artefacto sin fecha", b.store.saved[0].Payload, "version", "items")

			log := b.log.String()
			if c.wantLogged != "" && !strings.Contains(log, c.wantLogged) {
				t.Fatalf("falta el aviso %q: %q", c.wantLogged, log)
			}
			if strings.Contains(log, c.wantNotLogged) {
				t.Fatalf("el log lleva %q y no debía: %q", c.wantNotLogged, log)
			}
			if !strings.Contains(log, "con_fecha=false") {
				t.Fatalf("la línea de cierre no dice que salió sin fecha: %q", log)
			}
		})
	}
}

// TestP4Run_NoItems_CallsNothingButStillDates: la fecha no depende del modelo, así que
// un pedido cuyo P3 se quedó sin ítems conserva el día que el cliente pidió y el dueño
// solo tiene que escribir las líneas.
func TestP4Run_NoItems_CallsNothingButStillDates(t *testing.T) {
	b := newP4Bench(t, ambarP4Output, nil)

	art, err := b.stage.Run(context.Background(), ambarP4Job(), ambarText, nil, ambarDeliveryHint())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(b.calls) != 0 || len(b.selector.asked) != 0 {
		t.Fatalf("llamadas=%d provider=%d; sin ítems no hay nada que normalizar", len(b.calls), len(b.selector.asked))
	}
	if art.DeliveryDate != "2026-07-22" || art.DeliveryDateBasis != "message_ts=2026-07-13" {
		t.Fatalf("fecha = %q con base %q; la fecha no depende del modelo", art.DeliveryDate, art.DeliveryDateBasis)
	}
	if len(b.store.saved) != 1 || len(art.Items) != 0 {
		t.Fatalf("guardados=%d items=%d; se esperaba un artefacto vacío persistido", len(b.store.saved), len(art.Items))
	}
	if !strings.Contains(string(b.store.saved[0].Payload), `"items":[]`) {
		t.Fatalf("el artefacto vacío no lleva `items` como lista vacía: %s", b.store.saved[0].Payload)
	}
}
