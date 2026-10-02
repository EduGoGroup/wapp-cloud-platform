package cosahelpertest

// Doble es un doble con lógica en un paquete …helpertest. Desde D-F1-13 se MIDE como
// cualquier fichero: lo ejecuta el test de su propio paquete (doble_test.go, que el árbol de
// prueba no necesita: lo que cuenta es el perfil), así que su cobertura es real. Aquí está
// cubierto entero y pasa el umbral.
type Doble struct{ n int }

// Leer devuelve el valor y lo incrementa.
func (d *Doble) Leer() int {
	d.n++
	return d.n
}
