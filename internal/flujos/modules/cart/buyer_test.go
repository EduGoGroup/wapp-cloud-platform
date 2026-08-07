// buyer_test.go prueba el paso de DATOS DEL COMPRADOR del carrito (Plan 041 ·
// T4.5, D-041.13).
//
// El PRIMER test del archivo es la regresión de teclas (INV-15) y está antes que
// la funcionalidad a propósito, igual que en notes_test.go: este paso se intercala
// entre "confirmo" y el pedido cerrado, que es el sitio de más tráfico del carrito,
// y el criterio que manda es que un tenant SIN checklist —todos los de hoy— teclee
// exactamente lo mismo que ayer.
//
// El SEGUNDO grupo es la prueba de que el valor no se queda en el estado
// conversacional. No comprueba que el código no lo escriba: comprueba que el JSON
// que se persistiría en public.flow_state.vars no lo CONTIENE. Leer el código y
// concluir que no hay fuga es lo que este archivo existe para no tener que hacer.
package cart

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// rutDelCliente y direcciónDelCliente son los valores que el cliente teclea en los
// tests. Son literales RAROS a propósito: se buscan luego, como subcadena, en el
// JSON del estado, en los flow_events y en el summary. Un valor corriente ("Juan")
// podría aparecer por casualidad en otro campo y dar un falso positivo o negativo.
const (
	rutDelCliente        = "12.345.678-K-zzq"
	direcciónDelCliente  = "Pasaje Los Aromos 4412-zzq, depto 7"
	checklistTenantVacío = "el tenant sin checklist"
)

// varsConChecklist siembra el catálogo Y el checklist del comprador, como haría la
// ResumePolicy leyendo tenant_settings.buyer_fields.
func varsConChecklist(fields ...store.BuyerField) map[string]any {
	vars := seededVars()
	vars[VarBuyerFields] = fields
	return vars
}

// dosCamposRequeridos es el checklist del criterio de T4.5 más un segundo campo:
// con UNO solo no se puede distinguir "pregunta el checklist entero" de "pregunta
// una vez y cierra".
func dosCamposRequeridos() []store.BuyerField {
	return []store.BuyerField{
		{Key: "rut", Label: "RUT", Required: true},
		{Key: "direccion", Label: "Dirección de entrega", Required: true},
	}
}

// varsJSON serializa las Vars EXACTAMENTE como el engine las persiste en
// public.flow_state.vars (JSONB). Es el material sobre el que se buscan fugas: lo
// que no esté aquí no llega a la base.
func varsJSON(t *testing.T, vars map[string]any) string {
	t.Helper()
	b, err := json.Marshal(vars)
	if err != nil {
		t.Fatalf("serializando Vars: %v", err)
	}
	return string(b)
}

// TestINV15CompraSinChecklistMismasPulsaciones es la RED DE REGRESIÓN DE TECLAS de
// esta tarea. Con buyer_fields vacío —el DEFAULT de la columna y el estado de todos
// los tenants de hoy— las SEIS pulsaciones de siempre cierran el pedido, sin un
// paso de más y sin efectos nuevos.
func TestINV15CompraSinChecklistMismasPulsaciones(t *testing.T) {
	casos := map[string]map[string]any{
		checklistTenantVacío:        seededVars(),       // ni siquiera se sembró la clave
		"el tenant con lista vacía": varsConChecklist(), // buyer_fields = []
		"el tenant con un opcional": varsConChecklist(store.BuyerField{Key: "referencia", Label: "Referencia", Required: false}),
		"el tenant con clave vacía": varsConChecklist(store.BuyerField{Key: "", Label: "Sin clave", Required: true}),
	}
	for nombre, iniciales := range casos {
		t.Run(nombre, func(t *testing.T) {
			m := New()
			vars := iniciales
			var cerrado bool
			for i, tecla := range comprarSinComentario {
				res := m.Step(model.Node{}, model.Conversation{Vars: vars}, tecla)
				vars = res.Vars
				st := loadState(vars)
				if st.Level != nivelesDelRecorrido[i] {
					t.Fatalf("INV-15 ROTA en la pulsación %d (%q): el carrito quedó en %q y antes de T4.5 quedaba en %q.\n"+
						"Pantalla emitida:\n%s", i+1, tecla, st.Level, nivelesDelRecorrido[i], joined(res.Outputs))
				}
				for _, eff := range res.Effects {
					if eff.Name == EffectCartClosed {
						cerrado = true
						if got := eff.Payload["total"]; got != 2.5 {
							t.Fatalf("el total del cierre cambió: %v (esperado 2.5)", got)
						}
					}
					if eff.Name == EffectBuyerDataCaptured {
						t.Fatalf("INV-15 ROTA: sin checklist se emitió %q en la pulsación %d",
							EffectBuyerDataCaptured, i+1)
					}
				}
			}
			if !cerrado {
				t.Fatalf("INV-15 ROTA: tras las %d pulsaciones de siempre el pedido no cerró",
					len(comprarSinComentario))
			}
		})
	}
}

// TestBuyerDataPreguntaCadaRequeridoAntesDeCerrar conduce el recorrido completo con
// checklist: seis pulsaciones de siempre + un mensaje por campo. El pedido cierra
// en el ÚLTIMO campo, no antes: el checklist es un requisito del cierre, no una
// encuesta posterior.
func TestBuyerDataPreguntaCadaRequeridoAntesDeCerrar(t *testing.T) {
	m := New()
	vars := varsConChecklist(dosCamposRequeridos()...)

	// Las seis de siempre. La última ("1", confirmar) ya NO cierra: abre el checklist.
	for _, tecla := range comprarSinComentario {
		vars = m.Step(model.Node{}, model.Conversation{Vars: vars}, tecla).Vars
	}
	st := loadState(vars)
	if st.Level != LevelBuyerData {
		t.Fatalf("tras confirmar con checklist el carrito quedó en %q, esperaba %q", st.Level, LevelBuyerData)
	}

	// Primer campo: se captura y se pregunta el segundo, sin cerrar todavía.
	res := m.Step(model.Node{}, model.Conversation{Vars: vars}, rutDelCliente)
	vars = res.Vars
	if st = loadState(vars); st.Level != LevelBuyerData || st.BuyerIdx != 1 {
		t.Fatalf("tras el primer campo: level=%q buyer_idx=%d (esperaba %q / 1)", st.Level, st.BuyerIdx, LevelBuyerData)
	}
	if !strings.Contains(joined(res.Outputs), "Dirección de entrega") {
		t.Fatalf("tras el primer campo no se preguntó el segundo:\n%s", joined(res.Outputs))
	}
	for _, eff := range res.Effects {
		if eff.Name == EffectCartClosed {
			t.Fatalf("el pedido cerró con el checklist a medias")
		}
	}

	// Segundo campo: se captura Y cierra en el mismo turno.
	res = m.Step(model.Node{}, model.Conversation{Vars: vars}, direcciónDelCliente)
	if st = loadState(res.Vars); st.Level != LevelClosed {
		t.Fatalf("tras el último campo el carrito quedó en %q, esperaba %q", st.Level, LevelClosed)
	}
	if !strings.Contains(joined(res.Outputs), "Pedido confirmado") {
		t.Fatalf("la pantalla final no es la confirmación:\n%s", joined(res.Outputs))
	}

	// ORDEN de los efectos: los del comprador ANTES de cart_closed. Es lo que
	// permite al proyector encontrar la solicitud todavía ABIERTA donde colgarlos.
	orden := make([]string, 0, len(res.Effects))
	for _, eff := range res.Effects {
		orden = append(orden, eff.Name)
	}
	if len(orden) != 2 || orden[0] != EffectBuyerDataCaptured || orden[1] != EffectCartClosed {
		t.Fatalf("orden de efectos del cierre = %v; esperaba [%s %s]",
			orden, EffectBuyerDataCaptured, EffectCartClosed)
	}
}

// TestBuyerDataElValorNoQuedaEnElEstadoConversacional es la prueba de NO FUGA al
// estado. public.flow_state.vars es JSONB EN CLARO: si el RUT del cliente cayera
// ahí, estaría sin cifrar y sobreviviría toda la conversación, que es justo lo que
// la fila cifrada de intake_buyer_data existe para evitar.
//
// No se lee el código para concluirlo: se serializan las Vars como las serializa el
// engine y se busca el valor dentro.
func TestBuyerDataElValorNoQuedaEnElEstadoConversacional(t *testing.T) {
	m := New()
	vars := varsConChecklist(dosCamposRequeridos()...)
	for _, tecla := range comprarSinComentario {
		vars = m.Step(model.Node{}, model.Conversation{Vars: vars}, tecla).Vars
	}

	for _, valor := range []string{rutDelCliente, direcciónDelCliente} {
		vars = m.Step(model.Node{}, model.Conversation{Vars: vars}, valor).Vars
		if blob := varsJSON(t, vars); strings.Contains(blob, valor) {
			t.Fatalf("FUGA: el valor %q del comprador quedó en el estado conversacional que se persiste:\n%s",
				valor, blob)
		}
	}
	// Y lo que SÍ queda es el contador, que es lo único que el paso necesita
	// recordar entre mensajes.
	if st := loadState(vars); st.BuyerIdx != 2 {
		t.Fatalf("buyer_idx = %d, esperaba 2 (dos campos capturados)", st.BuyerIdx)
	}
}

// TestBuyerDataEfectoEsPrivadoYLlevaElCampo fija el contrato del efecto: Kind
// modules.KindPrivate —el que hace que el PersistSink NO lo escriba en
// flow_events— y payload con la clave y el valor, que es lo que el proyector
// necesita para cifrarlo.
func TestBuyerDataEfectoEsPrivadoYLlevaElCampo(t *testing.T) {
	m := New()
	vars := varsConChecklist(store.BuyerField{Key: "rut", Label: "RUT", Required: true})
	for _, tecla := range comprarSinComentario {
		vars = m.Step(model.Node{}, model.Conversation{Vars: vars}, tecla).Vars
	}
	res := m.Step(model.Node{}, model.Conversation{Vars: vars}, rutDelCliente)

	var capturado *modules.Effect
	for i, eff := range res.Effects {
		if eff.Name == EffectBuyerDataCaptured {
			capturado = &res.Effects[i]
		}
	}
	if capturado == nil {
		t.Fatalf("no se emitió %q", EffectBuyerDataCaptured)
	}
	if capturado.Kind != modules.KindPrivate {
		t.Fatalf("Kind del efecto = %q, esperaba %q: con otro Kind el sink lo escribiría EN CLARO en flow_events",
			capturado.Kind, modules.KindPrivate)
	}
	if got := capturado.Payload["key"]; got != "rut" {
		t.Fatalf("payload[key] = %v, esperaba \"rut\"", got)
	}
	if got := capturado.Payload["value"]; got != rutDelCliente {
		t.Fatalf("payload[value] = %v, esperaba el valor tecleado", got)
	}
}

// TestBuyerDataCeroVuelveAlResumenSinReDesandarLoCapturado: el 0 del checklist
// vuelve al resumen, y volver a confirmar RETOMA donde se quedó. Volver a
// preguntar un dato personal que el cliente ya dio —y que ya está guardado y
// cifrado— sería pedirle dos veces lo mismo y guardarlo dos veces.
func TestBuyerDataCeroVuelveAlResumenSinReDesandarLoCapturado(t *testing.T) {
	m := New()
	vars := varsConChecklist(dosCamposRequeridos()...)
	for _, tecla := range comprarSinComentario {
		vars = m.Step(model.Node{}, model.Conversation{Vars: vars}, tecla).Vars
	}
	vars = m.Step(model.Node{}, model.Conversation{Vars: vars}, rutDelCliente).Vars

	res := m.Step(model.Node{}, model.Conversation{Vars: vars}, "0")
	vars = res.Vars
	if st := loadState(vars); st.Level != LevelSummary {
		t.Fatalf("el 0 del checklist dejó el carrito en %q, esperaba %q", st.Level, LevelSummary)
	}
	if !strings.Contains(joined(res.Outputs), "Resumen del pedido") {
		t.Fatalf("el 0 no volvió al resumen:\n%s", joined(res.Outputs))
	}

	res = m.Step(model.Node{}, model.Conversation{Vars: vars}, "1")
	if st := loadState(res.Vars); st.BuyerIdx != 1 {
		t.Fatalf("al reconfirmar, buyer_idx = %d: se perdió lo ya capturado", st.BuyerIdx)
	}
	if !strings.Contains(joined(res.Outputs), "Dirección de entrega") {
		t.Fatalf("al reconfirmar se volvió a preguntar un campo ya dado:\n%s", joined(res.Outputs))
	}
}

// TestBuyerDataCampoObligatorioNoSeSalta: lo que quede vacío tras sanear
// repregunta el MISMO campo. Un `required` que se pudiera saltar con un espacio no
// sería un requisito.
func TestBuyerDataCampoObligatorioNoSeSalta(t *testing.T) {
	m := New()
	vars := varsConChecklist(store.BuyerField{Key: "rut", Label: "RUT", Required: true})
	for _, tecla := range comprarSinComentario {
		vars = m.Step(model.Node{}, model.Conversation{Vars: vars}, tecla).Vars
	}

	for _, entrada := range []string{"   ", "\u200b\u200b", "\n\t"} {
		res := m.Step(model.Node{}, model.Conversation{Vars: vars}, entrada)
		st := loadState(res.Vars)
		if st.Level != LevelBuyerData || st.BuyerIdx != 0 {
			t.Fatalf("con la entrada %q el checklist avanzó: level=%q idx=%d", entrada, st.Level, st.BuyerIdx)
		}
		if len(res.Effects) != 0 {
			t.Fatalf("con la entrada %q se emitieron efectos: %v", entrada, res.Effects)
		}
		if !strings.Contains(joined(res.Outputs), "Necesitamos ese dato") {
			t.Fatalf("con la entrada %q no se repreguntó:\n%s", entrada, joined(res.Outputs))
		}
	}
}

// TestBuyerDataDemasiadoLargoRechazaYRepregunta: se aplica el MISMO saneo que las
// indicaciones (SanitizeNote), y pasarse RECHAZA sin truncar. Truncar un RUT o una
// dirección guardaría un dato inservible y nadie se enteraría hasta el reparto.
func TestBuyerDataDemasiadoLargoRechazaYRepregunta(t *testing.T) {
	m := New()
	vars := varsConChecklist(store.BuyerField{Key: "direccion", Label: "Dirección de entrega", Required: true})
	for _, tecla := range comprarSinComentario {
		vars = m.Step(model.Node{}, model.Conversation{Vars: vars}, tecla).Vars
	}

	res := m.Step(model.Node{}, model.Conversation{Vars: vars}, strings.Repeat("a", MaxNoteRunes+1))
	if st := loadState(res.Vars); st.BuyerIdx != 0 {
		t.Fatalf("un valor demasiado largo se dio por capturado (idx=%d)", st.BuyerIdx)
	}
	if len(res.Effects) != 0 {
		t.Fatalf("un valor demasiado largo emitió efectos: %v", res.Effects)
	}
	if out := joined(res.Outputs); !strings.Contains(out, "más corta") {
		t.Fatalf("no se avisó del largo:\n%s", out)
	}
}

// TestBuyerDataElAcuseNoRepiteElValor: el carrito confirma que anotó el campo por
// su ETIQUETA, nunca repitiendo lo que el cliente escribió. Devolverlo por WhatsApp
// lo dejaría escrito una segunda vez en un sitio que wApp no controla.
func TestBuyerDataElAcuseNoRepiteElValor(t *testing.T) {
	m := New()
	vars := varsConChecklist(dosCamposRequeridos()...)
	for _, tecla := range comprarSinComentario {
		vars = m.Step(model.Node{}, model.Conversation{Vars: vars}, tecla).Vars
	}
	res := m.Step(model.Node{}, model.Conversation{Vars: vars}, rutDelCliente)
	out := joined(res.Outputs)
	if strings.Contains(out, rutDelCliente) {
		t.Fatalf("el acuse le devolvió al cliente su propio dato:\n%s", out)
	}
	if !strings.Contains(out, "Anotado: RUT") {
		t.Fatalf("no se acusó el campo capturado:\n%s", out)
	}
}

// TestBuyerDataChecklistRecortadoNoBloqueaElCierre: si el dueño quita campos a
// media conversación, el contador puede quedar por encima de la lista. Ese carrito
// tiene que poder cerrar — no quedarse esperando una pregunta que ya no existe.
func TestBuyerDataChecklistRecortadoNoBloqueaElCierre(t *testing.T) {
	m := New()
	vars := varsConChecklist(dosCamposRequeridos()...)
	for _, tecla := range comprarSinComentario {
		vars = m.Step(model.Node{}, model.Conversation{Vars: vars}, tecla).Vars
	}
	vars = m.Step(model.Node{}, model.Conversation{Vars: vars}, rutDelCliente).Vars

	// El dueño recorta su checklist a un solo campo: el que ya se capturó.
	vars[VarBuyerFields] = []store.BuyerField{{Key: "rut", Label: "RUT", Required: true}}
	res := m.Step(model.Node{}, model.Conversation{Vars: vars}, "loquesea")
	if st := loadState(res.Vars); st.Level != LevelClosed {
		t.Fatalf("con el checklist ya cumplido el carrito quedó en %q, esperaba cerrar", st.Level)
	}
}

// TestLoadBuyerFieldsToleraElRoundTripJSONB: entre mensaje y mensaje las Vars pasan
// por JSONB, así que lo que se sembró como []store.BuyerField vuelve como []any de
// map[string]any. Si el parseo no lo tolerara, el checklist funcionaría en el
// primer mensaje y desaparecería en el segundo.
func TestLoadBuyerFieldsToleraElRoundTripJSONB(t *testing.T) {
	original := map[string]any{VarBuyerFields: dosCamposRequeridos()}
	b, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var releído map[string]any
	if err := json.Unmarshal(b, &releído); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := loadBuyerFields(releído)
	if len(got) != 2 || got[0].Key != "rut" || got[0].Label != "RUT" || !got[0].Required {
		t.Fatalf("tras el round-trip JSONB el checklist quedó en %+v", got)
	}
}

// settingsFalsas es un ResumeStore que devuelve una config fija: lo que se prueba
// es la SIEMBRA, no la lectura de la BD (esa tiene su test de integración en el
// paquete store).
type settingsFalsas struct{ settings store.TenantSettings }

func (s settingsFalsas) GetTenantSettings(context.Context, string) (store.TenantSettings, error) {
	return s.settings, nil
}

// TestResumePolicy_SiembraElChecklistDelTenant cierra el hueco entre la config y el
// módulo puro: sin esta siembra, buyer_fields estaría bien leído en la BD y el
// carrito no se enteraría nunca. Se siembra en CADA mensaje —igual que el
// page_size—, así que cambiar el checklist alcanza a las conversaciones vivas.
func TestResumePolicy_SiembraElChecklistDelTenant(t *testing.T) {
	p := NewResumePolicy(settingsFalsas{settings: store.TenantSettings{
		PageSize:    7,
		BuyerFields: dosCamposRequeridos(),
	}})

	vars := map[string]any{}
	if err := p.Seed(context.Background(), "tenant-x", vars); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if got := loadBuyerFields(vars); len(got) != 2 || got[0].Key != "rut" {
		t.Fatalf("el checklist sembrado quedó en %+v", got)
	}
	// Y lo que se siembra es CONFIGURACIÓN: en el estado que se persiste no puede
	// haber respuestas, solo claves y etiquetas.
	if blob := varsJSON(t, vars); !strings.Contains(blob, "\"key\":\"rut\"") {
		t.Fatalf("el checklist no viajó con su forma canónica:\n%s", blob)
	}
}
