package logs

import (
	"log/slog"

	"net/http"
	"runtime/debug"

	"github.com/FernasFragas/LLMGateway-Go/internal/logs"
)

var _ http.Handler = (*PanicLogger)(nil)

// PanicLogger gives a panic its log line — the value and the stack — and
// re-panics. Observing is this decorator's whole job: answering the caller
// belongs to the recover middleware outside it, the only layer that
// swallows. The stack survives the re-panic hops between here and there,
// gaining only the panic machinery's own frames.
type PanicLogger struct {
	next http.Handler
	log  *slog.Logger
}

// NewPanicLogger wraps next; a nil log means slog.Default().
func NewPanicLogger(next http.Handler, log *slog.Logger) *PanicLogger {
	return &PanicLogger{next: next, log: logs.OrDefault(log)}
}

func (l *PanicLogger) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if v := recover(); v != nil {
			l.log.ErrorContext(r.Context(), "handler panicked",
				"panic", v,
				"correlation_id", w.Header().Get("X-Correlation-ID"),
				"stack", string(debug.Stack()),
			)

			panic(v)
		}
	}()

	l.next.ServeHTTP(w, r)
}
