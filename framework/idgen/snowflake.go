// Package idgen generates globally unique, roughly time-ordered 63-bit ids
// using the Snowflake layout:
//
//	| 1 bit unused | 41 bits milliseconds since Epoch | 10 bits worker | 12 bits sequence |
//
// 41 bits of milliseconds last ~69 years; each worker can produce 4096 ids per ms.
// Every process that generates ids must use a distinct worker id.
package idgen

import (
	"fmt"
	"sync"
	"time"
)

const (
	workerBits   = 10
	sequenceBits = 12

	MaxWorker   = 1<<workerBits - 1
	maxSequence = 1<<sequenceBits - 1

	timeShift   = workerBits + sequenceBits
	workerShift = sequenceBits
)

// Epoch is the custom epoch (2024-01-01 UTC). Never change it once ids are persisted.
var Epoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// Snowflake is a thread-safe id generator.
type Snowflake struct {
	mu       sync.Mutex
	worker   int64
	lastMs   int64
	sequence int64
	now      func() time.Time
}

// NewSnowflake creates a generator for worker in [0, MaxWorker].
func NewSnowflake(worker int64) (*Snowflake, error) {
	if worker < 0 || worker > MaxWorker {
		return nil, fmt.Errorf("idgen: worker %d out of range [0,%d]", worker, MaxWorker)
	}
	return &Snowflake{worker: worker, now: time.Now}, nil
}

// Next returns a new id.
func (s *Snowflake) Next() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	ms := s.now().Sub(Epoch).Milliseconds()
	if ms < s.lastMs {
		// Clock moved backwards (NTP adjustment): keep using the last timestamp
		// so ids stay unique and monotonic.
		ms = s.lastMs
	}
	if ms == s.lastMs {
		s.sequence = (s.sequence + 1) & maxSequence
		if s.sequence == 0 {
			// Sequence exhausted for this millisecond: borrow the next millisecond.
			// The logical clock catches up with the wall clock as soon as load drops.
			ms = s.lastMs + 1
		}
	} else {
		s.sequence = 0
	}
	s.lastMs = ms
	return ms<<timeShift | s.worker<<workerShift | s.sequence
}

// Parse splits an id into its components.
func Parse(id int64) (t time.Time, worker, sequence int64) {
	ms := id >> timeShift
	return Epoch.Add(time.Duration(ms) * time.Millisecond), (id >> workerShift) & MaxWorker, id & maxSequence
}
