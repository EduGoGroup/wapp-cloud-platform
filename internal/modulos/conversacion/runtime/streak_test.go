//go:build pendiente

package runtime

// streak.go no tiene exportados y en el rojo no declara nada (los no exportados romperían
// el lint `unused`), así que aquí no hay test que compile contra un símbolo existente. Los
// tests nacen en el verde (F8-04b), uno por promesa del comentario de streak.go. Todos con
// un `now` EXPLÍCITO (ninguno llama a time.Now ni a time.Sleep), un observador de onClose
// sincronizado y comparación de longitudes como multiconjunto (el recorrido de un mapa es
// aleatorio):
//
//   - ST-4 · TestStreak_DifferentConversationsDoNotMix: tabla con claves que difieren solo
//     en tenant, solo en sesión y solo en contacto.
//   - ST-5 · TestStreak_NonPositiveLimitsFallBackToDefaults: (0, 0) y (-1, -1) dan 30 min y
//     10.000; se prueba por conducta (una emisión a 29 min acumula; 10.000 claves no
//     desalojan y la 10.001 sí).
//   - ST-6 · TestStreak_WithoutOnCloseStillCounts.
//   - ST-7, ST-8 · TestStreak_IncAccumulatesWithinConversation: 1, 2, 3.
//   - ST-8 · TestStreak_LongLegitimateStreakIsNotFalsified: 30 emisiones cada 20 s dan
//     1…30 y ninguna observación; el Close final observa UNA racha de 30.
//   - ST-9 · TestStreak_IdleEpisodeIsClosedByNextInc: la racha vieja se reporta con su
//     longitud y el Inc devuelve 1.
//   - ST-10 · TestStreak_IdleLimitIsStrict: a exactamente idleTTL sigue viva (Inc acumula,
//     Max la cuenta); a idleTTL + 1 ns está vencida. Los dos métodos, el mismo caso.
//   - ST-12, ST-22, ST-27 · TestStreak_NilReceiverIsSafe: Inc = 0, Close no hace nada,
//     Max = 0.
//   - ST-13, ST-14 · TestStreak_FullMapSweepsExpiredFirst: con vencidas, se reportan todas
//     y no se desaloja ninguna viva.
//   - ST-15 · TestStreak_FullMapEvictsOldestAndReportsIt: sin vencidas, sale la de lastSeen
//     más antiguo, con su longitud, y la clave nueva entra con 1.
//   - ST-16 · TestStreak_ExistingKeyOnFullMapEvictsNothing.
//   - ST-17 · TestStreak_CloseReportsOnceAndResets.
//   - ST-18 · TestStreak_CloseWithoutStreakReportsNothing.
//   - ST-19 · TestStreak_CloseIsIdempotent.
//   - ST-20 · TestStreak_CloseReportsAnExpiredStreakToo.
//   - ST-21 · TestStreak_CloseIgnoresNow: el mismo resultado con el `now` cero y con uno
//     un año después.
//   - ST-23 · TestStreak_MaxReturnsLongestAlive: y baja al cerrar esa conversación; 0 sin
//     rachas.
//   - ST-24, ST-25 · TestStreak_MaxSweepsAndReportsExpired: la vencida más larga no es el
//     máximo, se reporta y desaparece.
//   - ST-26 · TestStreak_MaxNeverReportsTheSameStreakTwice.
//   - ST-28 · TestStreak_OnCloseRunsOutsideTheLock: un hook que reentra con Inc, con Close
//     y con Max (tabla) termina; se observa con un canal, sin time.Sleep.
//   - ST-29 · TestStreak_NeverReportsNonPositiveLength.
//   - ST-3 · TestStreak_ConcurrentUseCountsRight: G goroutines × N Inc sobre claves
//     propias y una compartida, con Close y Max intercalados; para -race.
//   - Mutantes (nivel complejo): `Before` por `!After` en Inc y en Max (ST-10); quitar el
//     `delete` de Max (ST-26); reportar dentro del candado (ST-28); quitar el reporte del
//     desalojo (ST-15); `e.n = 1` por `e.n++` en la vencida (ST-9); quitar la
//     normalización de idleTTL o de maxEntries (ST-5); `e.n > mayor` por `>=` no debe
//     morir (equivalente) y se anota como tal.
//
// De conducta DESDE EL MOTOR (nacen con send.go e incoming.go, ola siguiente, no aquí):
// que un entrante no reinicia la racha, que 30 turnos reales dan una racha de 30, que el
// cierre de la conversación observa la racha una sola vez y que sin hook el motor no se
// rompe. Y el candado AST streak_invariante_test.go (todo Delete cierra el episodio).
