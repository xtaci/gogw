package aiohttp

import (
	"bytes"
	"log"
	"net"

	"github.com/xtaci/gaio"
)

var (
	HeaderEndFlag = []byte{0xD, 0xA, 0xD, 0xA}
)

const (
	stateRequest = iota
	stateBody
)

type responseData struct {
	ctx  *AIOHttpContext
	conn net.Conn
	buf  []byte
}

type AIOHttpProcessor struct {
	watcher *gaio.Watcher
	die     chan struct{}
}

// Create processor context
func NewAIOHttpProcessor(watcher *gaio.Watcher) *AIOHttpProcessor {
	proc := new(AIOHttpProcessor)
	proc.watcher = watcher
	proc.die = make(chan struct{})
	return proc
}

// Add connection to this processor
func (proc *AIOHttpProcessor) AddConn(conn net.Conn) error {
	ctx := new(AIOHttpContext)
	ctx.buf = new(bytes.Buffer)
	return proc.watcher.Read(ctx, conn, nil)
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
			if res.Operation == gaio.OpRead {
				if res.Error == nil {
					proc.processRequest(ctx, &res)
				} else {
					proc.watcher.Free(res.Conn)
				}
			} else {
				if res.Error == nil {
				} else {
					proc.watcher.Free(res.Conn)
				}
			}
		}
	}
}

// process request
func (proc *AIOHttpProcessor) processRequest(ctx *AIOHttpContext, res *gaio.OpResult) {
	ctx.buf.Write(res.Buffer[:res.Size])

	if ctx.state == stateRequest {
		buffer := ctx.buf.Bytes()
		// traceback at most 3 extra bytes to locate CRLF-CRLF
		s := len(buffer) - res.Size - 3
		if s < 0 {
			s = 0
		}

		// O(n) search of CRLF-CRLF
		if i := bytes.Index(buffer[s:], HeaderEndFlag); i != -1 {
			ctx.header.Reset()
			_, err := ctx.header.parse(ctx.buf.Bytes())
			if err != nil {
				return
			}

			// start to read body
			ctx.state = stateBody
			ctx.buf.Reset()

			// continue to read body
			proc.readBody(ctx, res.Conn)
		}
	} else if ctx.state == stateBody {
		proc.readBody(ctx, res.Conn)
	}

	err := proc.watcher.Read(ctx, res.Conn, nil)
	if err != nil {
		return
	}
}

func (proc *AIOHttpProcessor) readBody(ctx *AIOHttpContext, conn net.Conn) {
	var respText = "Welcome!"
	if ctx.buf.Len() >= ctx.header.ContentLength() {
		ctx.response.Reset()
		ctx.response.SetContentLength(len(respText))
		ctx.response.SetStatusCode(200)
		ctx.response.Set("Connection:", "Keep-Alive")
		// aio send
		proc.watcher.Write(ctx, conn, append(ctx.response.Header(), []byte(respText)...))

		// set state back to request
		ctx.state = stateRequest
	}
}
