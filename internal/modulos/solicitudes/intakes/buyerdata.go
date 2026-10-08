// Porta internal/intakes/buyerdata.go @ 64c181a

// buyerdata.go declara lo PURO de los DATOS DEL COMPRADOR de una solicitud
// (Plan 041 · T4.5, D-041.13 / ADR-0031 §5 / ADR-0017): el tipo que los lleva en
// memoria y el centinela de la escritura. Quien los guarda CIFRADOS EN REPOSO es
// PostgresBuyerData (buyerdata_postgres.go, D-F6-6).
//
// Es la ÚNICA parte del dominio de solicitudes que toca PII almacenada, y por eso
// vive aparte: el resto del paquete mueve datos de negocio en claro (ADR-0009) y
// mezclarlos daría a entender que la separación es opcional. No lo es: la fila de
// public.intake_buyer_data es la razón por la que intakes, intake_items e
// intake_revisions pueden ir en claro sin remordimiento.
//
// 🔴 HOMÓNIMO (T-1). La «DEK» de la que hablan este fichero, su adaptador y las
// columnas `*_dek` es la llave por-fila del SOBRE DE PII DE NEGOCIO: la genera y
// la custodia ESTA pieza, envuelta por la KEK de su keyring. NO es la DEK del
// ADR-0007 —la que descifra el almacén de whatsmeow—: esa la custodia el Edge,
// jamás cruza el contrato y este paquete no sabe nada de ella. Que aquí haya una
// DEK no dice nada sobre el zero-knowledge de la plataforma.

package intakes

import "errors"

// ErrBuyerFieldEmpty lo devuelve la escritura de un campo del comprador cuando la
// CLAVE del campo viene vacía: sin clave, el valor no se podría volver a leer ni
// distinguir de otro, y escribirlo igualmente solo dejaría un dato personal
// inalcanzable en la base. Se rechaza en vez de guardarse bajo "".
//
// Su texto es «intakes: campo del comprador sin clave». Llega SIN envolver.
var ErrBuyerFieldEmpty = errors.New("intakes: campo del comprador sin clave")

// BuyerData es el checklist del comprador de UNA solicitud, en claro: clave del
// campo → valor. Vive SOLO en memoria, en el borde de la app, entre el descifrado
// y el cifrado de la fila. Nunca se serializa fuera del blob cifrado.
//
// Lo que el dominio PUBLICA de él es un booleano —si la fila existe
// (BuyerDataPresent)— y nunca su contenido: ni el detalle de la solicitud ni el
// summary que se entrega a un LLM externo lo llevan (REQ-04 / INV-04). Su único
// consumidor en claro es el worker del puente CRM, justo antes del POST
// (D-042.9, INV-02).
type BuyerData map[string]string
