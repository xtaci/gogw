package aiohttp

import (
	"bytes"
	"time"
)

type timeoutError struct{}

func (e *timeoutError) Error() string {
	return "timeout"
}

// Only implement the Timeout() function of the net.Error interface.
// This allows for checks like:
//
//   if x, ok := err.(interface{ Timeout() bool }); ok && x.Timeout() {
func (e *timeoutError) Timeout() bool {
	return true
}

// ErrTimeout is returned from timed out calls.
var ErrTimeout = &timeoutError{}

//  AIO Http context
type AIOHttpContext struct {
	state        int
	buf          *bytes.Buffer
	URI          URI
	headerSize   int
	Header       RequestHeader
	Response     ResponseHeader
	ResponseData []byte

	// dead line for reading
	headerDeadLine time.Time
	bodyDeadLine   time.Time
}
