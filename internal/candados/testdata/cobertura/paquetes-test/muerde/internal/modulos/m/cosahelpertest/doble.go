package cosahelpertest

// Doble es un doble con lógica en un paquete …helpertest al que nadie ejecuta desde el test
// de su propio paquete: en el perfil sale al 0 %. Hasta D-F1-13 quedaba exento con todo su
// paquete; desde D-F1-13 solo se eximen los ficheros de suite (contrato.go y *_contrato.go),
// y este, que no lo es, se mide con el umbral normal y muerde.
type Doble struct{ n int }

// Leer devuelve el valor y lo incrementa.
func (d *Doble) Leer() int {
	d.n++
	return d.n
}
