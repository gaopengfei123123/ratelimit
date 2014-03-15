// Copyright 2014 Canonical Ltd.
// Licensed under the LGPLv3 with static-linking exception.
// See LICENCE file for details.

// The ratelimit package provides an efficient token bucket implementation.
// See http://en.wikipedia.org/wiki/Token_bucket.
package ratelimit

import (
	"strconv"
	"sync"
	"time"
)

// Bucket represents a token bucket that fills at a predetermined rate.
// Methods on Bucket may be called concurrently.
type Bucket struct {
	startTime    time.Time
	capacity     int64
	quantum      int64
	fillInterval time.Duration

	// The mutex guards the fields following it.
	mu sync.Mutex

	// avail holds the number of available tokens
	// in the bucket, as of availTick ticks from startTime.
	// It will be negative when there are consumers
	// waiting for tokens.
	avail     int64
	availTick int64
}

// NewBucket returns a new token bucket that fills at the
// rate of one token every fillInterval, up to the given
// maximum capacity. Both arguments must be
// positive. The bucket is initially full.
func NewBucket(fillInterval time.Duration, capacity int64) *Bucket {
	return newBucketWithQuantum(fillInterval, capacity, 1)
}

// rateMargin specifes the allowed variance of actual
// rate from specified rate. 1% seems reasonable.
const rateMargin = 0.01

// NewBucketWithRate returns a token bucket that fills the bucket
// at the rate of rate tokens per second up to the given
// maximum capacity. Because of limited clock resolution,
// at high rates, the actual rate may be up to 1% different from the
// specified rate.
func NewBucketWithRate(rate float64, capacity int64) *Bucket {
	for quantum := int64(1); quantum < 1<<50; quantum = nextQuantum(quantum) {
		fillInterval := time.Duration(1e9 * float64(quantum) / rate)
		if fillInterval <= 0 {
			continue
		}
		tb := newBucketWithQuantum(fillInterval, capacity, quantum)
		if diff := abs(tb.Rate() - rate); diff/rate <= rateMargin {
			return tb
		}
	}
	panic("cannot find suitable quantum for " + strconv.FormatFloat(rate, 'g', -1, 64))
}

// nextQuantum returns the next quantum to try after q.
// We grow the quantum exponentially, but slowly, so we
// get a good fit in the lower numbers.
func nextQuantum(q int64) int64 {
	q1 := q * 11 / 10
	if q1 == q {
		q1++
	}
	return q1
}

// newBucketWithQuantum is similar to NewBucket, but allows
// the specification of the quantum size - quantum tokens
// are added every fillInterval. This is so that we can get accurate
// rates even when we want to add more than one token per ns.
func newBucketWithQuantum(fillInterval time.Duration, capacity, quantum int64) *Bucket {
	if fillInterval <= 0 {
		panic("token bucket fill interval is not > 0")
	}
	if capacity <= 0 {
		panic("token bucket capacity is not > 0")
	}
	if quantum <= 0 {
		panic("token bucket quantum is not > 0")
	}
	return &Bucket{
		startTime:    time.Now(),
		capacity:     capacity,
		quantum:      quantum,
		avail:        capacity,
		fillInterval: fillInterval,
	}
}

// Wait takes count tokens from the bucket, waiting until they are
// available.
func (tb *Bucket) Wait(count int64) {
	if d := tb.Take(count); d > 0 {
		time.Sleep(d)
	}
}

// Take takes count tokens from the bucket without blocking. It returns
// the time that the caller should wait until the tokens are actually
// available.
//
// Note that if the request is irrevocable - there is no way to return
// tokens to the bucket once this method commits us to taking them.
func (tb *Bucket) Take(count int64) time.Duration {
	return tb.take(time.Now(), count)
}

// TakeAvailable takes up to count immediately available tokens from the
// bucket. It returns the number of tokens removed, or zero if there are
// no available tokens. It does not block.
func (tb *Bucket) TakeAvailable(count int64) int64 {
	return tb.takeAvailable(time.Now(), count)
}

// takeAvailable is the internal version of TakeAvailable - it takes the
// current time as an argument to enable easy testing.
func (tb *Bucket) takeAvailable(now time.Time, count int64) int64 {
	if count <= 0 {
		return 0
	}
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.adjust(now)
	if tb.avail <= 0 {
		return 0
	}
	if count > tb.avail {
		count = tb.avail
	}
	tb.avail -= count
	return count
}

// Rate returns the fill rate of the bucket, in tokens per second.
func (tb *Bucket) Rate() float64 {
	return 1e9 * float64(tb.quantum) / float64(tb.fillInterval)
}

// take is the internal version of Take - it takes the current time as
// an argument to enable easy testing.
func (tb *Bucket) take(now time.Time, count int64) time.Duration {
	if count <= 0 {
		return 0
	}
	tb.mu.Lock()
	defer tb.mu.Unlock()

	currentTick := tb.adjust(now)
	tb.avail -= count
	if tb.avail >= 0 {
		return 0
	}
	// Round up the missing tokens to the nearest multiple
	// of quantum - the tokens won't be available until
	// that tick.
	endTick := currentTick + (-tb.avail+tb.quantum-1)/tb.quantum
	endTime := tb.startTime.Add(time.Duration(endTick) * tb.fillInterval)
	return endTime.Sub(now)
}

// adjust adjusts the current bucket capacity based on the current time.
// It returns the current tick.
func (tb *Bucket) adjust(now time.Time) (currentTick int64) {
	currentTick = int64(now.Sub(tb.startTime) / tb.fillInterval)

	if tb.avail >= tb.capacity {
		return
	}
	tb.avail += (currentTick - tb.availTick) * tb.quantum
	if tb.avail > tb.capacity {
		tb.avail = tb.capacity
	}
	tb.availTick = currentTick
	return
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
