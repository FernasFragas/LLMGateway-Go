package metrics

import (
	"net/http"
	"sync/atomic"
)

// PanicCounter counts panicking handlers and re-panics. Observing is this
// decorator's whole job: answering the caller belongs to the recover
// middleware outside it, the only layer that swallows.
type PanicCounter struct {
	panics atomic.Int64
}

func NewPanicCounter() *PanicCounter {
	return &PanicCounter{}
}

// Wrap counts panics escaping next. One instance counts every route it
// wraps.
func (c *PanicCounter) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				c.panics.Add(1)

				panic(v)
			}
		}()

		next.ServeHTTP(w, r)
	})
}

// Panics reports how many panics this instance observed.
func (c *PanicCounter) Panics() int64 { return c.panics.Load() }
