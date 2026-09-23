package extract_test

import (
	"strings"
	"testing"

	"github.com/SergeAx/scrutus/internal/core"
	"github.com/SergeAx/scrutus/internal/extract"
)

func TestContextCentresOnCodeStartingMidLine(t *testing.T) {
	src := strings.Repeat("above,\n", 30) + "first, second,\n" + strings.Repeat("below,\n", 30)
	at := strings.Index(src, "second")
	code := core.Span{Start: at, End: at + len("second")}

	got := extract.Context([]byte(src), core.Span{End: len(src)}, code, nil, 20)
	if !strings.Contains(got, "first, >>> CODE\nsecond\n<<< CODE") {
		t.Errorf("context lost the code:\n%s", got)
	}
}
