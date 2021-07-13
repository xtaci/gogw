package aiohttp

import (
	"container/heap"
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

// DelegatedRequestContext defines the context for a single remote request
type DelegatedRequestContext struct {
	remoteAddr   string
	protoState   int   // the state for reading
	expectedChar uint8 // fast indexing for end of header
	nextCompare  int

	request []byte // input request

	respHeaderSize int
	respHeader     ResponseHeader
	buffer         []byte
	respBytes      []byte
	chResponse     chan []byte

	// heap data references
	connsHeap *weightedConnsHeap
	wConn     *weightedConn

	// deadlines for reading, adjusted per request
	headerDeadLine time.Time
	bodyDeadLine   time.Time
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

	chRequests    chan *DelegatedRequestContext
	chIOCompleted chan *DelegatedRequestContext

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
	proxy.chRequests = make(chan *DelegatedRequestContext)
	proxy.chIOCompleted = make(chan *DelegatedRequestContext)
	proxy.die = make(chan struct{})
	return proxy, nil
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

// Delegate queues a request for sequential remote accessing
// TODO: a request has a guaranteed response
func (proxy *DelegationProxy) Delegate(remoteAddr string, request []byte, chResponse chan []byte) error {
	var connsHeap *weightedConnsHeap
	var exists bool
	var err error

	// create if not initialized
	connsHeap, exists = proxy.pool[remoteAddr]
	if !exists {
		// create conn
		connsHeap, err = proxy.initConnsHeap(remoteAddr)
		if err != nil {
			return err
		}
		proxy.pool[remoteAddr] = connsHeap
	}

	// create delegated request context
	ctx := new(DelegatedRequestContext)
	ctx.protoState = stateHeader
	ctx.request = request
	ctx.chResponse = chResponse
	ctx.headerDeadLine = time.Now().Add(proxy.headerTimeout)
	ctx.bodyDeadLine = ctx.headerDeadLine.Add(proxy.bodyTimeout)

	proxy.chRequests <- ctx
	return nil
}

func (proxy *DelegationProxy) requestScheduler() {
	for {
		select {
		case ctx := <-proxy.chRequests:
			connsHeap := proxy.pool[ctx.remoteAddr]
			// load conn from heap
			wConn := (*connsHeap)[0]
			// check disconnection
			if atomic.LoadInt32(&wConn.disconnected) == 1 {
				conn, err := net.Dial("tcp", ctx.remoteAddr)
				if err != nil {
					// TODO:
				}
				wConn := &weightedConn{conn: conn, load: 0, idx: 0}
				// replace heap top element
				(*connsHeap)[0] = wConn
			}
			ctx.wConn = wConn              // ref
			ctx.connsHeap = connsHeap      // ref
			wConn.load++                   // adjust weight
			heap.Fix(connsHeap, wConn.idx) // heap fix

			// watcher
			proxy.watcher.Write(ctx, ctx.wConn.conn, ctx.request)
		case ctx := <-proxy.chIOCompleted:
			// once the request completes, we reduce the load of the connection
			ctx.wConn.load--
			heap.Fix(ctx.connsHeap, ctx.wConn.idx)

			// send back request
			select {
			case ctx.chResponse <- ctx.respBytes:
			case <-proxy.die:
				return
			}
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
				if ctx, ok := res.Context.(*DelegatedRequestContext); ok {
					if res.Operation == gaio.OpRead {
						if res.Error != nil {
							proxy.watcher.Free(res.Conn)
							atomic.StoreInt32(&ctx.wConn.disconnected, 1)
						} else {
							proxy.processResponse(ctx, &res)
						}
					} else if res.Operation == gaio.OpWrite {
						if res.Error != nil {
							proxy.watcher.Free(res.Conn)
							atomic.StoreInt32(&ctx.wConn.disconnected, 1)
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
func (proxy *DelegationProxy) processResponse(ctx *DelegatedRequestContext, res *gaio.OpResult) {
	// read into buffer
	ctx.buffer = append(ctx.buffer, res.Buffer[:res.Size]...)

	// process header or body
	if ctx.protoState == stateHeader {
		if err := proxy.procHeader(ctx, res.Conn); err == nil {
			proxy.watcher.ReadTimeout(ctx, res.Conn, nil, ctx.headerDeadLine)
		} else {
			proxy.watcher.Free(res.Conn)
			atomic.StoreInt32(&ctx.wConn.disconnected, 1)
		}
	} else if ctx.protoState == stateBody {
		if err := proxy.procBody(ctx, res.Conn); err == nil {
			proxy.watcher.ReadTimeout(ctx, res.Conn, nil, ctx.bodyDeadLine)
		} else {
			proxy.watcher.Free(res.Conn)
			atomic.StoreInt32(&ctx.wConn.disconnected, 1)
		}
	}
}

// process header fields
func (proxy *DelegationProxy) procHeader(ctx *DelegatedRequestContext, conn net.Conn) error {
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
		return proxy.procBody(ctx, conn)
	}

	return nil
}

// process body
func (proxy *DelegationProxy) procBody(ctx *DelegatedRequestContext, conn net.Conn) error {
	// read body data
	if len(ctx.buffer) >= ctx.respHeader.ContentLength() {
		// notify request scheduler
		ctx.respBytes = make([]byte, ctx.respHeader.ContentLength())
		copy(ctx.respBytes, ctx.buffer)

		select {
		case proxy.chIOCompleted <- ctx:
		case <-proxy.die:
			return nil
		}
	}

	return nil
}
