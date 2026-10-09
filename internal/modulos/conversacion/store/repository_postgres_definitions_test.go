package store

import (
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// El resto de repository_postgres.go: las definiciones, las encuestas y el outbox de efectos,
// sobre el driver de mentira (ver repository_postgres_test.go).

const (
	// pgDefinition es el JSONB de una definición cuya versión EMBEBIDA (99) no es la de la columna.
	pgDefinition = `{"flow_id": "menu", "version": 99, "initial": "root", "nodes": {"root": {"type": "message", "text": "Hola"}}}`
	// pgDefinitionNotFound es el texto de ErrDefinitionNotFound para el tenant y el flujo de estos tests.
	pgDefinitionNotFound = "definición de flujo no encontrada: tenant=" + pgTenant + " flow=menu"
)

// TestPostgres_Definitions_VersionComesFromTheColumn: la versión que sale es la de la COLUMNA (o
// la pedida), no la embebida en el JSON.
func TestPostgres_Definitions_VersionComesFromTheColumn(t *testing.T) {
	h := newFakeRepository(t, pgOne(int64(7), []byte(pgDefinition)), pgOne([]byte(pgDefinition)))

	latest, err := h.repo.LatestDefinition(t.Context(), pgTenant, "menu")
	requireNoError(t, "LatestDefinition", err)
	requireEqual(t, "LatestDefinition: versión", latest.Version, 7)
	requireEqual(t, "LatestDefinition: flujo e inicial", latest.FlowID+"/"+latest.Initial, "menu/root")
	requireEqual(t, "LatestDefinition: nodos", len(latest.Nodes), 1)

	exact, err := h.repo.GetDefinition(t.Context(), pgTenant, "menu", 4)
	requireNoError(t, "GetDefinition", err)
	requireEqual(t, "GetDefinition: versión", exact.Version, 4)
	requireEqual(t, "GetDefinition: inicial", exact.Initial, "root")

	stmts := loose(t, h.fake, 2)
	requireArgs(t, "LatestDefinition", stmts[0], pgTenant, "menu")
	requireArgs(t, "GetDefinition", stmts[1], pgTenant, "menu", 4)
}

// TestPostgres_Definitions_NotFoundAndBrokenJSON: sin fila es ErrDefinitionNotFound con su texto
// (el de GetDefinition lleva además la versión); un JSON ilegible es un error con el suyo.
func TestPostgres_Definitions_NotFoundAndBrokenJSON(t *testing.T) {
	h := newFakeRepository(t, pgReply{}, pgReply{}, pgOne(int64(1), []byte(`{rota`)), pgOne([]byte(`{rota`)))

	_, err := h.repo.LatestDefinition(t.Context(), pgTenant, "menu")
	requireSentinel(t, "LatestDefinition sin fila", err, ErrDefinitionNotFound, pgDefinitionNotFound)
	_, err = h.repo.GetDefinition(t.Context(), pgTenant, "menu", 4)
	requireSentinel(t, "GetDefinition sin fila", err, ErrDefinitionNotFound, pgDefinitionNotFound+" version=4")

	_, err = h.repo.LatestDefinition(t.Context(), pgTenant, "menu")
	requireErrPrefix(t, "LatestDefinition con JSON ilegible", err, "store: deserializar definición: ")
	_, err = h.repo.GetDefinition(t.Context(), pgTenant, "menu", 1)
	requireErrPrefix(t, "GetDefinition con JSON ilegible", err, "store: deserializar definición: ")
	loose(t, h.fake, 4)
}

// TestPostgres_InsertDefinition_ReturnsTheAssignedVersion: UNA sentencia suelta que asigna la
// versión y la devuelve; al SQL viajan el tenant, el flow_id y la definición serializada.
func TestPostgres_InsertDefinition_ReturnsTheAssignedVersion(t *testing.T) {
	h := newFakeRepository(t, pgOne(int64(5)))
	flow := model.Flow{FlowID: "menu", Version: 99, Initial: "root",
		Nodes: map[string]model.Node{"root": {Type: model.NodeTypeMessage, Text: "Hola"}}}

	version, err := h.repo.InsertDefinition(t.Context(), pgTenant, flow)
	requireNoError(t, "InsertDefinition", err)
	requireEqual(t, "versión asignada", version, 5)
	requireArgs(t, "InsertDefinition", loose(t, h.fake, 1)[0], pgTenant, "menu", pgDefinitionJSON(t, flow))
}

// pgDefinitionJSON es la definición serializada como la guarda el adaptador.
func pgDefinitionJSON(t *testing.T, flow model.Flow) []byte {
	t.Helper()
	raw, err := model.MarshalDefinition(flow)
	requireNoError(t, "serializar el flujo", err)
	return raw
}

// pgSummaryRows son dos filas del listado de definiciones.
func pgSummaryRows() [][]driver.Value {
	return [][]driver.Value{{"alfa", int64(1), pgAt}, {"beta", int64(3), pgAt}}
}

// TestPostgres_ListDefinitions_MapsRowsAndClosesThem: una fila por flujo, en el orden de la base;
// sin filas, la lista vacía; y los fallos del recorrido (D-17): al iterar, y al cerrar si era lo
// único que fallaba.
func TestPostgres_ListDefinitions_MapsRowsAndClosesThem(t *testing.T) {
	h := newFakeRepository(t, pgReply{rows: pgSummaryRows()}, pgReply{},
		pgReply{rows: pgSummaryRows(), endErr: errPgBoom}, pgReply{rows: pgSummaryRows(), closeErr: errPgBoom})

	got, err := h.repo.ListDefinitions(t.Context(), pgTenant)
	requireNoError(t, "ListDefinitions", err)
	requireEqual(t, "filas", len(got), 2)
	requireEqual(t, "primera fila", got[0], FlowSummary{FlowID: "alfa", Version: 1, CreatedAt: pgAt})
	requireEqual(t, "segunda fila", got[1], FlowSummary{FlowID: "beta", Version: 3, CreatedAt: pgAt})

	got, err = h.repo.ListDefinitions(t.Context(), pgTenant)
	requireNoError(t, "ListDefinitions sin filas", err)
	requireEqual(t, "filas sin filas", len(got), 0)

	_, err = h.repo.ListDefinitions(t.Context(), pgTenant)
	requirePgWrapped(t, err, "store: iterar definiciones: ", errPgBoom)
	_, err = h.repo.ListDefinitions(t.Context(), pgTenant)
	requirePgWrapped(t, err, "store: cerrar filas: ", errPgBoom)
	loose(t, h.fake, 4)
}

// TestPostgres_InsertResults_OneStatementForTheBatch: una tanda vacía no va a la base; una con N
// filas es UN INSERT multi-fila suelto con siete argumentos por fila, y un EventID vacío viaja NULL.
func TestPostgres_InsertResults_OneStatementForTheBatch(t *testing.T) {
	h := newFakeRepository(t, pgReply{})
	for _, empty := range [][]SurveyResult{nil, {}} {
		requireNoError(t, "InsertResults de una tanda vacía", h.repo.InsertResults(t.Context(), empty))
	}
	requirePgUntouched(t, h.fake)

	requireNoError(t, "InsertResults", h.repo.InsertResults(t.Context(), []SurveyResult{
		{TenantID: pgTenant, ContactID: pgContact, FlowID: "menu", FlowVersion: 2, QuestionID: "q1", AnswerCode: "si", EventID: pgEventID},
		{TenantID: pgTenant, ContactID: pgContact, FlowID: "menu", FlowVersion: 3, QuestionID: "q2", AnswerCode: "no"},
	}))
	insert := loose(t, h.fake, 1)[0]
	requireEqual(t, "clase de la sentencia", insert.kind, pgExec)
	requireArgs(t, "InsertResults", insert,
		pgTenant, pgContact, "menu", 2, "q1", "si", pgEventID,
		pgTenant, pgContact, "menu", 3, "q2", "no", nil)
	requireEqual(t, "marcadores del INSERT (14, ni uno más)",
		strings.Contains(insert.query, "$14") && !strings.Contains(insert.query, "$15"), true)
}

// pgResultRows son dos filas de la lectura de respuestas: siete columnas, SIN event_id.
func pgResultRows() [][]driver.Value {
	return [][]driver.Value{
		{pgTenant, pgContact, "menu", int64(1), "q1", "a", pgAt},
		{pgTenant, pgContact, "menu", int64(2), "q2", "b", pgAt},
	}
}

// TestPostgres_ListResults_MapsRowsAndClosesThem: siete columnas por fila —esta consulta no lee
// event_id y EventID sale vacío—; sin filas, la lista vacía; y los fallos del recorrido con su
// texto propio, sin filas a medias.
func TestPostgres_ListResults_MapsRowsAndClosesThem(t *testing.T) {
	h := newFakeRepository(t, pgReply{rows: pgResultRows()}, pgReply{},
		pgReply{rows: pgResultRows(), endErr: errPgBoom}, pgReply{rows: pgResultRows(), closeErr: errPgBoom})

	got, err := h.repo.ListResults(t.Context(), pgTenant, pgContact, "menu")
	requireNoError(t, "ListResults", err)
	requireEqual(t, "filas", len(got), 2)
	requireEqual(t, "segunda fila", got[1], SurveyResult{TenantID: pgTenant, ContactID: pgContact, FlowID: "menu",
		FlowVersion: 2, QuestionID: "q2", AnswerCode: "b", CreatedAt: pgAt})

	got, err = h.repo.ListResults(t.Context(), pgTenant, pgContact, "menu")
	requireNoError(t, "ListResults sin filas", err)
	requireEqual(t, "filas sin filas", len(got), 0)

	got, err = h.repo.ListResults(t.Context(), pgTenant, pgContact, "menu")
	requirePgWrapped(t, err, "store: iterar resultados de encuesta: ", errPgBoom)
	requireEqual(t, "filas junto al error del recorrido", len(got), 0)
	got, err = h.repo.ListResults(t.Context(), pgTenant, pgContact, "menu")
	requirePgWrapped(t, err, "store: cerrar filas de resultados: ", errPgBoom)
	requireEqual(t, "filas junto al error del cierre", len(got), 0)

	requireArgs(t, "ListResults", loose(t, h.fake, 4)[0], pgTenant, pgContact, "menu")
}

// TestPostgres_InsertFlowEvent_SerializesThePayload: una sentencia suelta con siete argumentos; el
// payload viaja serializado, y uno nil viaja como `{}`.
func TestPostgres_InsertFlowEvent_SerializesThePayload(t *testing.T) {
	h := newFakeRepository(t, pgReply{}, pgReply{})
	ev := FlowEvent{TenantID: pgTenant, ContactID: pgContact, FlowID: "menu", FlowVersion: 2, Kind: "persist", Name: "survey_answer"}
	requireNoError(t, "InsertFlowEvent sin payload", h.repo.InsertFlowEvent(t.Context(), ev))
	ev.Payload = map[string]any{"answer": "si"}
	requireNoError(t, "InsertFlowEvent con payload", h.repo.InsertFlowEvent(t.Context(), ev))

	stmts := loose(t, h.fake, 2)
	requireArgs(t, "efecto sin payload", stmts[0], pgTenant, pgContact, "menu", 2, "persist", "survey_answer", `{}`)
	requireArgs(t, "efecto con payload", stmts[1], pgTenant, pgContact, "menu", 2, "persist", "survey_answer", `{"answer":"si"}`)
}
