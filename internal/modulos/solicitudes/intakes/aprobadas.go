// Porta internal/intakes/aprobadas.go @ 64c181a

// aprobadas.go — EL HISTORIAL APROBADO DEL TENANT (D-044.11): «cómo escribe este
// negocio», que es material de otra naturaleza que la negociación de un pedido y
// tiene otro consumidor (quien le sugiere al dueño el texto de la cotización).
//
// Aquí vive solo lo PURO: la cota. La lectura en sí (`ApprovedRenderedTexts`) es de
// cada store y nace con él.

package intakes

// MaxApprovedTexts acota cuántas cotizaciones aprobadas puede pedir un llamante de
// una vez: 50. No es una regla de negocio: es la cota que impide que un `limit`
// grande se convierta en un prompt de un megabyte y en un escaneo del historial
// entero del tenant. El consumidor de hoy pide cinco.
const MaxApprovedTexts = 50
