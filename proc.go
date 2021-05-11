package aiohttp

import (
	"bytes"
	"io"
	"log"
	"net"
	"time"

	"github.com/xtaci/gaio"
)

var (
	HeaderEndFlag = []byte{0xD, 0xA, 0xD, 0xA}
)

const (
	stateHeader = iota
	stateBody
)

const (
	KB = 1024
	MB = 1024 * KB
)

const (
	defaultHeaderTimeout     = 5 * time.Second
	defaultBodyTimeout       = 15 * time.Second
	defaultMaximumHeaderSize = 2 * KB
	defaultMaximumBodySize   = 1 * MB
)

type RequestHandler func(*AIOHttpContext) error
type AIOHttpProcessor struct {
	watcher *gaio.Watcher
	die     chan struct{}
	handler RequestHandler

	// timeouts
	headerTimeout time.Duration
	bodyTimeout   time.Duration

	// buffer limits
	maximumHeaderSize int
	maximumBodySize   int
}

// Create processor context
func NewAIOHttpProcessor(watcher *gaio.Watcher, handler RequestHandler) *AIOHttpProcessor {
	proc := new(AIOHttpProcessor)
	proc.watcher = watcher
	proc.die = make(chan struct{})
	proc.handler = handler

	// init with default vault
	proc.headerTimeout = defaultHeaderTimeout
	proc.bodyTimeout = defaultBodyTimeout
	proc.maximumHeaderSize = defaultMaximumHeaderSize
	proc.maximumBodySize = defaultMaximumBodySize

	return proc
}

// Add connection to this processor
func (proc *AIOHttpProcessor) AddConn(conn net.Conn) error {
	ctx := new(AIOHttpContext)
	ctx.buf = new(bytes.Buffer)
	ctx.headerDeadLine = time.Now().Add(proc.headerTimeout)
	ctx.bodyDeadLine = ctx.headerDeadLine.Add(proc.bodyTimeout)
	return proc.watcher.ReadTimeout(ctx, conn, nil, ctx.headerDeadLine)
}

// Processor loop
func (proc *AIOHttpProcessor) StartProcessor() {
	go func() {
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
				} else if res.Operation == gaio.OpWrite {
					if res.Error != nil {
						proc.watcher.Free(res.Conn)
					}
				}
			}
		}
	}()
}

// set header timeout
func (proc *AIOHttpProcessor) SetHeaderTimeout(d time.Duration) {
	proc.headerTimeout = d
}

// set body timeout
func (proc *AIOHttpProcessor) SetBodyTimeout(d time.Duration) {
	proc.bodyTimeout = d
}

// set header size limit
func (proc *AIOHttpProcessor) SetHeaderMaximumSize(size int) {
	proc.maximumHeaderSize = size
}

// set body size limit
func (proc *AIOHttpProcessor) SetBodyMaximumSize(size int) {
	proc.maximumBodySize = size
}

// process request
func (proc *AIOHttpProcessor) processRequest(ctx *AIOHttpContext, res *gaio.OpResult) {
	if ctx.protoState == stateHeader {
		// check buffer size
		if ctx.buf.Len()+res.Size > proc.maximumHeaderSize {
			proc.watcher.Free(res.Conn)
			return
		}

		// read into buffer
		ctx.buf.Write(res.Buffer[:res.Size])

		// try process header
		if err := proc.procHeader(ctx, res); err == nil {
			// initiate next reading
			proc.watcher.ReadTimeout(ctx, res.Conn, nil, ctx.headerDeadLine)
		} else {
			proc.watcher.Free(res.Conn)
		}
	} else if ctx.protoState == stateBody {
		// body size limit
		if ctx.buf.Len() > proc.maximumBodySize {
			proc.watcher.Free(res.Conn)
			return
		}

		// read into buffer
		ctx.buf.Write(res.Buffer[:res.Size])

		// try process body
		if err := proc.procBody(ctx, res); err == nil {
			proc.watcher.ReadTimeout(ctx, res.Conn, nil, ctx.bodyDeadLine)
		} else {
			proc.watcher.Free(res.Conn)
		}
	}
}

// process header fields
func (proc *AIOHttpProcessor) procHeader(ctx *AIOHttpContext, res *gaio.OpResult) error {
	buffer := ctx.buf.Bytes()
	// traceback at most 3 extra bytes to locate CRLF-CRLF
	s := len(buffer) - res.Size - 3
	if s < 0 {
		s = 0
	}

	// O(n) search of CRLF-CRLF
	if i := bytes.Index(buffer[s:], HeaderEndFlag); i != -1 {
		ctx.headerSize = s + i + len(HeaderEndFlag)
		ctx.Header.Reset()
		_, err := ctx.Header.parse(ctx.buf.Bytes())
		if err != nil {
			//	log.Println(err)
			return err
		}

		// since header has parsed, remove header bytes now
		io.CopyN(io.Discard, ctx.buf, int64(ctx.headerSize))

		// set URI
		ctx.URI.Reset()
		ctx.URI.Parse(nil, ctx.Header.RequestURI())

		// start to read body
		ctx.protoState = stateBody

		// continue to read body
		return proc.procBody(ctx, res)
	}

	return nil
}

// process body
func (proc *AIOHttpProcessor) procBody(ctx *AIOHttpContext, res *gaio.OpResult) error {
	// read body data
	if ctx.buf.Len() < ctx.Header.ContentLength() {
		return nil
	}

	// process request
	err := proc.handler(ctx)

	// process request
	ctx.Response.Reset()
	if ctx.ResponseData != nil {
		ctx.Response.SetContentLength(len(ctx.ResponseData))
	}
	ctx.Response.Set("Connection", "Keep-Alive")

	proc.watcher.Write(ctx, res.Conn, append(ctx.Response.Header(), ctx.ResponseData...))

	// set state back to header
	ctx.protoState = stateHeader

	// discard buffer
	if ctx.Header.ContentLength() > 0 {
		io.CopyN(io.Discard, ctx.buf, int64(ctx.Header.ContentLength()))
	}

	// a complete request has done
	// reset timeouts
	ctx.headerDeadLine = time.Now().Add(proc.headerTimeout)
	ctx.bodyDeadLine = ctx.headerDeadLine.Add(proc.bodyTimeout)

	return err
}
