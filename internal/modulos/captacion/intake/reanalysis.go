// Porta internal/intake/reanalisis.go @ 8d875ab

// reanalysis.go — EL SEGUNDO PRODUCTOR DE JOBS (Plan 044 · Ola 4 · T4.6, D-044.15,
// design §8.1; migración 0080).
//
// # QUÉ ES ESTO, EN UNA FRASE
//
// `POST /api/v1/intakes/{id}/reanalyze` abre un `intake_job` sobre un evento que YA
// tiene su solicitud, para que el pipeline vuelva a interpretar el MISMO material y
// deje una revisión más. Es la segunda puerta por la que nace un job; la primera —y
// hasta hoy la única— es el agregador, que abre la ventana con el mensaje del
// cliente (D-044.26).
//
// # 🔴 POR QUÉ EL JOB NACE EN `pending` Y NO EN `aggregating`
//
// Las DOS razones son estructurales, no de estilo:
//
//  1. **El job nace CON SU SOBRE, y un job con sobre es un job cerrado.** El material
//     que se re-interpreta ya está escrito —no hay ráfaga que esperar—, así que quien
//     pide el re-análisis compone y cifra el literal ANTES de abrir el job y se lo
//     entrega en la petición (ReanalysisRequest.SourceText). La fila se inserta ya
//     `pending` y con sus tres columnas del sobre, en UNA sentencia: no existe ningún
//     instante en que el worker pueda reclamarla sin literal.
//
//     Divergencia deliberada del viejo (D-F7-9, D-F8-13), T8.40. El viejo —y este
//     paquete hasta F8-06b— abría el job `pending` y SIN sobre, y el compositor lo
//     escribía después con `PutSourceText`, que elige «la ÚLTIMA TOCADA de esa tupla
//     en `pending`» con el sobre vacío. Eran dos sentencias: si el worker reclamaba
//     el job entre las dos, lo mataba por «no trae literal que analizar» y el dueño,
//     que había pedido un re-análisis, no veía pasar nada. Es la misma carrera que
//     CloseWithSourceText cerró para la ventana del agregador.
//  2. **`aggregating` es el único estado que entra en el índice único parcial**
//     `intake_jobs_ventana_viva_uidx`. Un re-análisis pedido mientras el cliente
//     sigue escribiendo chocaría contra la ventana viva de esa misma tupla, y el
//     fallo sería un 23505 en vez del `422 reanalysis_in_progress` que el contrato
//     manda. Naciendo `pending` no entra en el índice y la concurrencia la decide
//     LiveJobOfEvent (antes `JobNoTerminalDeEvento`), que responde lo que el
//     contrato pide.
//
// Y hay un tercer efecto, buscado: un job `pending` es reclamable YA. No hay ventana
// que esperar porque no hay nada que agregar — el material ya está escrito, y desde
// T8.40 su literal también.
//
// # LO QUE ESTE FICHERO NO HACE
//
// No lee el hilo, no descifra, no cifra y no decide si hay material: eso es del
// endpoint (`internal/reanalisis`), que además lo comprueba ANTES de llamar aquí
// para no dejar jobs huérfanos que nadie va a poder correr. El sobre le llega ya
// cifrado y lo guarda sin abrirlo. Aquí solo se abre la fila y se pregunta por la
// concurrencia.
//
// # DÓNDE ESTÁ EL SQL
//
// En el paquete viejo este fichero llevaba también las dos sentencias y los dos
// métodos de `*Postgres`. Aquí nacen con el adaptador, en postgres_reanalysis.go
// (D-F6-6 ampliada: los métodos de `*Postgres` viven en `postgres_<tema>.go`), y
// este fichero se queda con el tipo.

package intake

// ReanalysisRequest (antes `SolicitudReanalisis`) es lo que hace falta para abrir el job del re-análisis: la
// clave de ventana del evento, la solicitud a la que se cuelga y el contexto de
// D-044.15 que el `draft` necesitará al otro extremo.
//
// `MessageTS` NO está aquí a propósito: lo resuelve la propia sentencia leyendo el
// PRIMER job del evento (ver openReanalysisSQL). Pedirlo por parámetro habría
// obligado al endpoint a una consulta más para copiar un valor que ya está en esta
// tabla.
type ReanalysisRequest struct {
	// Key es la clave de ventana del evento que se re-analiza. Las cuatro columnas
	// son NOT NULL en la 0072, así que se valida antes de escribir.
	Key WindowKey
	// IntakeID es la solicitud EXISTENTE a la que este job le colgará una revisión
	// más. Se escribe ya, en el INSERT, y no al terminar: a diferencia del pipeline
	// normal —donde el borrador todavía no existe y el id lo devuelve `draft`—, aquí
	// la solicitud es el sujeto de la petición y se conoce desde el principio.
	IntakeID string
	// Context (antes `Contexto`) son las cuatro columnas de la 0080.
	Context Reanalysis
	// SourceText es EL SOBRE CON EL QUE NACE EL JOB: el literal del evento ya compuesto
	// y cifrado por quien llama. Se escribe en el MISMO INSERT que abre el job, así que
	// la fila es visible en `pending` ya con sus tres columnas (D-F7-9, D-F8-13; T8.40).
	// Nuevo: el viejo no lo tenía.
	//
	// VA COMPLETO O VACÍO ENTERO, la misma regla que JobStore.CloseWithSourceText:
	//
	//   - completo (SourceText.Complete): las tres piezas se guardan tal cual;
	//   - vacío entero (SourceText.Empty): el job nace con las tres columnas a NULL. Es
	//     una forma legítima de la 0072 —el hilo sin mensajes—, y el worker lo matará
	//     al reclamarlo por no traer literal, como hacía el viejo;
	//   - A MEDIAS (ni lo uno ni lo otro): OpenReanalysis lo rechaza con error ANTES de
	//     tocar la base y no abre nada. Una fila con medio sobre es indescifrable.
	//
	// 🔴 Valid NO mira este campo, a propósito: Valid dice si la PETICIÓN está entera
	// (ventana, solicitud, dueño) y su rechazo enumera esas tres cosas; el sobre es
	// otra regla con otro texto. OpenReanalysis mira primero la petición y después el
	// sobre: con las dos cosas mal, el error es el de la petición.
	SourceText SourceText
}

// Valid dice si la solicitud puede abrir un job: la clave de ventana entera, la
// solicitud a la que se cuelga y un contexto pedido por el DUEÑO. La tercera no es
// defensiva por gusto: esta puerta existe SOLO para el re-análisis, y un
// `requested_by` vacío produciría un job que el `draft` trataría como del pipeline
// normal pero que nadie habría agregado.
//
// El sobre (SourceText) NO entra aquí: su regla —completo o vacío entero— la aplica
// OpenReanalysis después de esta, con su propio texto (ver el campo).
func (s ReanalysisRequest) Valid() bool {
	return s.Key.Valid() && s.IntakeID != "" && s.Context.IsFromOwner()
}
