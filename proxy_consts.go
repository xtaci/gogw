package gogw

import (
	"errors"
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
)

var (
	ErrProxyConnect = errors.New("cannot connect to remote service")
)

func proxyErrResponse(err error) []byte {
	errString := fmt.Sprint(err)
	str := fmt.Sprintf(proxyResponseTemplate, time.Now(), len(errString), errString)
	return []byte(str)
}
