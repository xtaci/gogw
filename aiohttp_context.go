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
	Action      int    //是否关闭连接
}

// ProxyContext defines the context for a single remote request
type ProxyContext struct {
	parentContext *AIOContext

	remoteAddr   string
	protoState   int   // the state for reading
	expectedChar uint8 // fast indexing for end of header
	nextCompare  int

	request       []byte // input request
	proxyResponse []byte // proxy response

	// watcher's temp data
	respHeaderSize int
	respHeader     ResponseHeader
	buffer         []byte
	respBytes      []byte
	err            error

	// heap data references
	connsHeap *weightedConnsHeap
	wConn     *weightedConn

	// deadlines for reading, adjusted per request
	headerDeadLine time.Time
	bodyDeadLine   time.Time
}

//  AIO Http context
type AIOContext struct {
	// mark waiting for proxy response
	awaitProxy   bool
	protoState   int   // the state for reading
	expectedChar uint8 // fast indexing for end of header
	nextCompare  int

	// watcher's temp data
	respHeaderSize int
	respHeader     ResponseHeader
	buffer         []byte
	//respBytes      []byte
	//	err            error

	// deadlines for reading, adjusted per request
	headerDeadLine time.Time
	bodyDeadLine   time.Time

	headerSize   int            // current  incoming requet's header size
	Header       RequestHeader  // current incoming header content
	Response     ResponseHeader // current outgoing response header
	ResponseData []byte         // current outgoing response data

	// limiter for requests
	limiter IRequestLimiter

	proc      *AsyncHttpProcessor
	conn      net.Conn
	WSMsg     WSMessage
	wsHandler WSHandler
}
