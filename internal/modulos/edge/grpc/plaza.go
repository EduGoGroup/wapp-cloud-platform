// Porta internal/gateway/grpc/plaza.go @ ec236b3.

package grpc

import "github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"

// plaza.go — LA DIRECCIÓN DE LA PLAZA (Plan 044 · Ola 2 · T2.7, ADR-0046 Mecanismo 1).
//
// El recurso escaso de la vía local es UN OLLAMA POR MÁQUINA, y la máquina es el
// Edge. El aforo que lo reparte vive en el Cloud (ADR-0038 Enmienda 2) y necesita
// saber una sola cosa que solo este paquete puede responder: **qué Edge atendería
// esta inferencia**. Eso es exactamente lo que ya decide `inferenceSession` para
// elegir el stream; aquí no se decide nada nuevo, se PUBLICA lo decidido.
//
// 🔴 POR QUÉ (tenant, Edge) Y NO (tenant, sesión). Un Edge multiplexa TODAS sus
// sesiones sobre un solo stream (ADR-0008): dos sesiones del mismo Edge son el mismo
// proceso y el mismo Ollama. Un aforo indexado por sesión le daría DOS plazas a una
// sola máquina en cuanto el cliente emparejara un segundo teléfono, y el entero
// dejaría de proteger lo que dice proteger — sin un solo error.
//
// 🔴 Y POR QUÉ NO UN ENTERO GLOBAL DEL PROCESO. Porque serializaría los presupuestos
// de TODOS los clientes detrás del más lento: el tenant A con un pedido de 10 ítems
// (5–6 min) dejaría al tenant B, con su propio Edge ocioso, esperando sin motivo
// (D7-b, D-044.42).

// PlazaDe dice QUÉ EDGE del tenant atendería una inferencia originada en esa sesión,
// o `false` si el tenant no tiene ninguna sesión viva en esta réplica.
//
// Es el MISMO recorrido que hace `Infer`, y lo es a propósito (R-G15, INV-057.3): se
// resuelve el stream con `inferenceSession` —candidato vivo, o la primera alfabética
// de las vivas del tenant que no hayan dicho DOWN, prefiriendo las que dijeron READY—
// y luego se traduce ese stream a su Edge. Reimplementar aquí el criterio
// sería fabricar la avería clásica de los caminos gemelos: el aforo protegería un
// Edge y la petición saldría por otro.
//
// ⚠️ ES UNA FOTO, NO UNA RESERVA. Entre esta llamada y la inferencia real el Edge
// puede irse y el enrutado caer a otro. Consecuencia acotada y conocida: el aforo
// guardaría la plaza de un Edge que ya no sirve, y el que sí sirve quedaría un rato
// sin proteger. No se arregla con un candado más largo —habría que sostenerlo
// durante los minutos que dura una cadena de lote, con el registro entero detrás—
// sino con el hecho de que el siguiente job vuelve a preguntar.
//
// El nombre NO se traduce (E-11): el aforo (llmvia) lo busca por aserción de tipo, y un
// renombrado lo apagaría en silencio (T-1). El Edge se busca SOLO entre los del tenant:
// si el stream resuelto no es de un Edge suyo en el seguimiento, no hay plaza.
func (s *Server) PlazaDe(_, _ string) (string, bool) {
	panic(pendiente.Implementar("grpc.Server.PlazaDe"))
}
