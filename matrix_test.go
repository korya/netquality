package netquality

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestMatrix fails when docs/test-matrix.md names a test that does not exist,
// or when a test function in the repo is missing from the matrix.
func TestMatrix(t *testing.T) {
	f, err := os.Open(filepath.Join("docs", "test-matrix.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	rows := map[string]map[string]bool{} // package -> test names
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		cells := strings.Split(strings.Trim(sc.Text(), "| "), "|")
		if len(cells) != 4 {
			continue
		}
		pkg, name := strings.TrimSpace(cells[2]), strings.TrimSpace(cells[3])
		if pkg == "Package" || (!strings.HasPrefix(name, "Test") && name != "Example") {
			continue
		}
		if rows[pkg] == nil {
			rows[pkg] = map[string]bool{}
		}
		rows[pkg][name] = true
	}
	funcRE := regexp.MustCompile(`(?m)^func (Test\w+|Example\w*)\(`)
	for pkg, names := range rows {
		files, err := filepath.Glob(filepath.Join(pkg, "*_test.go"))
		if err != nil || len(files) == 0 {
			t.Errorf("package %q has no test files", pkg)
			continue
		}
		defined := map[string]bool{}
		for _, file := range files {
			src, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range funcRE.FindAllStringSubmatch(string(src), -1) {
				defined[m[1]] = true
			}
		}
		for name := range names {
			if !defined[name] {
				t.Errorf("matrix names %s.%s but no such test exists", pkg, name)
			}
		}
		for name := range defined {
			if name == "TestMatrix" {
				continue
			}
			if !names[name] {
				t.Errorf("%s.%s exists but is not in docs/test-matrix.md", pkg, name)
			}
		}
	}
}
