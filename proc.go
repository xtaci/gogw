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
	HeaderEndFlag = []byte("\r\n\r\n")
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

// IRequestHandler interface is the function prototype for request handler
type IRequestHandler func(*AIOHttpContext) error

// IRequestLimiter interface defines the function prototype for limiting request per second
type IRequestLimiter interface {
	Test(uri *URI) bool
}

// AsyncHttpProcessor is the core async http processor
type AsyncHttpProcessor struct {
	watcher *gaio.Watcher
	die     chan struct{}
	handler IRequestHandler
	limiter IRequestLimiter

	// timeouts
	headerTimeout time.Duration
	bodyTimeout   time.Duration

	// buffer limits
	maximumHeaderSize int
	maximumBodySize   int
}

// Create processor context
func NewAsyncHttpProcessor(watcher *gaio.Watcher, handler IRequestHandler, limiter IRequestLimiter) *AsyncHttpProcessor {
	proc := new(AsyncHttpProcessor)
	proc.watcher = watcher
	proc.die = make(chan struct{})
	proc.handler = handler
	proc.limiter = limiter

	// init with default vault
	proc.headerTimeout = defaultHeaderTimeout
	proc.bodyTimeout = defaultBodyTimeout
	proc.maximumHeaderSize = defaultMaximumHeaderSize
	proc.maximumBodySize = defaultMaximumBodySize

	return proc
}

// Add connection to this processor
func (proc *AsyncHttpProcessor) AddConn(conn net.Conn) error {
	ctx := new(AIOHttpContext)
	ctx.buf = new(bytes.Buffer)
	ctx.limiter = proc.limiter // a shallow copy of limiter
	ctx.headerDeadLine = time.Now().Add(proc.headerTimeout)
	ctx.bodyDeadLine = ctx.headerDeadLine.Add(proc.bodyTimeout)
	return proc.watcher.ReadTimeout(ctx, conn, nil, ctx.headerDeadLine)
}

// Processor loop
func (proc *AsyncHttpProcessor) StartProcessor() {
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
func (proc *AsyncHttpProcessor) SetHeaderTimeout(d time.Duration) {
	proc.headerTimeout = d
}

// set body timeout
func (proc *AsyncHttpProcessor) SetBodyTimeout(d time.Duration) {
	proc.bodyTimeout = d
}

// set header size limit
func (proc *AsyncHttpProcessor) SetHeaderMaximumSize(size int) {
	proc.maximumHeaderSize = size
}

// set body size limit
func (proc *AsyncHttpProcessor) SetBodyMaximumSize(size int) {
	proc.maximumBodySize = size
}

// process request
func (proc *AsyncHttpProcessor) processRequest(ctx *AIOHttpContext, res *gaio.OpResult) {
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
func (proc *AsyncHttpProcessor) procHeader(ctx *AIOHttpContext, res *gaio.OpResult) error {

	// index CRLF-CRLF
	var headerOK bool
	for i := 0; i < res.Size; i++ {
		if res.Buffer[i] == HeaderEndFlag[ctx.expectedChar] {
			ctx.expectedChar++
			if ctx.expectedChar == uint8(len(HeaderEndFlag)) {
				// reset expected end
				ctx.expectedChar = 0
				headerOK = true
				break
			}
		} else {
			ctx.expectedChar = 0
		}
	}

	if headerOK {
		ctx.Header.Reset()
		var err error
		ctx.headerSize, err = ctx.Header.parse(ctx.buf.Bytes())
		if err != nil {
			//	log.Println(err)
			return err
		}

		// try to limit the RPS
		if ctx.limiter != nil {
			var uri URI
			err = uri.Parse(nil, ctx.Header.RequestURI())
			if err != nil {
				return err
			}

			if !ctx.limiter.Test(&uri) {
				return ErrRequestLimit
			}
		}

		// since header has parsed, discard header bytes now
		io.CopyN(io.Discard, ctx.buf, int64(ctx.headerSize))

		// start to read body
		ctx.protoState = stateBody

		return proc.procBody(ctx, res)
	}
	return nil
}

// process body
func (proc *AsyncHttpProcessor) procBody(ctx *AIOHttpContext, res *gaio.OpResult) error {
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
