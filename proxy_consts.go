package aiohttp

import (
	"fmt"
	"time"
)

const (
	proxyResponseTemplate = `HTTP/1.1 400 Bad Request
Date: %v
Content-Length: %v
Content-Type: text/html
Connection: Closed

%v
`
	proxyRemoteDisconnected  = "remote disconnected"
	proxyRemoteTimeout       = "remote timeout"
	proxyRemoteInternalError = "remote internal error"
)

func proxyErrResponse(format string, err error) []byte {
	errString := fmt.Sprint(err)
	str := fmt.Sprintf(format, time.Now(), len(errString), errString)
	return []byte(str)
}
