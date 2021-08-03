package aiohttp

import (
	"bytes"
	"container/heap"
	"container/list"
	"io"
	"log"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtaci/gaio"
)

type weightedConn struct {
	idx          int
	conn         net.Conn
	disconnected int32     // atomic flag to mark whether the connection has disconnected
	requestList  list.List // pending request list, request will be submitted one by one
	inprog       bool      // mark if watcher is in progress
}

// Heaped least used connection
type weightedConnsHeap []*weightedConn

func (h weightedConnsHeap) Len() int { return len(h) }
func (h weightedConnsHeap) Less(i, j int) bool {
	return h[i].requestList.Len() < h[j].requestList.Len()
}
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

func (h *weightedConnsHeap) totalLoad() (totalLoad int) {
	for k := range *h {
		totalLoad += (*h)[k].requestList.Len()
	}
	return totalLoad
}

const (
	defaultMaximumURIConnections = 128
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
	dieOnce       sync.Once
	die           chan struct{}
	watcher       *gaio.Watcher
	headerTimeout time.Duration
	bodyTimeout   time.Duration

	chRequests    chan *RemoteContext
	chIOCompleted chan *RemoteContext

	// metrics
	maxConns int // maximum connections for a single URI

	pool map[string]*weightedConnsHeap // URI -> heap
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
	proxy.chRequests = make(chan *RemoteContext)
	proxy.chIOCompleted = make(chan *RemoteContext)
	proxy.die = make(chan struct{})
	return proxy, nil
}

// Delegate queues a request for sequential remote accessing
func (proxy *DelegationProxy) Delegate(remoteAddr string, ctx *BaseContext) error {
	// create delegated request context
	ctx.awaitRemote = true
	proxyContext := new(RemoteContext)
	proxyContext.baseContext = ctx
	proxyContext.remoteAddr = remoteAddr
	proxyContext.protoState = stateHeader

	select {
	case proxy.chRequests <- proxyContext:
	case <-proxy.die:
		return io.EOF
	}
	return nil
}

func (proxy *DelegationProxy) Close() {
	proxy.dieOnce.Do(func() {
		close(proxy.die)
	})
}

// schedules new requests
func (proxy *DelegationProxy) requestScheduler() {
LOOP:
	for {
		select {
		case ctx := <-proxy.chRequests:
			var connsHeap *weightedConnsHeap
			var exists bool

			// create if not initialized
			connsHeap, exists = proxy.pool[ctx.remoteAddr]
			if !exists {
				connsHeap = new(weightedConnsHeap)
				proxy.pool[ctx.remoteAddr] = connsHeap
			}

			// add new connections if load is to too high
			// scale up logarithmicly
			if connsHeap.Len() < proxy.maxConns && (connsHeap.Len() == 0 || int(math.Log(float64(connsHeap.totalLoad()+1))) > connsHeap.Len()) {
				conn, err := net.Dial("tcp", ctx.remoteAddr)
				if err == nil {
					newConn := &weightedConn{conn: conn, idx: 0}
					heap.Push(connsHeap, newConn)
				}
				log.Println("scale", connsHeap.Len())
			}

			// get least loaded connection from heap
			wConn := (*connsHeap)[0]
			if atomic.LoadInt32(&wConn.disconnected) == 1 {
				// re-connect if disconnected
				conn, err := net.Dial("tcp", ctx.remoteAddr)
				if err != nil {
					ctx.proxyResponse = proxyErrResponse(err)
					ctx.baseContext.proc.resumeFromProxy(ctx)
					continue LOOP
				} else {
					// replace heap top element
					wConn = &weightedConn{conn: conn, idx: 0}
					(*connsHeap)[0] = wConn
				}
			}

			// successfully loaded connection, bind some vars
			ctx.wConn = wConn         // ref
			ctx.connsHeap = connsHeap // ref

			// move data downward from base context
			baseContext := ctx.baseContext

			// BUG(xtaci): add processing to chunked data
			contentLength := baseContext.Header.ContentLength()
			if contentLength < 0 {
				contentLength = 0
			}

			// re-marshal requests to raw binary
			header := baseContext.Header.Header()
			requests := make([]byte, len(header)+contentLength)

			copy(requests, header)
			copy(requests[len(baseContext.Header.RawHeaders()):], baseContext.buffer)

			// queue request
			ctx.wConn.requestList.PushBack(ctx)
			ctx.request = requests

			if !ctx.wConn.inprog {
				proxy.watcher.Write(ctx, ctx.wConn.conn, requests)
				ctx.wConn.inprog = true
			}
			heap.Fix(connsHeap, wConn.idx) // heap fix

		case ctx := <-proxy.chIOCompleted:
			// once the request completed, we fix the heap again
			heap.Fix(ctx.connsHeap, ctx.wConn.idx)

			// scale-down
			//log.Println("totalload", ctx.wConn.requestList.Len(), ctx.connsHeap.totalLoad())
			if ctx.connsHeap.Len() > 1 && ctx.wConn.requestList.Len() == 0 {
				if int(math.Log(float64(ctx.connsHeap.totalLoad()))) < ctx.connsHeap.Len() {
					heap.Remove(ctx.connsHeap, ctx.wConn.idx)
					//proxy.watcher.Free(ctx.wConn.conn)
					//log.Println("scale down", ctx.wConn.load)
				}
			}

			// check error
			var bts []byte
			if ctx.err != nil {
				bts = proxyErrResponse(ctx.err)
			} else if len(ctx.respData) > 0 {
				var resp bytes.Buffer
				resp.Write(ctx.respHeader.Header())
				resp.Write(ctx.respData)
				bts = resp.Bytes()
			}

			// send back response
			ctx.proxyResponse = bts
			ctx.baseContext.proc.resumeFromProxy(ctx)

			// remove front request
			front := ctx.wConn.requestList.Front()
			ctx.wConn.requestList.Remove(front)

			// submit new request
			if ctx.wConn.requestList.Len() > 0 {
				nextContext := ctx.wConn.requestList.Front().Value.(*RemoteContext)

				// check if ctx has a related dead connection
				if atomic.LoadInt32(&nextContext.wConn.disconnected) == 0 {
					proxy.watcher.Write(nextContext, nextContext.wConn.conn, nextContext.request)
				} else {
					// re-submit the request to re-queue
					go func() {
						select {
						case proxy.chRequests <- nextContext:
						case <-proxy.die:
						}
					}()
				}

			} else {
				ctx.wConn.inprog = false
				log.Println("inprog false")
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
				if ctx, ok := res.Context.(*RemoteContext); ok {
					if res.Operation == gaio.OpRead {
						proxy.processResponse(ctx, &res)
						// error processing
						if res.Error != nil {
							proxy.watcher.Free(res.Conn)
							atomic.StoreInt32(&ctx.wConn.disconnected, 1)
						}
					} else if res.Operation == gaio.OpWrite {
						if res.Error != nil {
							proxy.watcher.Free(res.Conn)
							proxy.notifySchedulerError(ctx, res.Error)
							atomic.StoreInt32(&ctx.wConn.disconnected, 1)
						} else {
							// submit reading
							ctx.headerDeadLine = time.Now().Add(proxy.headerTimeout)
							proxy.watcher.ReadTimeout(ctx, res.Conn, nil, ctx.headerDeadLine)
						}
					}
				}
			}
		}
	}()
}

// process response
func (proxy *DelegationProxy) processResponse(ctx *RemoteContext, res *gaio.OpResult) {
	ctx.buffer = append(ctx.buffer, res.Buffer[:res.Size]...)
	//log.Println("body: buffer:", string(ctx.buffer))

	// process header or body
	switch ctx.protoState {
	case stateHeader:
		if err := proxy.procHeader(ctx, res); err != nil {
			proxy.watcher.Free(res.Conn)
			proxy.notifySchedulerError(ctx, err)
			atomic.StoreInt32(&ctx.wConn.disconnected, 1)
			return
		}
	case stateBody:
		if err := proxy.procBody(ctx, res); err != nil {
			proxy.watcher.Free(res.Conn)
			proxy.notifySchedulerError(ctx, err)
			atomic.StoreInt32(&ctx.wConn.disconnected, 1)
			return
		}
	}

}

// process header fields
func (proxy *DelegationProxy) procHeader(ctx *RemoteContext, res *gaio.OpResult) error {
	//log.Println("header: buffer:", string(ctx.buffer))
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
		respHeaderSize, err := ctx.respHeader.parse(ctx.buffer)
		if err != nil {
			return err
		}

		// since header has parsed, remove header bytes now
		ctx.buffer = ctx.buffer[respHeaderSize:]

		// start to process body
		ctx.protoState = stateBody
		ctx.nextCompare = 0
		ctx.expectedChar = 0
		ctx.bodyDeadLine = time.Now().Add(proxy.bodyTimeout)
		proxy.procBody(ctx, res)
	} else {
		// incomplete header, submit read again
		proxy.watcher.ReadTimeout(ctx, res.Conn, nil, ctx.headerDeadLine)
	}

	return nil
}

// process body
func (proxy *DelegationProxy) procBody(ctx *RemoteContext, res *gaio.OpResult) error {
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
			ctx.respData = make([]byte, len(ctx.buffer))
			copy(ctx.respData, ctx.buffer)
		}

	} else if contentLength > 0 {
		// read body data
		if len(ctx.buffer) >= contentLength {
			// notify request scheduler
			ctx.respData = make([]byte, contentLength)
			copy(ctx.respData, ctx.buffer)
		}

	} else if res.Error == io.EOF { // remote actively terminates
		ctx.respData = make([]byte, len(ctx.buffer))
		copy(ctx.respData, ctx.buffer)
	}

	// check if response is ready
	if ctx.respData != nil {
		select {
		case proxy.chIOCompleted <- ctx:
			return nil
		case <-proxy.die:
			return io.EOF
		}
	} else {
		// submit read again
		//log.Println("submit", string(ctx.respHeader.Header()), string(ctx.buffer), len(ctx.buffer), contentLength)
		return proxy.watcher.ReadTimeout(ctx, res.Conn, nil, ctx.bodyDeadLine)
	}
}

// link error
func (proxy *DelegationProxy) notifySchedulerError(ctx *RemoteContext, err error) {
	ctx.err = err

	log.Println("notifySchedulerError:", err)

	select {
	case proxy.chIOCompleted <- ctx:
	case <-proxy.die:
		return
	}
}
