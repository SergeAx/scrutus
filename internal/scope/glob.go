package scope

import (
	"regexp"
	"strings"
	"sync"
)

var (
	globCacheMu sync.Mutex
	globCache   = map[string]*regexp.Regexp{}
)

// MatchGlob matches slash-separated paths, with ** spanning separators the way
// .gitignore and every config file in the wild expect.
func MatchGlob(pattern, path string) bool {
	re, err := compileGlob(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(strings.TrimPrefix(filepathSlash(path), "./"))
}

func compileGlob(pattern string) (*regexp.Regexp, error) {
	globCacheMu.Lock()
	defer globCacheMu.Unlock()
	if re, ok := globCache[pattern]; ok {
		return re, nil
	}

	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
					continue
				}
				b.WriteString(".*")
				continue
			}
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		case '/':
			b.WriteString("/")
		default:
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	b.WriteString("$")

	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, err
	}
	globCache[pattern] = re
	return re, nil
}

func filepathSlash(path string) string { return strings.ReplaceAll(path, "\\", "/") }
