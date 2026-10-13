package cosa

// Doble es el MISMO código que cosahelpertest/doble.go, pero en un paquete cuyo nombre no
// termina en «helpertest»: no hay exención, así que su 0 % en el perfil muerde. Es el gemelo
// de control: si este no mordiera, que el otro no muerda no probaría nada.
type Doble struct{ n int }

// Leer devuelve el valor y lo incrementa.
func (d *Doble) Leer() int {
	d.n++
	return d.n
}
