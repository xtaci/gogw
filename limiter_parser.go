package aiohttp

import (
	"bufio"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

type regExpRule struct {
	regexp *regexp.Regexp
	limits uint64
}

type RegexLimiter []regExpRule

func (reg RegexLimiter) Test(ctx *AIOHttpContext) bool {
	var uri URI // current incoming request's URL
	err := uri.Parse(nil, ctx.Header.RequestURI())
	if err != nil {
		return false
	}

	for k := range reg {
		if reg[k].regexp.Match(uri.Path()) {
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
func LoadRegexLimiter(path string) (RegexLimiter, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	fileReader := bufio.NewReader(file)
	return parseRegexLimiter(fileReader)
}

func parseRegexLimiter(reader *bufio.Reader) (RegexLimiter, error) {
	var regexLimiter RegexLimiter

	var lineNum int

	// start by reading regex line
	readRegex := true

	var rexp *regexp.Regexp
	var limit uint64

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
			limit, err = strconv.ParseUint(line, 0, 64)
			if err != nil {
				return nil, errors.Errorf("regexLimiter cannot parse limit number:line %d, data:%v, error: %v", lineNum, line, err)
			}
			readRegex = true

			// add rules
			regexLimiter = append(regexLimiter, regExpRule{rexp, limit})
		}
	}

	return regexLimiter, nil
}
