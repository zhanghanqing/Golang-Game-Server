// Package errcode defines errors that carry a numeric code for the client.
//
// Handlers return *errcode.Error to make the router answer with an ErrorResp
// containing that code. Any other error is reported as INTERNAL and its text
// is only logged, never sent to the client.
package errcode

import (
	"errors"
	"fmt"

	"gameserver/framework/pb/syspb"
)

// Error is an error with a client-visible code.
type Error struct {
	Code int32
	Msg  string
}

func (e *Error) Error() string { return fmt.Sprintf("code=%d: %s", e.Code, e.Msg) }

// New creates an Error.
func New(code int32, msg string) *Error { return &Error{Code: code, Msg: msg} }

// Newf creates an Error with a formatted message.
func Newf(code int32, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// Code extracts the code from err: OK for nil, the embedded code for *Error, INTERNAL otherwise.
func Code(err error) int32 {
	if err == nil {
		return int32(syspb.ErrorCode_OK)
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return int32(syspb.ErrorCode_INTERNAL)
}

// Common framework errors.
var (
	ErrInternal           = New(int32(syspb.ErrorCode_INTERNAL), "internal error")
	ErrUnknownMessage     = New(int32(syspb.ErrorCode_UNKNOWN_MESSAGE), "unknown message")
	ErrBadRequest         = New(int32(syspb.ErrorCode_BAD_REQUEST), "bad request")
	ErrNotAuthenticated   = New(int32(syspb.ErrorCode_NOT_AUTHENTICATED), "not authenticated")
	ErrRateLimited        = New(int32(syspb.ErrorCode_RATE_LIMITED), "too many requests")
	ErrServiceUnavailable = New(int32(syspb.ErrorCode_SERVICE_UNAVAILABLE), "service unavailable")
	ErrTimeout            = New(int32(syspb.ErrorCode_TIMEOUT), "timeout")
)
