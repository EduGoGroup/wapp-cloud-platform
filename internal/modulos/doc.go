// Package modulos es la raíz de los módulos de negocio de wapp-cloud-platform: cada capacidad
// vive en internal/modulos/<módulo>/, con su frontera. Las normas con las que se escribe aquí
// (un fichero un test, exportados cubiertos, tamaño, idioma) y cómo se añade un módulo están en
// documentations/constitucion.md §5.1 y §5.2; quién puede importar a quién, en la tabla Capas
// de fronteras_test.go.
//
// Los siete módulos (entre paréntesis, los paquetes que agrupa cada uno):
//
//   - acceso: identidad y gobierno multi-empresa (iam + platformadmin + entitlements).
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
// Este paquete no tiene lógica: solo aloja los candados del árbol (fronteras_test.go,
// un_fichero_un_test_test.go, exportados_cubiertos_test.go, file_size_test.go,
// no_pending_test.go), que recorren el árbol real con la lógica de internal/candados.
package modulos
