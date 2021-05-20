package aiohttp

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestLimiterParser(t *testing.T) {
	testString := `
/abc
10

/a/.*/b

100
`

	reader := strings.NewReader(testString)

	limiter, err := ParseRegexLimiter(reader)
	assert.Nil(t, err)

	for k := range limiter.rules {
		t.Logf("regexp:%v limits:%v tokens:%v", limiter.rules[k].regexp.String(), limiter.rules[k].limits, limiter.rules[k].tokens)
	}

	var uri URI
	err = uri.Parse(nil, []byte("/abc/"))
	assert.Nil(t, err)

	assert.True(t, limiter.Test(&uri))
	assert.Equal(t, int32(9), limiter.rules[0].tokens)

	// token refresh
	<-time.After(time.Second)
	assert.True(t, limiter.Test(&uri))
	assert.Equal(t, int32(9), limiter.rules[0].tokens)
}
