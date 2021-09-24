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
	"time"

	"github.com/xtaci/gaio"
)

// a connection with weight(defined as list.Len())
type weightedConn struct {
	conn        net.Conn
	idx         int       // position in heap
	requestList list.List // pending request list, request will be submitted one by one
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

	newRequests   []*RemoteContext
	newRequestsMu sync.Mutex

	chNotifyNewRequests chan struct{}
	chIOCompleted       chan *RemoteContext

	// metrics
	maxConns int // maximum connections for a single URI

	pool map[string]*weightedConnsHeap // URI -> heap

	submit int
	// directwrite   int
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
	proxy.chNotifyNewRequests = make(chan struct{}, 1)
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

	proxy.newRequestsMu.Lock()
	proxy.newRequests = append(proxy.newRequests, proxyContext)
	proxy.newRequestsMu.Unlock()

	select {
	case proxy.chNotifyNewRequests <- struct{}{}:
	default:
	}
	return nil
}

func (proxy *DelegationProxy) Close() {
	proxy.dieOnce.Do(func() {
		close(proxy.die)
	})
}

// sched a new request
func (proxy *DelegationProxy) sched(ctx *RemoteContext) {
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
	if connsHeap.Len() < proxy.maxConns {
		if connsHeap.Len() == 0 || int(math.Log(float64(connsHeap.totalLoad()+1))) > connsHeap.Len() {
			if conn, err := net.Dial("tcp", ctx.remoteAddr); err == nil {
				newConn := &weightedConn{conn: conn}
				heap.Push(connsHeap, newConn)
			}
			log.Println("scale up", connsHeap.Len())
		}
	}

	// connection broken check
	if connsHeap.Len() == 0 {
		ctx.proxyResponse = proxyErrResponse(ErrProxyConnect)
		ctx.baseContext.proc.resumeFromProxy(ctx)
		return
	}
	// get least loaded connection from heap
	wConn := (*connsHeap)[0]

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
	header := baseContext.Header.RawHeaders()
	requests := make([]byte, len(header)+contentLength)

	copy(requests, header)
	copy(requests[len(baseContext.Header.RawHeaders()):], baseContext.buffer)

	// queue request
	ctx.wConn.requestList.PushBack(ctx)
	ctx.request = requests

	// the only request, submit immediately
	if ctx.wConn.requestList.Len() == 1 {
		if err := proxy.watcher.Write(ctx, ctx.wConn.conn, requests); err != nil {
			go proxy.notifySchedulerError(ctx, err)
		}
		//proxy.directwrite++
		//log.Println("directwrite", proxy.directwrite)
	}
	heap.Fix(connsHeap, wConn.idx) // heap fix

	proxy.submit++
	//log.Println("submit", proxy.submit)
}

// goroutine to accept new requests
func (proxy *DelegationProxy) requestScheduler() {
	var numDone int
	//var schedwrite int
	for {
		select {
		case <-proxy.chNotifyNewRequests:
			var requests []*RemoteContext
			proxy.newRequestsMu.Lock()
			requests = proxy.newRequests
			proxy.newRequests = nil
			proxy.newRequestsMu.Unlock()

			for k := range requests {
				proxy.sched(requests[k])
			}
		case ctx := <-proxy.chIOCompleted:
			numDone++
			//log.Println("numdone", numDone)
			if ctx.done {
				continue
			}
			ctx.done = true

			// marshal response bytes
			var bts []byte
			if ctx.err != nil {
				bts = proxyErrResponse(ctx.err)
			} else if len(ctx.respData) > 0 {
				var resp bytes.Buffer
				resp.Write(ctx.respHeader.Header())
				resp.Write(ctx.respData)
				bts = resp.Bytes()
			}

			// wakeup base context
			ctx.proxyResponse = bts
			ctx.baseContext.proc.resumeFromProxy(ctx)

			// remove front request
			front := ctx.wConn.requestList.Front()
			ctx.wConn.requestList.Remove(front)

			// we fix the heap again
			heap.Fix(ctx.connsHeap, ctx.wConn.idx)

			// scale-down if necessary
			//log.Println("totalload", ctx.wConn.requestList.Len(), ctx.connsHeap.totalLoad())
			if ctx.connsHeap.Len() > 1 && ctx.wConn.requestList.Len() == 0 {
				if int(math.Log(float64(ctx.connsHeap.totalLoad()))) < ctx.connsHeap.Len() {
					proxy.watcher.Free(ctx.wConn.conn)
					heap.Remove(ctx.connsHeap, ctx.wConn.idx)
					log.Println("scale down", ctx.connsHeap.Len())
				}
			}

			// check if previous ctx has a related dead connection
			if ctx.disconnected {
				// propagated back to wConn disconnected
				// check if ctx has a related dead connection, try re-connect
				if conn, err := net.Dial("tcp", ctx.remoteAddr); err == nil {
					//log.Println("dial")
					// replace previous conn in weightedConn
					proxy.watcher.Free(ctx.wConn.conn)
					ctx.wConn.conn = conn
				}
			}

			// submit next request
			if ctx.wConn.requestList.Len() > 0 {
				nextContext := ctx.wConn.requestList.Front().Value.(*RemoteContext)
				if err := proxy.watcher.Write(nextContext, nextContext.wConn.conn, nextContext.request); err != nil {
					go proxy.notifySchedulerError(ctx, err)
				}
				//schedwrite++
				//log.Println("schedwrite", schedwrite)
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
					// mark connection error
					if res.Error != nil {
						ctx.disconnected = true
					}

					if res.Operation == gaio.OpRead {
						proxy.processResponse(ctx, &res)
					} else if res.Operation == gaio.OpWrite {
						if res.Error != nil {
							proxy.notifySchedulerError(ctx, res.Error)
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

	// process header or body
	switch ctx.protoState {
	case stateHeader:
		if err := proxy.procHeader(ctx, res); err != nil {
			proxy.notifySchedulerError(ctx, err)
			return
		}
	case stateBody:
		if err := proxy.procBody(ctx, res); err != nil {
			proxy.notifySchedulerError(ctx, err)
			return
		}
	}
}

// process header fields
func (proxy *DelegationProxy) procHeader(ctx *RemoteContext, res *gaio.OpResult) error {
	//log.Println("header: buffer:", string(ctx.buffer))
	//log.Println("procHeader", res.Error, ctx.disconnected)
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
		return proxy.procBody(ctx, res)
	} else if res.Error == nil { // incomplete header, submit read again
		return proxy.watcher.ReadTimeout(ctx, res.Conn, nil, ctx.headerDeadLine)
	} else {
		return res.Error
	}
}

// process body
func (proxy *DelegationProxy) procBody(ctx *RemoteContext, res *gaio.OpResult) error {
	//	log.Println("procBody")
	contentLength := ctx.respHeader.ContentLength()
	hasResponse := false
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
			hasResponse = true
		}

	} else if contentLength > 0 {
		// read body data
		if len(ctx.buffer) >= contentLength {
			// notify request scheduler
			ctx.respData = make([]byte, contentLength)
			copy(ctx.respData, ctx.buffer)
			hasResponse = true
		}

	} else if res.Error == io.EOF { // remote actively terminates
		// handling of connection:close
		ctx.respData = make([]byte, len(ctx.buffer))
		copy(ctx.respData, ctx.buffer)
		hasResponse = true
	}

	// check if response is ready
	if hasResponse {
		select {
		case proxy.chIOCompleted <- ctx:
			return nil
		case <-proxy.die:
			return io.EOF
		}
	} else if res.Error == nil {
		// submit read again
		//log.Println("submit", string(ctx.respHeader.Header()), string(ctx.buffer), len(ctx.buffer), contentLength)
		return proxy.watcher.ReadTimeout(ctx, res.Conn, nil, ctx.bodyDeadLine)
	} else {
		return res.Error
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
