package aiohttp

import (
	"bufio"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
)

type regExpRule struct {
	regexp *regexp.Regexp
	limits int32
	tokens int32

	lastTest time.Time // last test time
}

// RegexLimiter wraps regexLimiter for gc
type RegexLimiter struct {
	rules []regExpRule
}

func (reg *RegexLimiter) Test(uri *URI) bool {
	for k := range reg.rules {
		if reg.rules[k].regexp.Match(uri.Path()) {
			// token re-approve
			if time.Since(reg.rules[k].lastTest) > time.Second {
				reg.rules[k].tokens = reg.rules[k].limits
			}

			if reg.rules[k].tokens > 0 {
				reg.rules[k].tokens--
				return true
			}
			return false
		}
	}
	return true
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
	regexLimiter := new(RegexLimiter)

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
			regexLimiter.rules = append(regexLimiter.rules, regExpRule{rexp, int32(limit), int32(limit), time.Now()})
		}
	}

	return regexLimiter, nil
}
