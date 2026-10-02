package cosatest

// Doble es el MISMO código que cosahelpertest/doble.go en un paquete con el nombre VIEJO de
// las suites: termina en «test» a secas. Hasta D-F1-10 quedaba exento; ya no: su test no lo
// nombra, así que muerde.
type Doble struct{ n int }

// Leer devuelve el valor y lo incrementa.
func (d *Doble) Leer() int {
	d.n++
	return d.n
}
