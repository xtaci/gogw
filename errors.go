package aiohttp

import "errors"

var (
	ErrRequestHandlerEmpty = errors.New("empty request handler")
	ErrWatcherBufSize      = errors.New("swap buffer size must be positive")
)
