package router

import (
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/protobuf/proto"

	"gameserver/framework/errcode"
)

var (
	handlerDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gs_handler_duration_seconds",
		Help:    "Time spent in message handlers.",
		Buckets: []float64{.0001, .0005, .001, .005, .01, .05, .1, .5, 1},
	}, []string{"service", "msg_id"})
	handlerErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gs_handler_errors_total",
		Help: "Handler errors by code.",
	}, []string{"service", "msg_id", "code"})
)

// Recover converts a panic in a handler into an INTERNAL error so one bad
// message never crashes the process.
func Recover(logger *slog.Logger) Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context, req proto.Message) (resp proto.Message, err error) {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("handler panic", "msg_id", c.MsgID(), "uid", c.UID(),
						"panic", r, "stack", string(debug.Stack()))
					resp, err = nil, fmt.Errorf("panic: %v", r)
				}
			}()
			return next(c, req)
		}
	}
}

// Metrics records handler latency and error codes in Prometheus.
func Metrics(service string) Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context, req proto.Message) (proto.Message, error) {
			start := time.Now()
			resp, err := next(c, req)
			id := strconv.FormatUint(uint64(c.MsgID()), 10)
			handlerDuration.WithLabelValues(service, id).Observe(time.Since(start).Seconds())
			if err != nil {
				handlerErrors.WithLabelValues(service, id, strconv.Itoa(int(errcode.Code(err)))).Inc()
			}
			return resp, err
		}
	}
}

// AccessLog logs every request at debug level and slow requests at warn level.
func AccessLog(logger *slog.Logger, slow time.Duration) Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context, req proto.Message) (proto.Message, error) {
			start := time.Now()
			resp, err := next(c, req)
			cost := time.Since(start)
			attrs := []any{"msg_id", c.MsgID(), "uid", c.UID(), "cost", cost, "code", errcode.Code(err)}
			if slow > 0 && cost > slow {
				logger.Warn("slow request", attrs...)
			} else {
				logger.Debug("request", attrs...)
			}
			return resp, err
		}
	}
}
