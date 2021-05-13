package aiohttp

import (
	"bufio"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLimiterParser(t *testing.T) {
	testString := `
/abc
10

/a/.*/b

100
`

	reader := bufio.NewReader(strings.NewReader(testString))

	limiter, err := parseRegexLimiter(reader)
	assert.Nil(t, err)

	for k := range limiter {
		t.Logf("%v %v", limiter[k].regexp.String(), limiter[k].limits)
	}
}
