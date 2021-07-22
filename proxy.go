package aiohttp

import (
	"bytes"
	"container/heap"
	"io"
	"log"
	"net"
	"sync/atomic"
	"time"

	"github.com/xtaci/gaio"
)

type weightedConn struct {
	idx          int
	conn         net.Conn
	load         uint32 // connection load
	disconnected int32  // atomic flag to mark whether the connection has disconnected
}

// Heaped least used connection
type weightedConnsHeap []*weightedConn

func (h weightedConnsHeap) Len() int           { return len(h) }
func (h weightedConnsHeap) Less(i, j int) bool { return h[i].load < h[j].load }
func (h weightedConnsHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].idx = i
	h[j].idx = j
}

func (h *weightedConnsHeap) Push(x interface{}) {
	*h = append(*h, x.(*weightedConn))
	n := len(*h)
	(*h)[n-1].idx = n - 1
}

func (h *weightedConnsHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	old[n-1] = nil // avoid memory leak
	*h = old[0 : n-1]
	return x
}

const (
	defaultMaximumURIConnections = 16
)

// Delegation Proxy delegates a special conn to remote,
// and redirect it's IO to original connection:
//
//
//  Client Connection -> Pattern 1 -> REQ1 REQ2 ... REQn -> Remote Service -> RESP1 RESP2 ... RESPn
//      |-------------> Pattern 2 -> REQ1 REQ2 ... REQn -> Remote Service -> RESP1 RESP2 ... RESPn
//
//
type DelegationProxy struct {
	die           chan struct{}
	watcher       *gaio.Watcher
	headerTimeout time.Duration
	bodyTimeout   time.Duration

	chRequests    chan *ProxyContext
	chIOCompleted chan *ProxyContext

	maxConns int                           // maximum connections for a single URI
	pool     map[string]*weightedConnsHeap // URI -> heap
}

// NewDelegationProxy creates a proxy to remote service
func NewDelegationProxy(bufSize int) (*DelegationProxy, error) {
	// create watcher
	watcher, err := gaio.NewWatcherSize(bufSize)
	if err != nil {
		return nil, err
	}

	// create proxy
	proxy := new(DelegationProxy)
	proxy.watcher = watcher
	proxy.headerTimeout = defaultHeaderTimeout
	proxy.bodyTimeout = defaultBodyTimeout
	proxy.maxConns = defaultMaximumURIConnections
	proxy.pool = make(map[string]*weightedConnsHeap)
	proxy.chRequests = make(chan *ProxyContext)
	proxy.chIOCompleted = make(chan *ProxyContext)
	proxy.die = make(chan struct{})
	return proxy, nil
}

// Delegate queues a request for sequential remote accessing
func (proxy *DelegationProxy) Delegate(remoteAddr string, ctx *AIOContext) error {
	// create delegated request context
	ctx.awaitProxy = true
	proxyContext := new(ProxyContext)
	proxyContext.parentContext = ctx
	proxyContext.remoteAddr = remoteAddr
	proxyContext.protoState = stateHeader
	proxyContext.headerDeadLine = time.Now().Add(proxy.headerTimeout)
	proxyContext.bodyDeadLine = ctx.headerDeadLine.Add(proxy.bodyTimeout)

	select {
	case proxy.chRequests <- proxyContext:
	case <-proxy.die:
		return io.EOF
	}
	return nil
}

func (proxy *DelegationProxy) initConnsHeap(remoteAddr string) (h *weightedConnsHeap, err error) {
	h = new(weightedConnsHeap)

	for i := 0; i < proxy.maxConns; i++ {
		conn, err := net.Dial("tcp", remoteAddr)
		if err != nil {
			return nil, err
		}

		wConn := &weightedConn{conn: conn, load: 0}
		heap.Push(h, wConn)
	}

	return h, nil
}

func (proxy *DelegationProxy) requestScheduler() {
LOOP:
	for {
		select {
		case ctx := <-proxy.chRequests:
			var connsHeap *weightedConnsHeap
			var exists bool
			var err error

			// create if not initialized
			connsHeap, exists = proxy.pool[ctx.remoteAddr]
			if !exists {
				// create conn
				connsHeap, err = proxy.initConnsHeap(ctx.remoteAddr)
				if err != nil {
					ctx.proxyResponse = proxyErrResponse(err)
					ctx.parentContext.proc.resumeFromProxy(ctx)
					continue LOOP
				}
				proxy.pool[ctx.remoteAddr] = connsHeap
			}

			// load conn from heap
			wConn := (*connsHeap)[0]
			if atomic.LoadInt32(&wConn.disconnected) == 1 {
				// re-connect if disconnected
				conn, err := net.Dial("tcp", ctx.remoteAddr)
				if err != nil {
					ctx.proxyResponse = proxyErrResponse(err)
					ctx.parentContext.proc.resumeFromProxy(ctx)
					continue LOOP
				} else {
					// replace heap top element
					wConn = &weightedConn{conn: conn, load: 0, idx: 0}
					(*connsHeap)[0] = wConn
				}
			}

			// successfully loaded connection, bind some vars
			ctx.wConn = wConn              // ref
			ctx.connsHeap = connsHeap      // ref
			wConn.load++                   // adjust weight
			heap.Fix(connsHeap, wConn.idx) // heap fix

			// move data downward from upper context
			parentContext := ctx.parentContext
			contentLength := parentContext.Header.ContentLength()
			if contentLength < 0 {
				contentLength = 0
			}

			// marshal to binary
			header := parentContext.Header.Header()
			requests := make([]byte, len(header)+contentLength)
			copy(requests, header)
			copy(requests[len(parentContext.Header.RawHeaders()):], parentContext.buffer)

			//println(string(parentContext.Header.Header()))
			//println(string(requests))
			proxy.watcher.Write(ctx, ctx.wConn.conn, requests)

		case ctx := <-proxy.chIOCompleted:
			// once the request completed, we reduce the load of the connection
			ctx.wConn.load--
			heap.Fix(ctx.connsHeap, ctx.wConn.idx)

			// check error
			var bts []byte
			if ctx.err != nil {
				bts = proxyErrResponse(ctx.err)
			} else if len(ctx.respBytes) > 0 {
				var resp bytes.Buffer
				resp.Write(ctx.respHeader.Header())
				resp.Write(ctx.respBytes)
				bts = resp.Bytes()
			}

			// send back response
			ctx.proxyResponse = bts
			ctx.parentContext.proc.resumeFromProxy(ctx)
		case <-proxy.die:
			return
		}
	}
}

func (proxy *DelegationProxy) Start() {
	go proxy.requestScheduler()

	go func() {
		for {
			results, err := proxy.watcher.WaitIO()
			if err != nil {
				log.Println(err)
				return
			}

			for _, res := range results {
				if ctx, ok := res.Context.(*ProxyContext); ok {
					if res.Operation == gaio.OpRead {
						if res.Error != nil {
							proxy.watcher.Free(res.Conn)
							proxy.notifySchedulerError(ctx, res.Error)
							atomic.StoreInt32(&ctx.wConn.disconnected, 1)
						} else {
							proxy.processResponse(ctx, &res)
						}
					} else if res.Operation == gaio.OpWrite {
						if res.Error != nil {
							proxy.watcher.Free(res.Conn)
							proxy.notifySchedulerError(ctx, res.Error)
							atomic.StoreInt32(&ctx.wConn.disconnected, 1)
						} else {
							// if request writing to remote has completed successfully
							// initate response reading
							proxy.watcher.ReadTimeout(ctx, res.Conn, nil, ctx.headerDeadLine)
						}
					}
				}
			}
		}
	}()
}

// process response
func (proxy *DelegationProxy) processResponse(ctx *ProxyContext, res *gaio.OpResult) {
	// read into buffer
	var buf bytes.Buffer
	buf.Write(res.Buffer[:res.Size])
	ctx.buffer = append(ctx.buffer, res.Buffer[:res.Size]...)

	// process header or body
PROC_AGAIN:
	switch ctx.protoState {
	case stateHeader:
		if err := proxy.procHeader(ctx, res.Conn); err != nil {
			proxy.watcher.Free(res.Conn)
			proxy.notifySchedulerError(ctx, err)
			atomic.StoreInt32(&ctx.wConn.disconnected, 1)
			return
		}

		if ctx.protoState == stateHeader {
			proxy.watcher.ReadTimeout(ctx, res.Conn, nil, ctx.headerDeadLine)
		} else if ctx.protoState == stateBody {
			goto PROC_AGAIN
		}
	case stateBody:
		if err := proxy.procBody(ctx, res.Conn); err != nil {
			proxy.watcher.Free(res.Conn)
			proxy.notifySchedulerError(ctx, err)
			atomic.StoreInt32(&ctx.wConn.disconnected, 1)
			return
		}

		if ctx.err == nil && ctx.respBytes == nil {
			proxy.watcher.ReadTimeout(ctx, res.Conn, nil, ctx.bodyDeadLine)
		}
	}
}

// process header fields
func (proxy *DelegationProxy) procHeader(ctx *ProxyContext, conn net.Conn) error {
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
			return err
		}

		// since header has parsed, remove header bytes now
		ctx.buffer = ctx.buffer[ctx.respHeaderSize:]

		// start to read body
		ctx.protoState = stateBody
		ctx.nextCompare = 0
		ctx.expectedChar = 0

	}

	return nil
}

// process body
func (proxy *DelegationProxy) procBody(ctx *ProxyContext, conn net.Conn) error {
	contentLength := ctx.respHeader.ContentLength()
	if contentLength == -1 {
		// chunked data
		// read until \r\n\r\n
		var dataOK bool
		for i := ctx.nextCompare; i < len(ctx.buffer); i++ {
			if ctx.buffer[i] == ChunkDataEndFlag[ctx.expectedChar] {
				ctx.expectedChar++
				if ctx.expectedChar == uint8(len(ChunkDataEndFlag)) {
					dataOK = true
					break
				}
			} else {
				ctx.expectedChar = 0
			}
		}
		ctx.nextCompare = len(ctx.buffer)

		if dataOK {
			ctx.respBytes = make([]byte, len(ctx.buffer))
			copy(ctx.respBytes, ctx.buffer)
		}

	} else if contentLength > 0 {
		// read body data
		if len(ctx.buffer) >= contentLength {
			// notify request scheduler
			ctx.respBytes = make([]byte, contentLength)
			copy(ctx.respBytes, ctx.buffer)
		}
	}

	// check if response is ready
	if ctx.respBytes != nil {
		select {
		case proxy.chIOCompleted <- ctx:
		case <-proxy.die:
			return io.EOF
		}
	}

	return nil
}

func (proxy *DelegationProxy) notifySchedulerError(ctx *ProxyContext, err error) {
	ctx.err = err

	log.Println("notifySchedulerError:", err)

	select {
	case proxy.chIOCompleted <- ctx:
	case <-proxy.die:
		return
	}
}
