package aiohttp

import (
	"bufio"
	"bytes"
	"log"
	"net"
	"net/textproto"

	"github.com/xtaci/gaio"
)

var (
	RequestEndFlag = []byte{0xD, 0xA, 0xD, 0xA}
)

const (
	stateRequest = iota
	stateBody
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
	ctx.tp = textproto.NewReader(bufio.NewReader(ctx.buf))
	err = proc.watcher.Read(ctx, conn, make([]byte, 1024))
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
			switch res.Operation {
			case gaio.OpRead: // read completion event
				if res.Error == nil {
					proc.processRequest(&res)
					// send back everything, we won't start to read again until write completes.
					// submit an async write request
					proc.watcher.Write(nil, res.Conn, res.Buffer[:res.Size])
				}
			case gaio.OpWrite: // write completion event
				if res.Error == nil {
					// since write has completed, let's start read on this conn again
					proc.watcher.Read(nil, res.Conn, res.Buffer[:cap(res.Buffer)])
				}
			}
		}
	}
}

// process request
func (proc *AIOHttpProcessor) processRequest(res *gaio.OpResult) {
	ctx := res.Context.(*AIOHttpContext)
	ctx.buf.Write(res.Buffer[:res.Size])

	switch ctx.state {
	case stateRequest:
		buffer := ctx.buf.Bytes()
		s := len(buffer) - res.Size - 3 // traceback at most 3 bytes
		if s > 0 {
			/* https://tools.ietf.org/html/rfc2616#page-35
			   Request       = Request-Line              ; Section 5.1
			                   *(( general-header        ; Section 4.5
			                    | request-header         ; Section 5.3
			                    | entity-header ) CRLF)  ; Section 7.1
			                   CRLF
			                   [ message-body ]          ; Section 4.3
			*/

			// O(n) search of CRLF-CRLF
			if i := bytes.Index(buffer[s:], RequestEndFlag); i != -1 {
				// we've found CRLFCRLF
			}
		}
		ctx.state = stateBody
	case stateBody:
		ctx.state = stateRequest
	}
}
