package storehelpertest

import (
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Los casos de DefinitionReader y DefinitionStore, más ListDefinitions: flow_definitions.

// notFoundPrefix es el texto de ErrDefinitionNotFound hasta donde coinciden los dos adaptadores.
func notFoundPrefix(tenant, flowID string) string {
	return "definición de flujo no encontrada: tenant=" + tenant + " flow=" + flowID
}

// versioned devuelve la definición con la versión que el repositorio le asignó.
func versioned(f model.Flow, version int) model.Flow {
	f.Version = version
	return f
}

// caseInsertDefinitionNumbers: la versión la asigna el repositorio, de uno en uno desde 1 y POR
// (tenant, flujo): otro flujo del mismo tenant y el mismo flujo del otro tenant empiezan en 1. El
// Version del argumento se ignora.
func caseInsertDefinitionNumbers(t *testing.T, m Montaje) {
	// Los testigos primero: dos versiones de OTRO flujo del tenant y una del MISMO flujo en el otro.
	mustInsertDefinition(t, m, m.TenantA, sampleFlow(flowOrder, "pedido v1"))
	mustInsertDefinition(t, m, m.TenantA, sampleFlow(flowOrder, "pedido v2"))
	mustInsertDefinition(t, m, m.TenantB, sampleFlow(flowMenu, "menú del otro"))
	before := take(t, m)

	for want := 1; want <= 3; want++ {
		if got := mustInsertDefinition(t, m, m.TenantA, sampleFlow(flowMenu, "menú")); got != want {
			t.Errorf("InsertDefinition nº %d de (A, %s) asignó la versión %d, quería %d", want, flowMenu, got, want)
		}
	}
	if got := mustInsertDefinition(t, m, m.TenantB, sampleFlow(flowOrder, "pedido del otro")); got != 1 {
		t.Errorf("el primer InsertDefinition de (B, %s) asignó la versión %d, quería 1", flowOrder, got)
	}
	requireRestUntouched(t, "InsertDefinition", before, take(t, m),
		forgetDefinitions(m.TenantA, flowMenu), forgetDefinitions(m.TenantB, flowOrder))
}

// caseGetDefinitionExact: GetDefinition devuelve la definición ENTERA de la versión pedida, con
// esa versión puesta, y publicar una versión nueva no reescribe la anterior: una conversación en
// curso sigue con la definición con la que arrancó.
func caseGetDefinitionExact(t *testing.T, m Montaje) {
	v1, v2 := sampleFlow(flowMenu, "¿En qué te ayudo?"), sampleFlow(flowMenu, "Elige una opción")
	v2.Nodes["extra"] = model.Node{Type: model.NodeTypeMessage, Text: "Solo en la 2."}
	mustInsertDefinition(t, m, m.TenantA, v1)
	got1, err := m.Store.GetDefinition(ctx, m.TenantA, flowMenu, 1)
	if err != nil {
		t.Fatalf("GetDefinition(1) antes de publicar la 2: %v", err)
	}
	requireSameFlow(t, "versión 1 recién publicada", got1, versioned(v1, 1))

	mustInsertDefinition(t, m, m.TenantA, v2)
	for version, want := range map[int]model.Flow{1: versioned(v1, 1), 2: versioned(v2, 2)} {
		got, err := m.Store.GetDefinition(ctx, m.TenantA, flowMenu, version)
		if err != nil {
			t.Fatalf("GetDefinition(%d): %v", version, err)
		}
		requireSameFlow(t, "GetDefinition", got, want)
	}
}

// caseGetDefinitionNotFound: una versión que no existe, un flujo que no existe y el flujo de otro
// tenant son la MISMA respuesta: ErrDefinitionNotFound, con el tenant y el flujo en el texto.
func caseGetDefinitionNotFound(t *testing.T, m Montaje) {
	mustInsertDefinition(t, m, m.TenantA, sampleFlow(flowMenu, "menú"))
	cases := []struct {
		name, tenant, flowID string
		version              int
		wantText             string
	}{
		{"version that does not exist", m.TenantA, flowMenu, 2, notFoundPrefix(m.TenantA, flowMenu) + " version=2"},
		{"version zero", m.TenantA, flowMenu, 0, notFoundPrefix(m.TenantA, flowMenu) + " version=0"},
		{"flow that does not exist", m.TenantA, "no-existe", 1, ""},
		{"flow of the other tenant", m.TenantB, flowMenu, 1, ""},
	}
	for _, tc := range cases {
		got, err := m.Store.GetDefinition(ctx, tc.tenant, tc.flowID, tc.version)
		if !errors.Is(err, store.ErrDefinitionNotFound) {
			t.Errorf("%s: err = %v, quería ErrDefinitionNotFound", tc.name, err)
			continue
		}
		if !strings.HasPrefix(err.Error(), notFoundPrefix(tc.tenant, tc.flowID)) {
			t.Errorf("%s: texto = %q, quería que empezara por %q", tc.name, err, notFoundPrefix(tc.tenant, tc.flowID))
		}
		if tc.wantText != "" && err.Error() != tc.wantText {
			t.Errorf("%s: texto = %q, quería %q", tc.name, err, tc.wantText)
		}
		if got.FlowID != "" || len(got.Nodes) != 0 {
			t.Errorf("%s: devolvió una definición junto al error: %+v", tc.name, got)
		}
	}
}

// caseLatestDefinition: LatestDefinition devuelve la versión más alta, con ese número; sin
// ninguna —flujo desconocido o de otro tenant— es ErrDefinitionNotFound con su texto exacto.
func caseLatestDefinition(t *testing.T, m Montaje) {
	for _, tc := range []struct{ tenant, flowID string }{{m.TenantA, flowMenu}, {m.TenantB, flowMenu}} {
		got, err := m.Store.LatestDefinition(ctx, tc.tenant, tc.flowID)
		if !errors.Is(err, store.ErrDefinitionNotFound) || err.Error() != notFoundPrefix(tc.tenant, tc.flowID) {
			t.Errorf("LatestDefinition sin versiones: err = %v, quería ErrDefinitionNotFound con el texto %q",
				err, notFoundPrefix(tc.tenant, tc.flowID))
		}
		if got.FlowID != "" || len(got.Nodes) != 0 {
			t.Errorf("LatestDefinition sin versiones devolvió una definición: %+v", got)
		}
	}

	first, last := sampleFlow(flowMenu, "primera"), sampleFlow(flowMenu, "última")
	mustInsertDefinition(t, m, m.TenantA, first)
	got, err := m.Store.LatestDefinition(ctx, m.TenantA, flowMenu)
	if err != nil {
		t.Fatalf("LatestDefinition con una versión: %v", err)
	}
	requireSameFlow(t, "LatestDefinition con una versión", got, versioned(first, 1))

	mustInsertDefinition(t, m, m.TenantA, sampleFlow(flowMenu, "segunda"))
	mustInsertDefinition(t, m, m.TenantA, last)
	// Otro flujo con MÁS versiones no decide cuál es la última de este.
	for range 4 {
		mustInsertDefinition(t, m, m.TenantA, sampleFlow(flowOrder, "pedido"))
	}
	got, err = m.Store.LatestDefinition(ctx, m.TenantA, flowMenu)
	if err != nil {
		t.Fatalf("LatestDefinition con tres versiones: %v", err)
	}
	requireSameFlow(t, "LatestDefinition con tres versiones", got, versioned(last, 3))

	if _, err := m.Store.LatestDefinition(ctx, m.TenantB, flowMenu); !errors.Is(err, store.ErrDefinitionNotFound) {
		t.Errorf("LatestDefinition del flujo de otro tenant: err = %v, quería ErrDefinitionNotFound", err)
	}
}

// caseListDefinitions: una fila por flujo con su ÚLTIMA versión, ordenadas por flow_id y solo las
// del tenant. Un tenant sin flujos da la lista vacía sin error.
func caseListDefinitions(t *testing.T, m Montaje) {
	if got, err := m.Store.ListDefinitions(ctx, m.TenantA); err != nil || len(got) != 0 {
		t.Fatalf("ListDefinitions de un tenant sin flujos = (%+v, %v), quería vacía sin error", got, err)
	}
	// Se publican en orden inverso al alfabético: el orden de salida es por flow_id, no por alta.
	for _, flowID := range []string{"zeta", "beta", "beta", "alfa", "beta"} {
		mustInsertDefinition(t, m, m.TenantA, sampleFlow(flowID, "hola"))
	}
	mustInsertDefinition(t, m, m.TenantB, sampleFlow("ajeno", "hola"))

	requireSummaries(t, m, m.TenantA, map[string]int{"alfa": 1, "beta": 3, "zeta": 1}, "alfa", "beta", "zeta")
	requireSummaries(t, m, m.TenantB, map[string]int{"ajeno": 1}, "ajeno")
}

// requireSummaries afirma que ListDefinitions del tenant da esos flujos, en ese orden, cada uno con
// su última versión.
func requireSummaries(t *testing.T, m Montaje, tenant string, versions map[string]int, order ...string) {
	t.Helper()
	got, err := m.Store.ListDefinitions(ctx, tenant)
	if err != nil {
		t.Fatalf("ListDefinitions(%s): %v", tenant, err)
	}
	if len(got) != len(order) {
		t.Fatalf("ListDefinitions(%s) = %+v, quería %d flujos: %v", tenant, got, len(order), order)
	}
	for i, flowID := range order {
		if got[i].FlowID != flowID || got[i].Version != versions[flowID] {
			t.Errorf("ListDefinitions(%s)[%d] = (%q, v%d), quería (%q, v%d)", tenant, i,
				got[i].FlowID, got[i].Version, flowID, versions[flowID])
		}
	}
}
