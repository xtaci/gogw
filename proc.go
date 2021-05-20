package aiohttp

import (
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
		// read into buffer
		ctx.buffer = append(ctx.buffer, res.Buffer[:res.Size]...)

		// try process header
		if err := proc.procHeader(ctx, res); err == nil {
			// initiate next reading
			proc.watcher.ReadTimeout(ctx, res.Conn, nil, ctx.headerDeadLine)
		} else {
			proc.watcher.Free(res.Conn)
		}
	} else if ctx.protoState == stateBody {
		// read into buffer
		ctx.buffer = append(ctx.buffer, res.Buffer[:res.Size]...)

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
	var headerOK bool
	for i := ctx.nextCompare; i < len(ctx.buffer); i++ {
		if ctx.buffer[i] == HeaderEndFlag[ctx.expectedChar] {
			ctx.expectedChar++
			if ctx.expectedChar == uint8(len(HeaderEndFlag)) {
				headerOK = true
				break
			}
		} else {
			ctx.expectedChar = 0
		}
	}
	ctx.nextCompare = len(ctx.buffer)

	if headerOK {
		var err error
		ctx.headerSize, err = ctx.Header.parse(ctx.buffer)
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

		// body size limit
		if ctx.Header.ContentLength() > proc.maximumBodySize {
			proc.watcher.Free(res.Conn)
			return ErrRequestBodySize
		}

		// since header has parsed, remove header bytes now
		ctx.buffer = ctx.buffer[ctx.headerSize:]

		// start to read body
		ctx.protoState = stateBody

		// prepare response struct
		ctx.Response.Reset()

		// toggle to process header
		return proc.procBody(ctx, res)
	}

	// restrict header size
	if len(ctx.buffer) > proc.maximumHeaderSize {
		proc.watcher.Free(res.Conn)
		return ErrRequestHeaderSize
	}
	return nil
}

// process body
func (proc *AsyncHttpProcessor) procBody(ctx *AIOHttpContext, res *gaio.OpResult) error {
	// read body data
	if len(ctx.buffer) < ctx.Header.ContentLength() {
		return nil
	}

	// process request
	if err := proc.handler(ctx); err != nil {
		return err
	}

	// process request
	if ctx.ResponseData != nil {
		ctx.Response.SetContentLength(len(ctx.ResponseData))
	}
	ctx.Response.Set("Connection", "Keep-Alive")

	proc.watcher.Write(ctx, res.Conn, append(ctx.Response.Header(), ctx.ResponseData...))

	// set state back to header
	ctx.protoState = stateHeader

	// discard buffer
	if ctx.Header.ContentLength() > 0 {
		ctx.buffer = ctx.buffer[ctx.Header.ContentLength():]
	}

	// a complete request has done
	// reset timeouts
	ctx.headerDeadLine = time.Now().Add(proc.headerTimeout)
	ctx.bodyDeadLine = ctx.headerDeadLine.Add(proc.bodyTimeout)

	// prepare header related data
	ctx.Header.Reset()
	ctx.nextCompare = 0
	ctx.expectedChar = 0

	// toggle to process header
	return proc.procHeader(ctx, res)
}
