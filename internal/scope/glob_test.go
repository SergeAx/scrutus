package scope

import "testing"

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"vendor/**", "vendor/foo/bar.go", true},
		{"vendor/**", "vendors/foo.go", false},
		{"**/*_test.go", "pkg/deep/run_test.go", true},
		{"**/*_test.go", "run_test.go", true},
		{"**/*_test.go", "run.go", false},
		{"**/generated/**", "internal/generated/api.go", true},
		{"*.go", "internal/a.go", false},
		{"internal/*.go", "internal/a.go", true},
		{"internal/*.go", "internal/sub/a.go", false},
		{"testdata/**", "testdata\\sample\\payments.go", true},
	}
	for _, tc := range cases {
		if got := MatchGlob(tc.pattern, tc.path); got != tc.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestIntersects(t *testing.T) {
	ranges := []LineRange{{Start: 10, End: 12}, {Start: 40, End: 40}}

	cases := []struct {
		start, end int
		want       bool
	}{
		{1, 5, false},
		{9, 10, true},
		{12, 20, true},
		{13, 39, false},
		{40, 41, true},
	}
	for _, tc := range cases {
		if got := Intersects(ranges, tc.start, tc.end); got != tc.want {
			t.Errorf("Intersects(%d-%d) = %v, want %v", tc.start, tc.end, got, tc.want)
		}
	}
	if !Intersects(nil, 1, 1) {
		t.Error("an unscoped file must include every comment")
	}
}

func TestParseHunks(t *testing.T) {
	diff := `diff --git a/pkg/a.go b/pkg/a.go
--- a/pkg/a.go
+++ b/pkg/a.go
@@ -3,0 +4,2 @@ func a() {
+	// added
+	x := 1
@@ -20 +22 @@ func b() {
+	y := 2
`
	got := parseHunks(diff)
	ranges := got["pkg/a.go"]
	if len(ranges) != 2 {
		t.Fatalf("ranges = %+v, want two hunks", ranges)
	}
	if ranges[0] != (LineRange{Start: 4, End: 5}) {
		t.Errorf("first hunk = %+v, want 4-5", ranges[0])
	}
	if ranges[1] != (LineRange{Start: 22, End: 22}) {
		t.Errorf("second hunk = %+v, want 22-22", ranges[1])
	}
}
