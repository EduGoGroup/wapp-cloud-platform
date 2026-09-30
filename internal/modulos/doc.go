// Package modulos es la raíz del árbol de la reconstrucción modular de wapp-cloud-platform
// (documentations/reorganizacion-modular/05-metodo-contratos-y-tdd.md): internal/ se
// RECONSTRUYE en internal/modulos/<módulo>/, no se mueve. Cada fichero nuevo nace con su
// contrato sin lógica y un test que lo cubre, en rojo, antes que la lógica; el código viejo no
// se toca y sigue siendo la referencia.
//
// Los siete módulos de D-5 (plan/DECISIONES.md; correspondencia viejo→nuevo en 04 §4):
//
//   - acceso: identidad y gobierno multi-empresa (internal/iam + platformadmin + entitlements).
//   - edge: el túnel con cada Edge y lo que sube por él (gateway, diagnostics, inferstats,
//     receipts, ingest, filtercfg).
//   - conversacion: el Motor de Flujos y el turno acotado (flujos, turnoacotado).
//   - catalogo: el catálogo del tenant, su importación y su índice (catalogimport,
//     intake/catalogo).
//   - captacion: el pipeline que convierte una conversación en un caso (intake, intakeahead,
//     evidence, reanalisis, casebank, intentcfg).
//   - inferencia: la vía LLM, los prompts P2–P5, el LLM por tenant y la degradación (llmvia,
//     prompts, tenantllm, degradation).
//   - solicitudes: las solicitudes resultantes y lo que las entrega (intakes, integrations,
//     contracts, tenantvars).
//
// F0 no crea ningún módulo: este paquete solo aloja los tres candados del árbol
// (fronteras_test.go, un_fichero_un_test_test.go, exportados_cubiertos_test.go), que recorren
// el árbol real y exigen cero violaciones con la lógica de internal/candados.
package modulos
