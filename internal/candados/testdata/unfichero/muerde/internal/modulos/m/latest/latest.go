// Package latest es un paquete de PRODUCCIÓN cuyo nombre termina en «test» por casualidad
// (como contest o attest). Es el defecto que cierra D-F1-10: con la exención por el sufijo
// «test», este fichero sin test pasaba el candado.
package latest

// Version devuelve la última versión conocida. No tiene test al lado.
func Version(known []int) int {
	last := 0
	for _, v := range known {
		last = max(last, v)
	}
	return last
}
