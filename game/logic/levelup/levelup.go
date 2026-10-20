// Package levelup contains the experience curve.
package levelup

// MaxLevel caps character level.
const MaxLevel = 100

// ExpToNext returns the experience required to go from level to level+1.
func ExpToNext(level int32) int64 { return int64(level) * 100 }

// AddExp applies gained experience and returns the new level and remaining exp.
// At MaxLevel experience is capped at 0 so it does not grow forever.
func AddExp(level int32, exp, gained int64) (int32, int64) {
	if level < 1 {
		level = 1
	}
	exp += gained
	for level < MaxLevel && exp >= ExpToNext(level) {
		exp -= ExpToNext(level)
		level++
	}
	if level >= MaxLevel {
		return MaxLevel, 0
	}
	return level, exp
}
