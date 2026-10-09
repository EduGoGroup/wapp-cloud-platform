package stages_test

// Trozo de p4_test.go (E-13): LAS CANTIDADES — el paquete que jamás es `qty`, el rango
// que no se colapsa, la cantidad omitida, la fusión por posición con lo que dejó P3 y la
// evidencia que no sustituye si no se sostiene.

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"
)

// TestP4Run_PackageOfThirty_NeverBecomesQtyThirty cubre los DOS lados de la regla, que
// es lo que la hace una garantía y no una petición al modelo: si el modelo se porta bien
// el paquete viaja tal cual, y si comete EL error caro —`qty:30`, que multiplica el
// presupuesto por treinta— Go lo deshace, lleve o no la etiqueta de paquete.
func TestP4Run_PackageOfThirty_NeverBecomesQtyThirty(t *testing.T) {
	cases := []struct {
		name          string
		item          string
		wantCorrected bool
	}{
		{"the model labels the package correctly",
			`{"product":"tequeños congelados","qty":1,"unit_kind":"package","package_size":30,
			  "evidence":"un paquete de tequeños congelados de 30"}`, false},
		{"the model takes the size for the quantity",
			`{"product":"tequeños congelados","qty":30,
			  "evidence":"un paquete de tequeños congelados de 30"}`, true},
		{"the model also labels it: 30 packages of 30 would be 900 units",
			`{"product":"tequeños congelados","qty":30,"unit_kind":"package","package_size":30,
			  "evidence":"un paquete de tequeños congelados de 30"}`, true},
		{"the size is only in the evidence that P3 kept",
			`{"product":"tequeños congelados","qty":30,"evidence":"` + inventedEvidence + `"}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, item := runOneItem(t, ambarSpecs()[2], c.item)
			if item.Qty != 1 {
				t.Fatalf("qty = %d; «un paquete de 30» es UNA unidad de paquete, JAMÁS 30", item.Qty)
			}
			if item.UnitKind != llm.UnitKindPackage || item.PackageSize != 30 {
				t.Fatalf("unit_kind=%q package_size=%d; se esperaba package/30", item.UnitKind, item.PackageSize)
			}
			if got := strings.Contains(b.log.String(), "confundió el TAMAÑO del paquete con la cantidad"); got != c.wantCorrected {
				t.Fatalf("aviso de corrección = %v; se esperaba %v: %q", got, c.wantCorrected, b.log.String())
			}
		})
	}
}

// TestP4Run_PackageRule_OnlyFiresOnAnExactMatch: la red no se pasa de lista. Dispara
// solo cuando el texto dice «paquete(s) de N» y la cantidad es EXACTAMENTE ese N, con
// N > 1; el número puede ir separado de «de» por hasta 40 caracteres SIN dígitos.
func TestP4Run_PackageRule_OnlyFiresOnAnExactMatch(t *testing.T) {
	const literal = "cliente: quiero 2 paquetes de 30 y 4 cajas de 6, un paquete de 1 y " +
		"un paquete de empanadas de carne cortada a cuchillo bien jugosas de 12"
	cases := []struct {
		name     string
		evidence string
		qty      int
		wantQty  int
		wantSize int
	}{
		{"two packages of thirty stay two", "2 paquetes de 30", 2, 2, 0},
		{"plural packages with the size as quantity are corrected", "2 paquetes de 30", 30, 1, 30},
		{"the digit of another product is not the size", "2 paquetes de 30 y 4 cajas de 6", 6, 6, 0},
		{"a package of one is not corrected", "un paquete de 1", 1, 1, 0},
		{"more than forty characters before the number", "un paquete de empanadas de carne cortada a cuchillo bien jugosas de 12", 12, 12, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newP4Bench(t, `{"version":1,"items":[{"product":"cosa","qty":`+strconv.Itoa(c.qty)+`,"evidence":"`+c.evidence+`"}]}`, nil)
			spec := llm.ItemSpec{Product: "cosa", Evidence: c.evidence}
			art, err := b.stage.Run(context.Background(), ambarP4Job(), literal, []llm.ItemSpec{spec}, nil)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			item := art.Items[0]
			if item.Qty != c.wantQty || item.PackageSize != c.wantSize {
				t.Fatalf("qty=%d package_size=%d; se esperaba %d/%d", item.Qty, item.PackageSize, c.wantQty, c.wantSize)
			}
			if wantKind := map[bool]string{true: llm.UnitKindPackage}[c.wantSize > 0]; item.UnitKind != wantKind {
				t.Fatalf("unit_kind = %q; se esperaba %q", item.UnitKind, wantKind)
			}
		})
	}
}

// TestP4Run_Range_IsNotCollapsed fija que «10 o 12 porciones» sale como rango y con la
// cantidad en 1: ni 11, ni 12, ni una torta de más. Elegir dentro del rango es del DUEÑO.
func TestP4Run_Range_IsNotCollapsed(t *testing.T) {
	_, item := runOneItem(t, ambarSpecs()[0],
		`{"product":"torta","qty":1,"range":{"min":10,"max":12,"unit":"porciones"},
		  "evidence":"una torta sería con decoración infantil, de bizcocho húmedo de chocolate"}`)

	if item.Range == nil || *item.Range != (llm.Range{Min: 10, Max: 12, Unit: "porciones"}) {
		t.Fatalf("el rango salió %+v; se esperaba {10 12 porciones}", item.Range)
	}
	if item.Qty != 1 {
		t.Fatalf("qty = %d: el rango se coló en la cantidad", item.Qty)
	}
}

// TestP4Run_OmittedQuantity_IsOneAndNeverZero cubre los dos caminos por los que un ítem
// puede quedarse sin cantidad, que acaban distinto a propósito.
func TestP4Run_OmittedQuantity_IsOneAndNeverZero(t *testing.T) {
	t.Run("an item the model did not return leaves with the neutral normalization", func(t *testing.T) {
		b := newP4Bench(t, `{"version":1,"items":[
			{"product":"torta","qty":2,
			 "evidence":"una torta sería con decoración infantil, de bizcocho húmedo de chocolate"}]}`, nil)
		art, err := b.stage.Run(context.Background(), ambarP4Job(), ambarText, ambarSpecs(), nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(art.Items) != 3 {
			t.Fatalf("items = %d; los TRES ítems de P3 tienen que sobrevivir aunque el modelo devuelva uno", len(art.Items))
		}
		if art.Items[0].Qty != 2 {
			t.Fatalf("qty del ítem que el modelo sí devolvió = %d; se esperaba 2", art.Items[0].Qty)
		}
		specs := ambarSpecs()
		for _, i := range []int{1, 2} {
			want := llm.NormalizedItem{
				Product: specs[i].Product, Qty: 1, Notes: specs[i].Notes, Evidence: specs[i].Evidence,
				AddonCandidates: specs[i].AddonCandidates, Customizations: specs[i].Customizations,
			}
			if !reflect.DeepEqual(art.Items[i], want) {
				t.Fatalf("ítem neutro %d = %+v; se esperaban los datos de P3 y qty 1, sin rango ni paquete: %+v", i, art.Items[i], want)
			}
		}
		if len(b.store.saved) != 1 {
			t.Fatalf("se persistieron %d artefactos", len(b.store.saved))
		}
		if !strings.Contains(b.log.String(), "item_pos=1") || !strings.Contains(b.log.String(), "item_pos=2") {
			t.Fatalf("los ítems que el modelo no devolvió no dejaron aviso con su posición: %q", b.log.String())
		}
	})

	t.Run("a quantity the model omitted is neither persisted as 0 nor dressed up as 1", func(t *testing.T) {
		b := newP4Bench(t, `{"version":1,"items":[
			{"product":"tequeños congelados","evidence":"un paquete de tequeños congelados de 30"}]}`, nil)
		art, err := b.stage.Run(context.Background(), ambarP4Job(), ambarText, ambarSpecs()[2:], nil)
		if !errors.Is(err, llm.ErrLLMQuality) {
			t.Fatalf("err = %v; se esperaba un fallo de calidad", err)
		}
		if !strings.HasPrefix(err.Error(), "p4: la salida del modelo no es un artefacto P4 legible: ") {
			t.Fatalf("texto del error = %q", err.Error())
		}
		if strings.Contains(err.Error(), tequenosEvidence) {
			t.Fatalf("el error cita la salida del modelo: %q", err.Error())
		}
		if art != nil || len(b.store.saved) != 0 || len(b.calls) != 1 {
			t.Fatalf("art=%v guardados=%d llamadas=%d; ni artefacto ni reintento", art, len(b.store.saved), len(b.calls))
		}
	})
}

// TestP4Run_InventedEvidence_KeepsTheOneFromP3: aquí el ítem ya está probado desde P3,
// así que una evidencia inventada NO tumba nada y NO se guarda: simplemente no sustituye
// a la que había.
func TestP4Run_InventedEvidence_KeepsTheOneFromP3(t *testing.T) {
	b, item := runOneItem(t, ambarSpecs()[2],
		`{"product":"tequeños congelados","qty":1,"unit_kind":"package","package_size":30,
		  "evidence":"`+inventedEvidence+`"}`)

	if item.Evidence != tequenosEvidence {
		t.Fatalf("evidence = %q; se esperaba la de P3, que sí está en el literal", item.Evidence)
	}
	if strings.Contains(string(b.store.saved[0].Payload), inventedEvidence) {
		t.Fatal("el artefacto guardó una frase que el cliente nunca escribió")
	}
}

// TestP4Run_AnchoredEvidenceFromTheModel_ReplacesTheOneFromP3: la del modelo sí sustituye
// cuando se sostiene sobre el literal.
func TestP4Run_AnchoredEvidenceFromTheModel_ReplacesTheOneFromP3(t *testing.T) {
	const anchored = "también quería un paquete de tequeños congelados de 30"
	_, item := runOneItem(t, ambarSpecs()[2],
		`{"product":"tequeños congelados","qty":1,"unit_kind":"package","package_size":30,
		  "evidence":"`+anchored+`"}`)

	if item.Evidence != anchored {
		t.Fatalf("evidence = %q; la del modelo aparece en el literal y tenía que sustituir a la de P3", item.Evidence)
	}
}

// TestP4Run_ModelOnlyContributesQuantities: producto, candidatos, personalizaciones y
// notas son SIEMPRE los de P3, aunque el modelo los reescriba. Un producto renombrado
// deja aviso —sin decir con qué—: es la señal barata de que el modelo reordenó los ítems.
func TestP4Run_ModelOnlyContributesQuantities(t *testing.T) {
	b, item := runOneItem(t, ambarSpecs()[0],
		`{"product":"pastel renombrado","qty":3,
		  "addon_candidates":["velas"],"customizations":["con nueces"],"notes":"otra nota",
		  "evidence":"una torta sería con decoración infantil, de bizcocho húmedo de chocolate"}`)

	spec := ambarSpecs()[0]
	if item.Product != spec.Product || item.Notes != spec.Notes ||
		!reflect.DeepEqual(item.AddonCandidates, spec.AddonCandidates) ||
		!reflect.DeepEqual(item.Customizations, spec.Customizations) {
		t.Fatalf("el modelo reescribió lo que es de P3: %+v", item)
	}
	if item.Qty != 3 {
		t.Fatalf("qty = %d; la cantidad sí es del modelo", item.Qty)
	}
	log := b.log.String()
	if !strings.Contains(log, "el modelo renombró el producto") || !strings.Contains(log, "item_pos=0") {
		t.Fatalf("renombrar el producto no dejó aviso con la posición: %q", log)
	}
	if strings.Contains(log, "pastel renombrado") {
		t.Fatalf("el aviso dice con qué se renombró: %q", log)
	}
}

// TestP4Run_SameProductInAnotherCase_IsNotARename: el producto se compara normalizado.
func TestP4Run_SameProductInAnotherCase_IsNotARename(t *testing.T) {
	b, _ := runOneItem(t, ambarSpecs()[2],
		`{"product":"  Tequeños   CONGELADOS ","qty":1,"unit_kind":"package","package_size":30,
		  "evidence":"un paquete de tequeños congelados de 30"}`)

	if strings.Contains(b.log.String(), "renombró el producto") {
		t.Fatalf("otra caja y otros blancos no son un renombrado: %q", b.log.String())
	}
}

// TestP4Run_MoreItemsThanP3_TheExtraOnesAreDropped protege la asimetría de la fusión: de
// menos se completa, de más se descarta. Un ítem de más es una línea de más COBRADA.
func TestP4Run_MoreItemsThanP3_TheExtraOnesAreDropped(t *testing.T) {
	b := newP4Bench(t, `{"version":1,"items":[
		{"product":"tequeños congelados","qty":1,"unit_kind":"package","package_size":30,
		 "evidence":"un paquete de tequeños congelados de 30"},
		{"product":"tequeños congelados","qty":1,"unit_kind":"package","package_size":30,
		 "evidence":"un paquete de tequeños congelados de 30"}]}`, nil)

	art, err := b.stage.Run(context.Background(), ambarP4Job(), ambarText, ambarSpecs()[2:], nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(art.Items) != 1 {
		t.Fatalf("items = %d; P3 dejó UNO y un ítem de más es una línea cobrada de más", len(art.Items))
	}
	if log := b.log.String(); !strings.Contains(log, "los sobrantes se descartan") || !strings.Contains(log, "descartados=1") {
		t.Fatalf("el descarte no dejó aviso con su cuenta: %q", log)
	}
}

// TestP4Run_SavedArtifactAlwaysPassesItsOwnValidator es I-CP-1 visto desde la etapa: lo
// que P4 escribe tiene que poder volver a entrar por `llm.ParseQuantities` —el lector
// del match y del borrador— por TODOS los caminos, también los que Go completa o corrige.
func TestP4Run_SavedArtifactAlwaysPassesItsOwnValidator(t *testing.T) {
	cases := []struct {
		name      string
		response  string
		specs     []llm.ItemSpec
		wantItems int
	}{
		{"the Ambar case", ambarP4Output, ambarSpecs(), 3},
		{"neutral items for what the model did not return", tequenosP4Output, ambarSpecs(), 3},
		{"a corrected package", `{"version":1,"items":[{"product":"tequeños congelados","qty":30,
			"evidence":"un paquete de tequeños congelados de 30"}]}`, ambarSpecs()[2:], 1},
		{"no items at all", ambarP4Output, nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newP4Bench(t, c.response, nil)
			if _, err := b.stage.Run(context.Background(), ambarP4Job(), ambarText, c.specs, ambarDeliveryHint()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			read, err := llm.ParseQuantities(b.store.saved[0].Payload)
			if err != nil {
				t.Fatalf("el artefacto persistido no pasa el parser compartido: %v\n%s", err, b.store.saved[0].Payload)
			}
			if read.DeliveryDate != "2026-07-22" || len(read.Items) != c.wantItems {
				t.Fatalf("el parser leyó otra cosa: %q con %d ítems", read.DeliveryDate, len(read.Items))
			}
			for i, item := range read.Items {
				if item.Qty < 1 {
					t.Fatalf("items[%d].qty = %d: el artefacto lleva un valor que su propio validador rechaza", i, item.Qty)
				}
			}
		})
	}
}

// TestP4Run_NoCustomerTextInTheLog es la regla de ADR-0034 / INV-6 en la etapa que más
// cerca está de romperla: aquí se avisa de pistas que no se resuelven, de evidencias
// inventadas, de productos renombrados y de paquetes corregidos, y NINGUNO de esos
// avisos puede llevar la palabra del cliente que lo provocó.
func TestP4Run_NoCustomerTextInTheLog(t *testing.T) {
	b := newP4Bench(t, `{"version":1,"items":[
		{"product":"tequenios","qty":30,"evidence":"`+inventedEvidence+`"}]}`, nil)

	hint := &llm.Hint{Text: "cuando terminen las vacaciones", Evidence: "cuando terminen las vacaciones"}
	if _, err := b.stage.Run(context.Background(), ambarP4Job(), ambarText, ambarSpecs()[2:], hint); err != nil {
		t.Fatalf("Run: %v", err)
	}

	log := b.log.String()
	for _, want := range []string{"no se pudo resolver a una fecha", "se conserva la de P3", "renombró el producto", "TAMAÑO del paquete"} {
		if !strings.Contains(log, want) {
			t.Fatalf("falta el aviso %q, así que el test no probaría nada: %q", want, log)
		}
	}
	for _, forbidden := range []string{"vacaciones", inventedEvidence, tequenosEvidence, "tequenios"} {
		if strings.Contains(log, forbidden) {
			t.Fatalf("el log filtró texto del cliente (%q):\n%s", forbidden, log)
		}
	}
}
