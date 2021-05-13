package aiohttp

import (
	"bufio"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

type regExpRule struct {
	regexp regexp.Regexp
	limits int
}

type RegexLimiter []regExpRule

func (reg *RegexLimiter) Test(*AIOHttpContext) bool {
	return false
}

// load a regex based limiter from config
// file format:
// regex matching: request per second
func LoadRegexLimiter(path string) (*IRequestLimiter, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	regexLimiter := new(RegexLimiter)

	// read rules line by line
	fileReader := bufio.NewReader(file)
	var lineNum int
	for {
		line, err := fileReader.ReadString("\n")
		if err != nil {
			break
		}

		// split by ":"
		parts := strings.Split(line, ":")
		if len(parts) != 2 {
			return nil, errors.Errorf("regexLimiter format error:line %d data:%v", lineNum, line)
		}

		// parse pattern matching
		regexp, err := regexp.Compile(parts[0])
		if err != nil {
			return nil, errors.Errorf("regexLimiter cannot parse regexp:line %d, data:%v, error: %v", lineNum, parts[0], err)
		}

		// parse limit
		limit, err := strconv.ParseUint(parts[1], 0, 64)
		if err != nil {
			return nil, errors.Errorf("regexLimiter cannot parse limit number:line %d, data:%v, error: %v", lineNum, parts[1], err)
		}

		// add rules
		regexLimiter = append(regexLimiter, regExpRule{regexp, limit})
	}

	return regexLimiter, nil
}
