package legacysuite

// Reader es un puerto (solo interfaces) cuya suite Contrato vive en legacysuitetest, el
// nombre VIEJO. Desde D-F1-10 la suite se busca en legacysuitehelpertest: esta ya no vale y
// el puerto necesita test.
type Reader interface{ Read() int }
