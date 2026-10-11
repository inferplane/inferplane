package governance

import "sync"

// Admission belongs to one successful PreCheck. Its identity is the handle, not
// a user-controlled request ID or an unbounded global deduplication map.
// Close must be deferred immediately after an allowed PreCheck, including on
// panic, cancellation and missing usage. Unproved usage retains the estimate.
// Legacy ingresses share an admission across pre-response retries; Responses
// performs PreCheck separately for each attempt.
type Admission struct {
	once     sync.Once
	rates    []func(*int64)
	estimate int64
	cost     int64
	missing  bool
}

func (a *Admission) finishRates(actual *int64) {
	if a == nil {
		return
	}
	for _, finish := range a.rates {
		finish(actual)
	}
}

func (a *Admission) Close() {
	if a != nil {
		a.once.Do(func() { a.finishRates(nil) })
	}
}
