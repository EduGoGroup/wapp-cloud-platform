package cosa

// Doble es el MISMO código que cosahelpertest/doble.go, pero en un paquete cuyo nombre no
// termina en «helpertest»: su test no lo nombra, así que muerde. Es el gemelo de control.
type Doble struct{ n int }

// Leer devuelve el valor y lo incrementa.
func (d *Doble) Leer() int {
	d.n++
	return d.n
}
