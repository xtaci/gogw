package aiohttp

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/xtaci/gaio"
)

var (
	HeaderEndFlag = []byte{0xD, 0xA, 0xD, 0xA}
)

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
	watcher     *gaio.Watcher
	headHandler http.Handler // head checks
	bodyHandler http.Handler // body handlers
}

// Create processor context
func NewAIOHttpProcessor(watcher *gaio.Watcher, headHandler http.Handler, bodyHandler http.Handler) *AIOHttpProcessor {
	context := new(AIOHttpProcessor)
	context.watcher = watcher
	context.headHandler = headHandler
	context.bodyHandler = bodyHandler
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
			req, err := readRequest(reader, false)
			if err != nil {
				return
			}

			// read body length
			n, err := fixLength(false, 200, req.Method, req.Header, false)
			if err != nil {
				return
			}

			// extract header fields
			ctx.contentLength = n
			ctx.req = req

			// start to read body
			ctx.state = stateBody
			ctx.buf.Reset()

			// callback header handler
			if proc.headHandler != nil {
				proc.headHandler.ServeHTTP(nil, ctx.req)
			}

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
		ctx.req.Body = newBody(ctx.buf, ctx.contentLength)
		resp := newResponse()
		proc.bodyHandler.ServeHTTP(resp, ctx.req)

		// write status code line
		respHeader := `HTTP/1.1 %v %v
Content-Length: %v
Connection: Keep-Alive

`
		ctx.xmitBuf.Reset()
		// merge header & data
		fmt.Fprintf(ctx.xmitBuf, respHeader, 200, "OK", len(resp.buf.Bytes()))
		ctx.xmitBuf.Write(resp.buf.Bytes())

		// aio send
		proc.watcher.Write(ctx, conn, ctx.xmitBuf.Bytes())

		// set state to finished
		ctx.state = stateRequest
	}
}
