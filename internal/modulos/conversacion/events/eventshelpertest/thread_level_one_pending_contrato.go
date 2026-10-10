//go:build !pendiente

package eventshelpertest

// levelOneThreadCases no añade nada mientras events/summary.go esté en rojo: los dos casos que
// renderizan un payload de nivel 1 viven en thread_level_one_contrato.go, tras la etiqueta
// `pendiente`. El verde de summary.go quita la etiqueta de aquel y BORRA este fichero.
func levelOneThreadCases() []contractCase { return nil }
