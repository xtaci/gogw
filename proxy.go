package aiohttp

import (
	"log"
	"net"
	"time"

	"github.com/xtaci/gaio"
)

// DelegatedRequestContext defines the context for a single remote request
type DelegatedRequestContext struct {
	protoState   int   // the state for reading
	expectedChar uint8 // fast indexing for end of header
	nextCompare  int

	request []byte // input request

	respHeaderSize int
	respHeader     ResponseHeader
	buffer         []byte
	chCompleted    chan []byte
	client         net.Conn
	remote         net.Conn

	// deadlines for reading, adjusted per request
	headerDeadLine time.Time
	bodyDeadLine   time.Time
}

// Delegation Proxy delegates a special conn to remote,
// and redirect it's IO to original connection:
//
//
//  Client Connection -> Pattern 1 -> REQ1 REQ2 ... REQn -> Remote Service -> RESP1 RESP2 ... RESPn
//      |-------------> Pattern 2 -> REQ1 REQ2 ... REQn -> Remote Service -> RESP1 RESP2 ... RESPn
//
//
type DelegationProxy struct {
	watcher       *gaio.Watcher
	headerTimeout time.Duration
	bodyTimeout   time.Duration
}

// NewDelegationProxy creates a proxy to remote service
func NewDelegationProxy(watcher *gaio.Watcher) *DelegationProxy {
	proxy := new(DelegationProxy)
	proxy.watcher = watcher
	proxy.headerTimeout = defaultHeaderTimeout
	proxy.bodyTimeout = defaultBodyTimeout
	return proxy
}

// Delegate queues a request for sequential remote accessing
func (proxy *DelegationProxy) Delegate(client net.Conn, remote net.Conn, request []byte, chCompleted chan []byte) error {
	if remote == nil {
		panic("nil conn")
	}

	// create delegated request context
	ctx := new(DelegatedRequestContext)
	ctx.client = client
	ctx.remote = remote
	ctx.request = request
	ctx.chCompleted = chCompleted
	ctx.headerDeadLine = time.Now().Add(proxy.headerTimeout)
	ctx.bodyDeadLine = ctx.headerDeadLine.Add(proxy.bodyTimeout)

	// watcher
	return proxy.watcher.Write(ctx, remote, request)
}

func (proxy *DelegationProxy) Start() {
	go func() {
		for {
			// loop wait for any IO events
			results, err := proxy.watcher.WaitIO()
			if err != nil {
				log.Println(err)
				return
			}

			// context based req/resp matchintg
			for _, res := range results {
				if ctx, ok := res.Context.(*DelegatedRequestContext); ok {
					if res.Operation == gaio.OpRead {
						if res.Error != nil {
							proxy.watcher.Free(res.Conn)
						} else {
							proxy.processResponse(ctx, &res)
						}
					} else if res.Operation == gaio.OpWrite {
						if res.Error != nil {
							proxy.watcher.Free(res.Conn)
						} else {
							// if request writing to remote has completed successfully
							// initate response reading
							proxy.watcher.Read(ctx, res.Conn, nil)
						}
					}
				}
			}
		}
	}()
}

// process response
func (proc *DelegationProxy) processResponse(ctx *DelegatedRequestContext, res *gaio.OpResult) {
	// read into buffer
	ctx.buffer = append(ctx.buffer, res.Buffer[:res.Size]...)

	// process header or body
	if ctx.protoState == stateHeader {
		if err := proc.procHeader(ctx, res.Conn); err == nil {
			proc.watcher.ReadTimeout(ctx, res.Conn, nil, ctx.headerDeadLine)
		} else {
			proc.watcher.Free(res.Conn)
		}
	} else if ctx.protoState == stateBody {
		if err := proc.procBody(ctx, res.Conn); err == nil {
			proc.watcher.ReadTimeout(ctx, res.Conn, nil, ctx.bodyDeadLine)
		} else {
			proc.watcher.Free(res.Conn)
		}
	}
}

// process header fields
func (proc *DelegationProxy) procHeader(ctx *DelegatedRequestContext, conn net.Conn) error {
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
		ctx.respHeaderSize, err = ctx.respHeader.parse(ctx.buffer)
		if err != nil {
			//	log.Println(err)
			return err
		}

		// since header has parsed, remove header bytes now
		ctx.buffer = ctx.buffer[ctx.respHeaderSize:]

		// start to read body
		ctx.protoState = stateBody

		// toggle to process header
		return proc.procBody(ctx, conn)
	}

	return nil
}

// process body
func (proc *DelegationProxy) procBody(ctx *DelegatedRequestContext, conn net.Conn) error {
	// read body data
	if len(ctx.buffer) == ctx.respHeader.ContentLength() {
	}

	return nil
}
