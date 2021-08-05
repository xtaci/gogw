package aiohttp

import (
	"net"
	"time"
)

type timeoutError struct{}

func (e *timeoutError) Error() string {
	return "timeout"
}

// Only implement the Timeout() function of the net.Error interface.
// This allows for checks like:
//
//   if x, ok := err.(interface{ Timeout() bool }); ok && x.Timeout() {
func (e *timeoutError) Timeout() bool {
	return true
}

// ErrTimeout is returned from timed out calls.
var ErrTimeout = &timeoutError{}

type WSMessage struct {
	maskKey     [4]byte
	msContinue  bool //针对分片消息是否结束的判断
	MessageType int
	CloseCode   int
	ReqData     []byte //已经完整解析后的请求业务数据
	RspHeader   []byte
	RspData     []byte //响应消息
}

// RemoteContext defines the context for a single remote request
type RemoteContext struct {
	baseContext *BaseContext

	request      []byte
	remoteAddr   string // remote service URI
	protoState   int    // the state for reading
	expectedChar uint8  // fast indexing for end of header
	nextCompare  int

	// watcher's temp data
	buffer []byte

	// remote response
	respHeader    ResponseHeader
	respData      []byte // response data
	proxyResponse []byte // proxy response(header + data)
	err           error
	done          bool
	disconnected  bool // mark if the request has detected a disconnection

	// heap data references
	connsHeap *weightedConnsHeap
	wConn     *weightedConn

	// deadlines for reading, adjusted per request
	headerDeadLine time.Time
	bodyDeadLine   time.Time
}

//  Base http processing  context
type BaseContext struct {
	conn net.Conn // client connection

	// mark waiting for remote response
	awaitRemote bool // mark if request has trapped to proxy

	protoState   int   // the state for reading
	expectedChar uint8 // fast indexing for end of header
	nextCompare  int

	// watcher's temp data
	buffer []byte

	// deadlines for reading, adjusted per request
	headerDeadLine time.Time
	bodyDeadLine   time.Time

	headerSize   int            // current incoming requet's header size
	Header       RequestHeader  // current incoming header content
	Response     ResponseHeader // current outgoing response header
	ResponseData []byte         // current outgoing response data

	// limiter for requests
	limiter IRequestLimiter

	proc *AsyncHttpProcessor // the processor it belongs to

	ShouldClose int // mark if server should close the connection

	WSMsg     WSMessage
	wsHandler WSHandler
}
