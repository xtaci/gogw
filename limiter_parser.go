package aiohttp

import (
	"bufio"
	"io"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
)

type regExpRule struct {
	regexp *regexp.Regexp
	limits int32
	tokens int32
}

// RegexLimiter wraps regexLimiter for gc
type regexLimiter struct {
	rules    []regExpRule
	chClosed chan struct{}
}

// periodically add tokens to rules
func (reg *regexLimiter) tokenApprover() {
	ticker := time.NewTicker(time.Second)
	for {
		select {
		case <-ticker.C:
			for k := range reg.rules {
				atomic.StoreInt32(&reg.rules[k].tokens, reg.rules[k].limits)
			}
		case <-reg.chClosed:
			return
		}
	}
}

func (reg *regexLimiter) Test(uri *URI) bool {
	for k := range reg.rules {
		if reg.rules[k].regexp.Match(uri.Path()) {
			if atomic.AddInt32(&reg.rules[k].tokens, -1) < 0 {
				return false
			} else {
				return true
			}
		}
	}
	return true
}

func (reg *regexLimiter) Close() {
	close(reg.chClosed)
}

// RegexLimiter wraps regexLimiter for gc
type RegexLimiter struct {
	*regexLimiter
}

// load a regex based limiter from config
// file format:
// 1 regex matching
// 2 request per second
// 3 regex matching
// 4 request per second
func LoadRegexLimiter(path string) (*RegexLimiter, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	fileReader := bufio.NewReader(file)
	return parseRegexLimiter(fileReader)
}

func parseRegexLimiter(reader *bufio.Reader) (*RegexLimiter, error) {
	regexLimiter := new(regexLimiter)
	regexLimiter.chClosed = make(chan struct{})

	var lineNum int

	// start by reading regex line
	readRegex := true

	var rexp *regexp.Regexp
	var limit int64

	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, err
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if readRegex {
			// parse pattern matching
			rexp, err = regexp.Compile(line)
			if err != nil {
				return nil, errors.Errorf("regexLimiter cannot parse regexp:line %d, data:%v, error: %v", lineNum, line, err)
			}
			readRegex = false
		} else {
			// parse limit
			limit, err = strconv.ParseInt(line, 0, 32)
			if err != nil {
				return nil, errors.Errorf("regexLimiter cannot parse limit number:line %d, data:%v, error: %v", lineNum, line, err)
			}
			readRegex = true

			// add rules
			regexLimiter.rules = append(regexLimiter.rules, regExpRule{rexp, int32(limit), int32(limit)})
		}
	}

	wrapper := &RegexLimiter{regexLimiter}
	runtime.SetFinalizer(wrapper, func(wrapper *RegexLimiter) {
		wrapper.Close()
	})

	// spin up the token control goroutine
	go regexLimiter.tokenApprover()

	return &RegexLimiter{regexLimiter}, nil
}
