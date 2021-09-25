package aiohttp

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"github.com/xtaci/gaio"
)

var (
	HeaderEndFlag    = []byte("\r\n\r\n")
	ChunkDataEndFlag = []byte("0\r\n\r\n")
)

const (
	stateHeader = iota
	stateBody
	stateProxy
	stateWS // 升级为了websocket
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

const (
	CloseNormalClosure           = 1000
	CloseGoingAway               = 1001
	CloseProtocolError           = 1002
	CloseUnsupportedData         = 1003
	CloseNoStatusReceived        = 1005
	CloseAbnormalClosure         = 1006
	CloseInvalidFramePayloadData = 1007
	ClosePolicyViolation         = 1008
	CloseMessageTooBig           = 1009
	CloseMandatoryExtension      = 1010
	CloseInternalServerErr       = 1011
	CloseServiceRestart          = 1012
	CloseTryAgainLater           = 1013
	CloseTLSHandshake            = 1015
)

var validReceivedCloseCodes = map[int]bool{
	// see http://www.iana.org/assignments/websocket/websocket.xhtml#close-code-number

	CloseNormalClosure:           true,
	CloseGoingAway:               true,
	CloseProtocolError:           true,
	CloseUnsupportedData:         true,
	CloseNoStatusReceived:        false,
	CloseAbnormalClosure:         false,
	CloseInvalidFramePayloadData: true,
	ClosePolicyViolation:         true,
	CloseMessageTooBig:           true,
	CloseMandatoryExtension:      true,
	CloseInternalServerErr:       true,
	CloseServiceRestart:          true,
	CloseTryAgainLater:           true,
	CloseTLSHandshake:            false,
}

const (
	// Frame header
	finalBit = 1 << 7
	rsv1Bit  = 1 << 6
	rsv2Bit  = 1 << 5
	rsv3Bit  = 1 << 4

	// Frame header byte 1 bits from Section 5.2 of RFC 6455
	maskBit = 1 << 7

	maxFrameHeaderSize         = 2 + 8 + 4 // Fixed header + length + mask
	maxControlFramePayloadSize = 125

	continuationFrame = 0
	noFrame           = -1
)

// The message types are defined in RFC 6455, section 11.8.
const (
	// TextMessage denotes a text data message. The text message payload is
	// interpreted as UTF-8 encoded text data.
	TextMessage = 1

	// BinaryMessage denotes a binary data message.
	BinaryMessage = 2

	// CloseMessage denotes a close control message. The optional message
	// payload contains a numeric code and text. Use the FormatCloseMessage
	// function to format a close message payload.
	CloseMessage = 8

	// PingMessage denotes a ping control message. The optional message payload
	// is UTF-8 encoded text.
	PingMessage = 9

	// PongMessage denotes a pong control message. The optional message payload
	// is UTF-8 encoded text.
	PongMessage = 10
)

var (
	errUpgrade = errors.New("uprade failed")
)

// IRequestHandler interface is the function prototype for request handler
type IRequestHandler func(*BaseContext) error

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

	// resume signal
	chResume chan *RemoteContext
}

// Create processor context
func NewAsyncHttpProcessor(watcher *gaio.Watcher, handler IRequestHandler, limiter IRequestLimiter) *AsyncHttpProcessor {
	proc := new(AsyncHttpProcessor)
	proc.watcher = watcher
	proc.die = make(chan struct{})
	proc.chResume = make(chan *RemoteContext)
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
	ctx := new(BaseContext)
	ctx.proc = proc
	ctx.conn = conn
	ctx.limiter = proc.limiter // a shallow copy of limiter
	ctx.headerDeadLine = time.Now().Add(proc.headerTimeout)
	ctx.bodyDeadLine = ctx.headerDeadLine.Add(proc.bodyTimeout)
	return proc.watcher.ReadTimeout(ctx, conn, nil, ctx.headerDeadLine)
}

// Processor loop
func (proc *AsyncHttpProcessor) StartProcessor() {
	chResults := make(chan []gaio.OpResult)

	go func() {
		for {
			// loop wait for any IO events
			results, err := proc.watcher.WaitIO()
			if err != nil {
				log.Println(err)
				return
			}

			select {
			case chResults <- results:
			case <-proc.die:
				return
			}
		}
	}()

	// agent for channeling
	go func() {
		//numResumed := 0
		for {
			select {
			case remoteCtx := <-proc.chResume:
				//numResumed++
				localCtx := remoteCtx.baseContext
				if remoteCtx.respHeader.ConnectionClose() {
					localCtx.CloseAfterWrite = Close
				}

				//log.Println("numResumed", numResumed)
				if len(remoteCtx.proxyResponse) > 0 {
					//	log.Println("numWritten", numResumed)
					localCtx.numWrites++
					proc.watcher.Write(localCtx, localCtx.conn, remoteCtx.proxyResponse)
				}

				// set to read state
				localCtx.protoState = stateHeader
				localCtx.nextCompare = 0
				localCtx.expectedChar = 0

				// discard previous buffer
				if localCtx.Header.ContentLength() > 0 {
					localCtx.buffer = localCtx.buffer[localCtx.Header.ContentLength():]
				}

				// continue to process requests
				proc.processRequest(localCtx)

			case results := <-chResults:
				for _, res := range results {
					if ctx, ok := res.Context.(*BaseContext); ok {
						if res.Operation == gaio.OpRead {
							//log.Println("Read from ", res.Conn.RemoteAddr(), len(ctx.buffer))
							if res.Error == nil {
								// read into buffer
								ctx.buffer = append(ctx.buffer, res.Buffer[:res.Size]...)
								proc.processRequest(ctx)
							} else {
								//log.Println("Read error ", res.Conn.RemoteAddr(), res.Error)
								proc.watcher.Free(res.Conn)
							}
						} else if res.Operation == gaio.OpWrite {
							ctx.numWrites--
							if res.Error != nil || (ctx.numWrites == 0 && ctx.CloseAfterWrite == Close) {
								//	log.Println("close after write", res.Error)
								proc.watcher.Free(res.Conn)
							}
						}
					}
				}
			case <-proc.die:
				return
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
func (proc *AsyncHttpProcessor) processRequest(ctx *BaseContext) {
	// process header or body
	if ctx.protoState == stateHeader {
		if err := proc.procHeader(ctx); err != nil {
			proc.watcher.Free(ctx.conn)
		}
	} else if ctx.protoState == stateBody {
		if err := proc.procBody(ctx); err != nil {
			proc.watcher.Free(ctx.conn)
		}
	} else if ctx.protoState == stateWS {
		if err := proc.procWS(ctx, ctx.conn); err != nil {
			proc.watcher.Free(ctx.conn)
		} else {
			proc.watcher.Read(ctx, ctx.conn, nil) //no need timeout for websocket, only few request
		}
	} else if ctx.protoState == stateProxy {
		// do nothing
	}
}

// resume processing from proxy
func (proc *AsyncHttpProcessor) resumeFromProxy(proxyCtx *RemoteContext) {
	// resume state and normal processing
	select {
	case proc.chResume <- proxyCtx:
	case <-proc.die:
	}

	return
}

// process header fields
func (proc *AsyncHttpProcessor) procHeader(ctx *BaseContext) error {
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

	// restrict header size
	if len(ctx.buffer) > proc.maximumHeaderSize {
		return ErrRequestHeaderSize
	}

	if headerOK {
		var err error
		ctx.Header.Reset()
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
			return ErrRequestBodySize
		}

		// since header has parsed, remove header bytes now
		ctx.buffer = ctx.buffer[ctx.headerSize:]
		// prepare response struct
		ctx.Response.Reset()
		ctx.ResponseData = nil

		// check websocket update
		if ctx.Header.upgrade {
			_ = proc.proUpgradeCheck(ctx)
			// only 1 msg if upgrade
			if len(ctx.buffer) != 0 {
				ctx.Response.SetStatusCode(StatusBadRequest)
				ctx.ResponseData = ctx.ResponseData[:0]
				ctx.ResponseData = append(ctx.ResponseData, "websocket: too many request"...)
				proc.WriteHttpRspData(ctx, true)
				return errUpgrade
			}

			if ctx.Response.StatusCode() == StatusOK {
				proc.WriteHttpRspData(ctx, false) // special header, already done
				ctx.WSMsg = &WSMessage{}
				ctx.protoState = stateWS
			} else {
				proc.WriteHttpRspData(ctx, true)
			}

			proc.watcher.Read(ctx, ctx.conn, nil)
			return nil
		}

		// start to read body
		ctx.protoState = stateBody

		// toggle to process header
		return proc.procBody(ctx)
	} else {
		ctx.headerDeadLine = time.Now().Add(proc.headerTimeout)
		proc.watcher.ReadTimeout(ctx, ctx.conn, nil, ctx.headerDeadLine)
	}

	return nil
}

// process body
func (proc *AsyncHttpProcessor) procBody(ctx *BaseContext) error {
	// read body data
	if len(ctx.buffer) < ctx.Header.ContentLength() {
		// submit again
		proc.watcher.ReadTimeout(ctx, ctx.conn, nil, ctx.bodyDeadLine)
		return nil
	}

	// call back handler
	if err := proc.handler(ctx); err != nil {
		return err
	}

	// if it's a proxied request, stop here
	if ctx.protoState == stateProxy {
		return nil
	}

	// discard buffer
	if ctx.Header.ContentLength() > 0 {
		ctx.buffer = ctx.buffer[ctx.Header.ContentLength():]
	}

	// set read state
	ctx.protoState = stateHeader
	ctx.nextCompare = 0
	ctx.expectedChar = 0

	proc.WriteHttpRspData(ctx, true)
	// toggle to process header
	return proc.procHeader(ctx)
}

func (proc *AsyncHttpProcessor) WriteHttpRspData(ctx *BaseContext, needHeader bool) {

	if needHeader {
		// set required field
		if ctx.ResponseData != nil {
			ctx.Response.SetContentLength(len(ctx.ResponseData))
		}
		if !ctx.Header.ConnectionClose() {
			ctx.Response.Set("Connection", "Keep-Alive")
		} else {
			ctx.Response.Set("Connection", "Close")
			ctx.CloseAfterWrite = Close
		}

		//if len(ctx.Response.contentType) == 0{
		//	ctx.Response.Set("Connection", "Keep-Alive")
		//}

		// send back
		ctx.numWrites++
		proc.watcher.Write(ctx, ctx.conn, append(ctx.Response.Header(), ctx.ResponseData...))
	} else {
		ctx.numWrites++
		proc.watcher.Write(ctx, ctx.conn, ctx.ResponseData)
	}
}

// websocket
type WSHandler func(*BaseContext) error

type WSHandlerInfo struct {
	mu      sync.RWMutex
	pattern string
	handler WSHandler
	hosts   bool // whether any patterns contain hostnames
}

// websocket注册 只能注册一个
// 暂时保留加锁处理 方便后续动态配置更新
var wsHandlerInfo WSHandlerInfo

func HandleWSFunc(pattern string, handler WSHandler) {
	wsHandlerInfo.HandleWSFunc(pattern, handler)
}

func (wsi *WSHandlerInfo) HandleWSFunc(pattern string, handler WSHandler) {
	if pattern == "" || handler == nil {
		return
	}

	wsi.mu.Lock()
	defer wsi.mu.Unlock()

	wsi.pattern = pattern
	wsi.handler = handler
	if pattern[0] != '/' {
		wsi.hosts = true
	}

}

func getWSHandler(ctx *BaseContext) WSHandler {
	return wsHandlerInfo.GetWSHandler(ctx)
}

func (wsi *WSHandlerInfo) GetWSHandler(ctx *BaseContext) WSHandler {
	wsi.mu.RLock()
	defer wsi.mu.RUnlock()

	var path string
	if wsi.hosts {
		path = string(ctx.Header.hostName) + string(ctx.Header.path)
	} else {
		path = string(ctx.Header.path)
	}

	var h WSHandler
	if path == wsi.pattern {
		h = wsi.handler
	}

	if h == nil {
		ctx.Response.SetStatusCode(StatusNotFound)
		return nil
	}

	return h
}

func (proc *AsyncHttpProcessor) proUpgradeCheck(ctx *BaseContext) error {
	const badHandshake = "websocket: the client is not using the websocket protocol: "
	ctx.Response.SetStatusCode(StatusOK)

	// 判断URL是否一致
	h := getWSHandler(ctx)
	if h == nil {
		ctx.Response.SetStatusCode(StatusBadRequest)
		ctx.ResponseData = append(ctx.ResponseData, "websocket: request url error"...)
		return nil
	}

	if bytes.Compare(ctx.Header.method, []byte("GET")) != 0 {
		ctx.Response.SetStatusCode(StatusMethodNotAllowed)
		ctx.ResponseData = append(ctx.ResponseData, badHandshake+"request method is not GET"...)
		return nil
	}

	upVal := ctx.Header.Peek("upgrade")
	if !caseInsensitiveContain(upVal, []byte("websocket")) {
		ctx.Response.SetStatusCode(StatusBadRequest)
		ctx.ResponseData = append(ctx.ResponseData, badHandshake+"'websocket' token not found in 'Upgrade' header"...)
		return nil
	}

	challengeKey := ctx.Header.Peek("sec-websocket-key")
	if len(challengeKey) == 0 {
		ctx.Response.SetStatusCode(StatusBadRequest)
		ctx.ResponseData = append(ctx.ResponseData, "websocket: not a websocket handshake: 'Sec-WebSocket-Key' header is missing or blank"...)
	}

	ctx.ResponseData = append(ctx.ResponseData, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "...)
	ctx.ResponseData = append(ctx.ResponseData, computeAcceptKey(challengeKey)...)
	ctx.ResponseData = append(ctx.ResponseData, "\r\n"...)
	ctx.ResponseData = append(ctx.ResponseData, "\r\n"...)

	ctx.wsHandler = h
	if wsPinghandler == nil {
		SetWSPingHandler(nil)
	}
	if wsCloseHandler == nil {
		SetWSCloseHandler(nil)
	}

	return nil
}

var wsPinghandler WSHandler
var wsCloseHandler WSHandler

func isValidReceivedCloseCode(code int) bool {
	return validReceivedCloseCodes[code] || (code >= 3000 && code <= 4999)
}

func FormatCloseMessage(closeCode int, data []byte) []byte {
	if closeCode == CloseNoStatusReceived {
		// Return empty message because it's illegal to send
		// CloseNoStatusReceived. Return non-nil value in case application
		// checks for nil.
		return []byte{}
	}
	buf := make([]byte, 2+len(data))
	binary.BigEndian.PutUint16(buf, uint16(closeCode))
	copy(buf[2:], data)
	return buf
}

func SetWSCloseHandler(h WSHandler) {
	if h == nil {
		h = func(ctx *BaseContext) error {
			message := FormatCloseMessage(ctx.WSMsg.CloseCode, nil)
			ctx.WSMsg.RspData = append(ctx.WSMsg.RspData, message...)
			return nil
		}
	}

	wsCloseHandler = h
}

func SetWSPingHandler(h WSHandler) {
	if h == nil {
		h = func(ctx *BaseContext) error {
			ctx.WSMsg.RspData = append(ctx.WSMsg.RspData, ctx.WSMsg.ReqData...)
			return nil
		}
	}

	wsPinghandler = h
}

// 字符串包含判断 大小写不敏感
func caseInsensitiveContain(a, b []byte) bool {
	lenA := len(a)
	lenB := len(b)
	if lenA < lenB {
		return false
	}

	i := 0
	j := 0
	lenC := lenA - lenB
	for ; i <= lenC; i++ {
		j = 0
		for ; j < lenB; j++ {
			if a[i+j]|0x20 != b[j]|0x20 {
				break
			}
		}

		if j == lenB {
			return true
		}
	}

	return false
}

var keyGUID = []byte("258EAFA5-E914-47DA-95CA-C5AB0DC85B11")

func computeAcceptKey(challengeKey []byte) []byte {
	h := sha1.New()
	h.Write(challengeKey)
	h.Write(keyGUID)
	src := h.Sum(nil)
	dst := make([]byte, base64.StdEncoding.EncodedLen(len(src)))
	base64.StdEncoding.Encode(dst, src)
	return dst
}

func (proc *AsyncHttpProcessor) parseWSReq(data []byte, wsMsg *WSMessage) (leftover []byte, err error) {

	// 客户端的请求至少有2个字节的请求头 4个字节的掩码
	// 客户端到服务端的请求必须掩码处理 服务端发送不需要
	if len(data) < 6 {
		return data, nil
	}

	// 第1个字节的前8位分别为 FIN RSV1 RSV2 RSV3 opcode(4)
	// 第2个字节的分别为 MASK PayloadLen(7)
	// 第3、4个字节可选 当PayloadLen=126时占用
	// 第5、6、7、8、9、10个字节可选 当PayloadLen=127时占用
	// 第10-13个字节可选 为maskingkey 设置了mask就有
	// 对于可选字节 没有的时候不占用 比如3-10没用的话 maskingkey会放在第3个字节
	final := data[0]&finalBit != 0
	frameType := int(data[0] & 0xf)
	if wsMsg.MessageType == 0 {
		wsMsg.MessageType = frameType
	}

	// RSV 不协商使用的话 都为0
	if rsv := data[0] & (rsv1Bit | rsv2Bit | rsv3Bit); rsv != 0 {
		return data, fmt.Errorf("unexpected reserved bits 0x" + strconv.FormatInt(int64(rsv), 16))
	}

	mask := data[1]&maskBit != 0
	if !mask {
		return data, errors.New("no mask flag from client")
	}

	payloadLen := int64(data[1] & 0x7f)

	// 请求长度识别
	iPos := 2
	switch payloadLen {
	case 126:
		if len(data) < 4 {
			return data, nil
		}
		p := data[2:4]
		payloadLen = int64(binary.BigEndian.Uint16(p))
		iPos = 4

	case 127:
		if len(data) < 10 {
			return data, nil
		}
		p := data[2:10]
		payloadLen = int64(binary.BigEndian.Uint64(p))
		iPos = 10
	}

	// 单次请求完整性判断
	// 4 frame masking
	if len(data) < (iPos + 4 + (int)(payloadLen)) {
		return data, nil
	}

	// 读取mask
	copy(wsMsg.maskKey[:], data[iPos:iPos+4])
	iPos += 4

	switch frameType {
	case CloseMessage, PingMessage, PongMessage:
		if payloadLen > maxControlFramePayloadSize {
			return data, errors.New("control frame length > 125")
		}
		if !final {
			return data, errors.New("control frame not final")
		}
	case TextMessage, BinaryMessage:
		if wsMsg.msContinue {
			return data, errors.New("message start before final message frame")
		}
		wsMsg.msContinue = !final
	case continuationFrame:
		if !wsMsg.msContinue {
			return data, errors.New("continuation after final message frame")
		}
		wsMsg.msContinue = !final
	default:
		return data, errors.New("unknown opcode " + strconv.Itoa(frameType))
	}

	if payloadLen > 0 {
		oldLen := len(wsMsg.ReqData)
		addLen := (int)(payloadLen)
		wsMsg.ReqData = append(wsMsg.ReqData, data[iPos:iPos+addLen]...)
		iPos += addLen
		maskBytes(wsMsg.maskKey, oldLen, wsMsg.ReqData)
	}

	leftover = data[iPos:]
	return leftover, nil
}

const wordSize = int(unsafe.Sizeof(uintptr(0)))

func maskBytes(key [4]byte, pos int, b []byte) int {
	// Mask one byte at a time for small buffers.
	if len(b) < 2*wordSize {
		for i := range b {
			b[i] ^= key[pos&3]
			pos++
		}
		return pos & 3
	}

	// Mask one byte at a time to word boundary.
	if n := int(uintptr(unsafe.Pointer(&b[0]))) % wordSize; n != 0 {
		n = wordSize - n
		for i := range b[:n] {
			b[i] ^= key[pos&3]
			pos++
		}
		b = b[n:]
	}

	// Create aligned word size key.
	var k [wordSize]byte
	for i := range k {
		k[i] = key[(pos+i)&3]
	}
	kw := *(*uintptr)(unsafe.Pointer(&k))

	// Mask one word at a time.
	n := (len(b) / wordSize) * wordSize
	for i := 0; i < n; i += wordSize {
		*(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(&b[0])) + uintptr(i))) ^= kw
	}

	// Mask one byte at a time for remaining bytes.
	b = b[n:]
	for i := range b {
		b[i] ^= key[pos&3]
		pos++
	}

	return pos & 3
}

// process websocket msg
func (proc *AsyncHttpProcessor) procWS(ctx *BaseContext, conn net.Conn) error {
	data := ctx.buffer
	for {
		if ctx.WSMsg.RspHeader == nil {
			ctx.WSMsg.RspHeader = make([]byte, 0, maxFrameHeaderSize)
		}
		wsMsg := ctx.WSMsg
		leftover, err := proc.parseWSReq(data, wsMsg)
		if err != nil {
			// 请求异常
			wsMsg.RspData = append(wsMsg.RspData, "bad request"...)
			proc.writeWSRspData(ctx, conn, wsMsg)
			break
		} else if len(leftover) == len(data) {
			//fmt.Println("frame incomplete ", len(leftover), len(wsMsg.ReqData), len(wsMsg.RspData))
			// 数据不完整
			break
		} else if wsMsg.msContinue {
			// 数据不全  有多帧
			//fmt.Println("data incomplete ", len(leftover), len(wsMsg.ReqData), len(wsMsg.RspData))
			data = leftover
			if len(data) == 0 {
				break
			}
			continue
		}

		//fmt.Println("rec data  ", len(wsMsg.ReqData), len(leftover), len(wsMsg.RspData))
		if isControl(wsMsg.MessageType) {
			// 处理控制帧
			ctx.handleWSControlFrame()
		} else {
			// 处理业务数据
			_ = ctx.wsHandler(ctx)
		}

		if len(wsMsg.RspData) > 0 {
			proc.writeWSRspData(ctx, conn, wsMsg)
		}

		data = leftover

		if ctx.CloseAfterWrite == Close {
			break
		}

		if len(data) == 0 {
			break
		}
	}

	checkEnd(&ctx.buffer, data)
	return nil
}

// 保留多余的数据下次处理 如果数据处理完了就清空处理缓存区
func checkEnd(buf *[]byte, data []byte) {
	if len(data) > 0 {
		if len(data) != len(*buf) { // 相等的话 数据一样不用再拷贝
			*buf = append((*buf)[:0], data...)
		}
	} else if len(*buf) > 0 {
		*buf = (*buf)[:0]
	}
}

func (proc *AsyncHttpProcessor) writeWSRspData(ctx *BaseContext, conn net.Conn, wsMsg *WSMessage) {

	if len(wsMsg.RspData) == 0 {
		return
	}

	wsMsg.RspHeader = wsMsg.RspHeader[:10] //服务端无掩码 10个就够了
	framePos := 2
	dataLen := len(wsMsg.RspData)
	wsMsg.RspHeader[0] = byte(wsMsg.MessageType)
	wsMsg.RspHeader[0] |= finalBit
	switch {
	case dataLen >= 65536:
		wsMsg.RspHeader[1] = 127
		binary.BigEndian.PutUint64(wsMsg.RspHeader[2:], uint64(dataLen))
		framePos += 8
	case dataLen > 125:
		wsMsg.RspHeader[1] = 126
		binary.BigEndian.PutUint16(wsMsg.RspHeader[2:], uint16(dataLen))
		framePos += 2
		wsMsg.RspHeader = wsMsg.RspHeader[:4]
	default:
		wsMsg.RspHeader[1] = byte(dataLen)
		wsMsg.RspHeader = wsMsg.RspHeader[:2]
	}

	proc.watcher.Write(ctx, conn, append(wsMsg.RspHeader, wsMsg.RspData...))
	wsMsg.Reset()
}

func (wsMsg *WSMessage) Reset() {
	wsMsg.msContinue = false
	wsMsg.MessageType = 0
	wsMsg.ReqData = wsMsg.ReqData[:0]
	wsMsg.RspData = wsMsg.RspData[:0]
}

func isControl(frameType int) bool {
	return frameType == CloseMessage || frameType == PingMessage || frameType == PongMessage
}

// 业务回调函数处理 参考自go http
const (
	None     int = iota
	Close        // Close closes the connection.
	Shutdown     // Shutdown shutdowns the server.
)

func (ctx *BaseContext) handleWSControlFrame() {
	wsMsg := ctx.WSMsg
	switch wsMsg.MessageType {
	case PingMessage:
		_ = wsPinghandler(ctx)
		wsMsg.MessageType = PongMessage
	case CloseMessage:
		wsMsg.CloseCode = CloseNoStatusReceived
		if len(wsMsg.ReqData) >= 2 {
			wsMsg.CloseCode = int(binary.BigEndian.Uint16(wsMsg.ReqData))
		}
		_ = wsCloseHandler(ctx)
		ctx.CloseAfterWrite = Close
	}
	return
}
