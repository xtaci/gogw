package aiohttp

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net"
	"net/http"

	"github.com/xtaci/gaio"
)

var (
	RequestEndFlag = []byte{0xD, 0xA, 0xD, 0xA}
)

const (
	stateRequest = iota
	stateBody
	stateWaitLastResponseSent // last response header
	stateWaitLastBodySent
)

type AIOHttpProcessor struct {
	watcher *gaio.Watcher
	handler http.Handler
}

// Create processor context
func NewAIOHttpProcessor(watcher *gaio.Watcher, handler http.Handler) *AIOHttpProcessor {
	context := new(AIOHttpProcessor)
	context.watcher = watcher
	context.handler = handler
	return context
}

// Add connection to this processor
func (proc *AIOHttpProcessor) AddConn(conn net.Conn) (err error) {
	ctx := new(AIOHttpContext)
	ctx.buf = new(bytes.Buffer)
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
			switch res.Operation {
			case gaio.OpRead: // read completion event
				if res.Error == nil {
					proc.processRequest(&res)
				}
			case gaio.OpWrite: // write completion event
				ctx := res.Context.(*AIOHttpContext)
				switch ctx.state {
				case stateWaitLastResponseSent:
					ctx.state = stateWaitLastBodySent
				case stateWaitLastBodySent:
					proc.watcher.Free(res.Conn)
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
		s := len(buffer) - res.Size - 3 // traceback at most 3 extra bytes
		if s < 0 {
			s = 0
		}
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
			reader := bufio.NewReader(ctx.buf)
			req, err := readRequest(reader, false)
			if err != nil {
				return
			}

			// read body length
			n, err := fixLength(false, 200, req.Method, req.Header, false)
			if err != nil {
				return
			}

			ctx.contentLength = n
			ctx.req = req
			ctx.state = stateBody
			ctx.buf.Reset()
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
		ctx.req.Body = newBody(ctx.buf, ctx.contentLength)
		r := newResponse()
		proc.handler.ServeHTTP(r, ctx.req)
		codeText := http.StatusText(r.statusCode)

		// write status code line
		respHeader := `HTTP/1.1 %v %v 
Content-Length: %v
Connection: close

`
		proc.watcher.Write(ctx, conn, []byte(fmt.Sprintf(respHeader, 200, codeText, len(r.buf.Bytes()))))
		proc.watcher.Write(ctx, conn, r.buf.Bytes())

		// set state to finished
		ctx.state = stateWaitLastResponseSent
	}
}
