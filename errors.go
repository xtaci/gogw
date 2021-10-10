package gogw

import "errors"

var (
	ErrRequestHandlerEmpty = errors.New("empty request handler")
	ErrWatcherBufSize      = errors.New("swap buffer size must be positive")
	ErrRequestLimit        = errors.New("the request has limited RPS")
	ErrRequestHeaderSize   = errors.New("the request has big header")
	ErrRequestBodySize     = errors.New("the request has big body")
)
