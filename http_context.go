package aiohttp

import (
	"bytes"
	"net/textproto"
)

type AIOHttpContext struct {
	state int
	buf   *bytes.Buffer
	tp    *textproto.Reader
}
