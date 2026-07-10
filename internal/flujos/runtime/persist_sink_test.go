package runtime_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

func persistEC() runtime.EffectContext {
	return runtime.EffectContext{TenantID: testTenant, ContactID: "c-opaco", FlowID: testFlow, FlowVersion: 1}
}

// TestPersistSink_SurveyAnswer_EscribeEventoYProyecta comprueba que un efecto
// survey_answer escribe UNA fila en flow_events y proyecta UNA en survey_results
// (misma fila que el flush del Plan 014).
func TestPersistSink_SurveyAnswer_EscribeEventoYProyecta(t *testing.T) {
	repo := store.NewMemoryRepository()
	sink := persistSinkWith(repo)
	eff := modules.Effect{Kind: "persist", Name: "survey_answer", Payload: map[string]any{"question_id": "q1", "answer_code": "a"}}

	if err := sink.Handle(context.Background(), persistEC(), eff); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if evs := repo.FlowEvents(); len(evs) != 1 {
		t.Fatalf("esperaba 1 flow_event, hay %d", len(evs))
	} else if evs[0].Name != "survey_answer" || evs[0].Kind != "persist" || evs[0].TenantID != testTenant {
		t.Fatalf("flow_event inesperado: %+v", evs[0])
	}
	res := repo.SurveyResults()
	if len(res) != 1 {
		t.Fatalf("esperaba 1 survey_result proyectado, hay %d", len(res))
	}
	if res[0].QuestionID != "q1" || res[0].AnswerCode != "a" || res[0].ContactID != "c-opaco" {
		t.Fatalf("survey_result proyectado inesperado: %+v", res[0])
	}
}

// TestPersistSink_OtroNombre_SoloEvento: un efecto con Name distinto de
// survey_answer solo escribe flow_events (no proyecta a survey_results).
func TestPersistSink_OtroNombre_SoloEvento(t *testing.T) {
	repo := store.NewMemoryRepository()
	sink := persistSinkWith(repo)
	eff := modules.Effect{Kind: "event", Name: "menu_selected", Payload: map[string]any{"option": "1"}}

	if err := sink.Handle(context.Background(), persistEC(), eff); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if evs := repo.FlowEvents(); len(evs) != 1 {
		t.Fatalf("esperaba 1 flow_event, hay %d", len(evs))
	}
	if res := repo.SurveyResults(); len(res) != 0 {
		t.Fatalf("un efecto no-survey_answer NO debe proyectar survey_results, hay %d", len(res))
	}
}

// TestPersistSink_PayloadSinClaves_NoPanica: un survey_answer sin las claves
// esperadas (o de otro tipo) escribe el evento pero OMITE la proyección sin
// panica (aserción de tipo defensiva).
func TestPersistSink_PayloadSinClaves_NoPanica(t *testing.T) {
	repo := store.NewMemoryRepository()
	sink := persistSinkWith(repo)
	// Payload sin question_id/answer_code y con un valor de tipo no-string.
	eff := modules.Effect{Kind: "persist", Name: "survey_answer", Payload: map[string]any{"question_id": 42}}

	if err := sink.Handle(context.Background(), persistEC(), eff); err != nil {
		t.Fatalf("Handle no debería fallar: %v", err)
	}
	if evs := repo.FlowEvents(); len(evs) != 1 {
		t.Fatalf("esperaba 1 flow_event, hay %d", len(evs))
	}
	if res := repo.SurveyResults(); len(res) != 0 {
		t.Fatalf("payload incompleto NO debe proyectar survey_results, hay %d", len(res))
	}
}

// TestPersistSink_Integracion_EscribeFlowEvents ejercita el PersistSink contra un
// Postgres real (gated por WAPP_TEST_DB_DSN): confirma que la fila aterriza en
// public.flow_events. SKIP limpio sin DSN.
func TestPersistSink_Integracion_EscribeFlowEvents(t *testing.T) {
	db := openTestDB(t) // hace t.Skip si no hay DSN/BD
	repo := store.NewPostgresRepository(db)
	sink := persistSinkWith(repo)

	ctx := context.Background()
	tenant := "tenant-persist-sink"
	contact := "c-opaco-integ"
	name := "survey_answer"
	eff := modules.Effect{Kind: "persist", Name: name, Payload: map[string]any{"question_id": "q1", "answer_code": "a"}}
	ec := runtime.EffectContext{TenantID: tenant, ContactID: contact, FlowID: "flujo-integ", FlowVersion: 1}

	if err := sink.Handle(ctx, ec, eff); err != nil {
		t.Fatalf("Handle (postgres): %v", err)
	}

	var n int
	err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.flow_events
		WHERE tenant_id = $1 AND contact_id = $2 AND name = $3
	`, tenant, contact, name).Scan(&n)
	if err != nil {
		t.Fatalf("SELECT flow_events: %v", err)
	}
	if n < 1 {
		t.Fatalf("esperaba >=1 fila en flow_events, hay %d", n)
	}
}

// TestPersistSink_Integracion_EncuestaDosRespuestas ejercita la costura T3 contra
// un Postgres real (gated por WAPP_TEST_DB_DSN): dos efectos survey_answer (como
// los que declara el módulo survey en una encuesta de 2 preguntas) dejan 2 filas
// en flow_events y 2 en survey_results, y el GROUP BY answer_code agrega bien.
// SKIP limpio sin DSN.
func TestPersistSink_Integracion_EncuestaDosRespuestas(t *testing.T) {
	db := openTestDB(t) // hace t.Skip si no hay DSN/BD
	repo := store.NewPostgresRepository(db)
	sink := persistSinkWith(repo)

	ctx := context.Background()
	tenant := "tenant-t3-encuesta"
	contact := "c-opaco-t3"
	// flow_id único aísla este test de corridas previas en la BD compartida.
	flowID := fmt.Sprintf("encuesta-t3-%d", time.Now().UnixNano())
	ec := runtime.EffectContext{TenantID: tenant, ContactID: contact, FlowID: flowID, FlowVersion: 1}

	effs := []modules.Effect{
		{Kind: "persist", Name: "survey_answer", Payload: map[string]any{"question_id": "q1", "answer_code": "si"}},
		{Kind: "persist", Name: "survey_answer", Payload: map[string]any{"question_id": "q2", "answer_code": "no"}},
	}
	for _, eff := range effs {
		if err := sink.Handle(ctx, ec, eff); err != nil {
			t.Fatalf("Handle (postgres): %v", err)
		}
	}

	var events, results int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.flow_events WHERE flow_id = $1`, flowID).Scan(&events); err != nil {
		t.Fatalf("SELECT flow_events: %v", err)
	}
	if events != 2 {
		t.Fatalf("esperaba 2 flow_events, hay %d", events)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.survey_results WHERE flow_id = $1`, flowID).Scan(&results); err != nil {
		t.Fatalf("SELECT survey_results: %v", err)
	}
	if results != 2 {
		t.Fatalf("esperaba 2 survey_results, hay %d", results)
	}

	// GROUP BY answer_code: cada opción aparece una vez.
	rows, err := db.QueryContext(ctx,
		`SELECT answer_code, count(*) FROM public.survey_results WHERE flow_id = $1 GROUP BY answer_code`, flowID)
	if err != nil {
		t.Fatalf("GROUP BY answer_code: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Logf("cerrando rows: %v", cerr)
		}
	}()
	agg := map[string]int{}
	for rows.Next() {
		var code string
		var c int
		if err := rows.Scan(&code, &c); err != nil {
			t.Fatalf("scan agregación: %v", err)
		}
		agg[code] = c
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterar agregación: %v", err)
	}
	if agg["si"] != 1 || agg["no"] != 1 {
		t.Fatalf("agregación por answer_code inesperada: %+v", agg)
	}
}
