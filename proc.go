package aiohttp

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/xtaci/gaio"
)

var (
	HeaderEndFlag = []byte{0xD, 0xA, 0xD, 0xA}
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

var (
	// a system-wide packet buffer shared among sending, receiving and FEC
	// to mitigate high-frequency memory allocation for packets, bytes from xmitBuf
	// is aligned to 64bit
	xmitBuf sync.Pool
)

func init() {
	xmitBuf.New = func() interface{} {
		return new(bytes.Buffer)
	}
}

const (
	stateRequest = iota
	stateBody
	stateWaitLastResponseSent // last response header
	stateWaitLastBodySent
)

type AIOHttpProcessor struct {
	watcher *gaio.Watcher
}

// Create processor context
func NewAIOHttpProcessor(watcher *gaio.Watcher) *AIOHttpProcessor {
	context := new(AIOHttpProcessor)
	context.watcher = watcher
	return context
}

// Add connection to this processor
func (proc *AIOHttpProcessor) AddConn(conn net.Conn) (err error) {
	ctx := new(AIOHttpContext)
	ctx.buf = new(bytes.Buffer)
	ctx.xmitBuf = xmitBuf.Get().(*bytes.Buffer)
	err = proc.watcher.Read(ctx, conn, nil)
	if err != nil {
		return err
	}
	return nil
}

// Processor loop
func (proc *AIOHttpProcessor) Processor() {
	for {
		// loop wait for any IO events
		results, err := proc.watcher.WaitIO()
		if err != nil {
			log.Println(err)
			return
		}

		for _, res := range results {
			ctx := res.Context.(*AIOHttpContext)

			switch res.Operation {
			case gaio.OpRead: // read completion event
				if res.Error == nil {
					proc.processRequest(ctx, &res)
				} else {
					proc.watcher.Free(res.Conn)
					xmitBuf.Put(ctx.xmitBuf)
				}
			case gaio.OpWrite: // write completion event
				if res.Error == nil {
				} else {
					proc.watcher.Free(res.Conn)
					xmitBuf.Put(ctx.xmitBuf)
				}
				/*
					ctx := res.Context.(*AIOHttpContext)
					switch ctx.state {
					case stateWaitLastResponseSent:
						ctx.state = stateWaitLastBodySent
					case stateWaitLastBodySent:
						proc.watcher.Free(res.Conn)
					}
				*/
			}
		}
	}
}

// process request
func (proc *AIOHttpProcessor) processRequest(ctx *AIOHttpContext, res *gaio.OpResult) {
	ctx.buf.Write(res.Buffer[:res.Size])

	switch ctx.state {
	case stateRequest:
		buffer := ctx.buf.Bytes()
		// traceback at most 3 extra bytes to locate CRLF-CRLF
		s := len(buffer) - res.Size - 3
		if s < 0 {
			s = 0
		}

		// O(n) search of CRLF-CRLF
		if i := bytes.Index(buffer[s:], HeaderEndFlag); i != -1 {
			reader := bufio.NewReader(ctx.buf)
			err := ctx.header.Read(reader)
			if err != nil {
				return
			}

			// start to read body
			ctx.state = stateBody
			ctx.buf.Reset()

			// continue to read body
			proc.readBody(ctx, res.Conn)
		}
	case stateBody:
		proc.readBody(ctx, res.Conn)
	}

	err := proc.watcher.Read(ctx, res.Conn, nil)
	if err != nil {
		return
	}
}

func (proc *AIOHttpProcessor) readBody(ctx *AIOHttpContext, conn net.Conn) {
	if int64(ctx.buf.Len()) >= ctx.contentLength {

		// write status code line
		respHeader := `HTTP/1.1 %v %v
Content-Length: %v
Connection: Keep-Alive

`
		ctx.xmitBuf.Reset()
		respText := "Welcome!"
		// merge header & data
		fmt.Fprintf(ctx.xmitBuf, respHeader, 200, "OK", len(respText))
		ctx.xmitBuf.WriteString(respText)

		// aio send
		proc.watcher.Write(ctx, conn, ctx.xmitBuf.Bytes())

		// set state to finished
		ctx.state = stateRequest
	}
}
