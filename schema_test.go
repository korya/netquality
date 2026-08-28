package netquality

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// jsonPaths enumerates every JSON path a type can produce, recursively,
// from struct tags — the wire schema, independent of any particular value.
func jsonPaths(t reflect.Type, prefix string, out map[string]string) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		if t.Kind() == reflect.Slice {
			prefix += "[]"
		}
		t = t.Elem()
	}
	if t == reflect.TypeOf(time.Time{}) {
		out[prefix] = "time"
		return
	}
	if t.Kind() != reflect.Struct {
		out[prefix] = t.Kind().String()
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		p := name
		if prefix != "" {
			p = prefix + "." + name
		}
		jsonPaths(f.Type, p, out)
	}
}

// TestResultSchemaGolden pins the JSON contract (INV-7): every field path and
// its kind. Adding, renaming or removing a field fails until the fixture is
// regenerated on purpose with UPDATE_GOLDEN=1 — the diff is the review.
func TestResultSchemaGolden(t *testing.T) {
	paths := map[string]string{}
	jsonPaths(reflect.TypeOf(Result{}), "", paths)
	keys := make([]string, 0, len(paths))
	for k := range paths {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + " " + paths[k] + "\n")
	}
	golden := filepath.Join("testdata", "result_schema.txt")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d paths)", golden, len(keys))
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v — run UPDATE_GOLDEN=1 go test -run TestResultSchemaGolden .", err)
	}
	// Windows checkouts may carry CRLF; the fixture is LF.
	if strings.ReplaceAll(string(want), "\r\n", "\n") != b.String() {
		t.Errorf("Result JSON schema changed; if intended, bump docs and run UPDATE_GOLDEN=1.\n--- fixture\n%s--- now\n%s", want, b.String())
	}
	for _, k := range keys {
		last := k[strings.LastIndex(k, ".")+1:]
		if strings.ToLower(last) != last || strings.Contains(last, "-") {
			t.Errorf("%s is not snake_case", k)
		}
	}
}

// TestStoredResultDocumentParses guards readers of stored results: a
// document at the current schema version must unmarshal with its values
// intact, and the version must be readable before anything else.
func TestStoredResultDocumentParses(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "result_schema1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil || probe.SchemaVersion != ResultSchemaVersion {
		t.Fatalf("schema_version probe: %d %v", probe.SchemaVersion, err)
	}
	var res Result
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("stored document no longer parses: %v", err)
	}
	if res.Idle.P80 != 0 || res.Idle.P95 != 0 || res.Download.Loaded.Foreign.P95 == 0 {
		t.Errorf("percentile presence: idle(2 samples)=%+v foreign(182)=%+v", res.Idle, res.Download.Loaded.Foreign)
	}
	if res.SchemaVersion != 1 || res.Target.Host != "h3.speed.cloudflare.com" || res.Download == nil || res.Download.RPM == 0 ||
		res.Download.Reason != ReasonBytesCap || res.Idle == nil || res.Idle.Stages == nil || len(res.Target.LocalIPs) != 1 {
		t.Errorf("values lost: %+v", res)
	}
	// Unknown fields from the future must be ignored, not fatal.
	var future Result
	if err := json.Unmarshal([]byte(`{"schema_version":99,"target":{"host":"x","new_thing":true},"download":{"direction":"download","rpm":1,"extra":{}}}`), &future); err != nil {
		t.Errorf("unknown fields must be ignored: %v", err)
	}
}
