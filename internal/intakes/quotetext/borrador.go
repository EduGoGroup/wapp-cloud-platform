// Package quotetext redacta el MENSAJE con el que el dueño le pasa la cotización a
// su cliente (Plan 044 · Ola 5 · T5.1, D-044.11).
//
// # QUÉ ES ESTE PAQUETE Y QUÉ NO ES
//
// Es una SUGERENCIA. Devuelve un texto y no escribe nada: no persiste revisiones, no
// transiciona la solicitud y NO le manda nada al cliente. Quien aprueba —y por tanto
// quien decide qué texto sale por WhatsApp— sigue siendo el dueño, por el camino de
// siempre (`POST …/approve` con su `rendered_text` obligatorio). Esa separación no es
// una cautela de estilo: INV-1 dice que ningún camino automático aprueba, y la forma
// de que siga siendo verdad es que el generador no tenga por dónde hacerlo. Mira los
// puertos de Servicio: no hay ninguno que escriba y no hay ninguno que envíe.
//
// # 🔴 INV-2 — EL LLM NUNCA CALCULA PRECIOS
//
// El modelo REDACTA; los importes salen de las líneas persistidas y se COMPRUEBAN en
// Go antes de devolver nada (precios.go). Si el texto que vuelve no cuadra con las
// líneas —o si no se puede afirmar que cuadre—, se descarta entero y se devuelve el
// render determinista (render.go). Un texto sobrio es peor que uno bonito; un texto
// con un precio inventado que se le manda a un cliente es peor que los dos.
//
// # POR QUÉ UN PAQUETE PROPIO Y NO UN MÉTODO MÁS EN `intakes`
//
// Por lo mismo que `reanalisis`: esta operación necesita el selector de vía LLM, el
// historial de revisiones aprobadas del TENANT (no de la solicitud) y el contenido
// dinámico del tenant. `intakes.Service` no depende hoy de ninguna de las tres, y
// meterlas ahí obligaría a montarlas en todos sus tests. Aquí entran por puertos
// estrechos declarados del lado del consumidor, que es el idioma del repo.
package quotetext

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// BorradorVersion es la versión del JSON que viaja al prompt. Va DENTRO del blob y no
// en un parámetro aparte por lo mismo que RevisionPayloadVersion: quien lo lea en un
// log, en un eval o en un fixture tiene que poder saber qué contrato mira sin salir de
// lo que tiene delante.
const BorradorVersion = 1

// Linea es UNA línea del presupuesto tal como entra al generador.
//
// Es un tipo PROPIO y no `intakes.Item` a propósito, y la razón es el verificador: lo
// que se le manda al modelo y lo que después se comprueba contra su respuesta tienen
// que ser EXACTAMENTE el mismo objeto. Con `intakes.Item` viajando al prompt, el día
// que ese struct gane un campo el prompt cambiaría solo —y el prefijo cacheado del
// ADR-0046 con él— sin que nadie lo hubiera decidido.
//
// 🔴 EL SKU NO ESTÁ, Y ES DELIBERADO: es un código interno del catálogo, el cliente no
// lo necesita, y ponérselo delante al modelo es invitarlo a imprimirlo. Su ausencia
// tiene además un efecto en el verificador — ver `numerosDelBorrador`.
type Linea struct {
	// Label es lo que el cliente lee: producto, tamaño y specs ya vienen dentro
	// («Torta chocolate húmedo + crema choc. — 10-12 porciones»).
	Label string `json:"label"`
	// Customization es la personalización NO FACTURABLE (INV-13: jamás entra en
	// ningún importe). Vacía se omite.
	Customization string `json:"customization,omitempty"`
	// Qty es la cantidad de la línea.
	Qty int `json:"qty"`
	// UnitPrice es el precio unitario. 🔴 float64 y no centavos porque así está en
	// `intakes.Item` y en la columna; convertir aquí inventaría una precisión que el
	// dato no tiene. Todas las comparaciones de este paquete pasan por
	// `mismoImporte`.
	UnitPrice float64 `json:"unit_price"`
	// LineTotal es Qty × UnitPrice. Viaja CALCULADO —y no se le pide al modelo— por
	// INV-2: el modelo no multiplica.
	LineTotal float64 `json:"line_total"`
	// PorConfirmar marca la línea SIN importe todavía (`unit_price` 0), típicamente
	// el envío antes de que se sepa la zona (D-041.11). No es un precio de cero: es
	// la ausencia de precio, y el texto tiene que decirlo con palabras en vez de
	// escribir «$0».
	PorConfirmar bool `json:"pending_price,omitempty"`
}

// Borrador es el presupuesto completo que se le da al modelo y contra el que se
// verifica su respuesta.
type Borrador struct {
	Version int     `json:"version"`
	Lineas  []Linea `json:"lines"`
	// Total es la SUMA de los LineTotal. No se copia de `intakes.Intake.Total`
	// aunque el invariante del store diga que coinciden: el texto tiene que cuadrar
	// consigo mismo, y si algún día divergieran, el número que el cliente suma a
	// mano es éste. La discrepancia se avisa por log en el servicio.
	Total float64 `json:"total"`
}

// BorradorDe proyecta las líneas persistidas de una solicitud al borrador que ve el
// modelo. Es PURA: sin BD, sin reloj y sin mutar la entrada.
//
// Una línea con `UnitPrice` 0 sale marcada `PorConfirmar` y aporta 0 al total. Ese es
// el caso REAL de la línea de envío de un tenant sin zonas configuradas
// (`EnsureShippingLine` con `ShippingPolicy` que la materializa sin precio), y
// tratarlo como «vale cero» le prometería al cliente un envío gratis.
func BorradorDe(items []intakes.Item) Borrador {
	b := Borrador{Version: BorradorVersion, Lineas: make([]Linea, 0, len(items))}
	for _, it := range items {
		qty := it.Qty
		if qty < 1 {
			// Una línea sin cantidad es UNA. Es la misma lectura que hace el render
			// del carrito, y no es una corrección del dato: `Qty` es un int de Go y
			// no distingue «no vino» de «cero», así que el cero no puede significar
			// «cero unidades» — una línea de cero unidades no existiría.
			qty = 1
		}
		l := Linea{
			Label:         it.Label,
			Customization: it.Customization,
			Qty:           qty,
			UnitPrice:     it.UnitPrice,
			LineTotal:     float64(qty) * it.UnitPrice,
			PorConfirmar:  it.UnitPrice == 0,
		}
		b.Lineas = append(b.Lineas, l)
		b.Total += l.LineTotal
	}
	return b
}

// TieneLineasDeCliente dice si el borrador contiene alguna línea que no sea de sistema.
// Se pregunta sobre los ítems y no sobre el borrador porque el SKU —que es lo que
// marca a las de sistema— no viaja en el borrador a propósito.
func TieneLineasDeCliente(items []intakes.Item) bool {
	for _, it := range items {
		if !strings.HasPrefix(it.SKU, intakes.ReservedSKUPrefix) {
			return true
		}
	}
	return false
}

// JSON serializa el borrador para el prompt.
func (b Borrador) JSON() (json.RawMessage, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("quotetext: serializar el borrador: %w", err)
	}
	return raw, nil
}
