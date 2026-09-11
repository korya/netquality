package netquality

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/korya/netquality/internal/engine"
)

// fill sets every field of the struct behind v to a distinct non-zero value,
// so a converter that forgets a field leaves a zero behind and is caught.
func fill(t *testing.T, v reflect.Value, seed *int64) {
	t.Helper()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		*seed++
		switch f.Kind() {
		case reflect.Int, reflect.Int64:
			f.SetInt(*seed)
		case reflect.Float64:
			f.SetFloat(float64(*seed) / 8)
		case reflect.String:
			f.SetString(strings.Repeat("x", int(*seed)))
		case reflect.Pointer:
			p := reflect.New(f.Type().Elem())
			fill(t, p.Elem(), seed)
			f.Set(p)
		default:
			t.Fatalf("fill: unhandled kind %s for field %s", f.Kind(), v.Type().Field(i).Name)
		}
	}
}

// TestStabilityParamsConvertsEveryField fails when toEngine or stabilityFrom
// forgets a field: a parameter the caller set would be silently dropped on the
// way into the engine.
func TestStabilityParamsConvertsEveryField(t *testing.T) {
	var seed int64
	var want engine.StabilityParams
	fill(t, reflect.ValueOf(&want).Elem(), &seed)

	if got := stabilityFrom(want).toEngine(); got != want {
		t.Errorf("round trip dropped a field:\n got %+v\nwant %+v", got, want)
	}
}

// TestLatencyStatsConvertsEveryField fails when latencyStatsFrom forgets a
// field, which would drop a number from the public Result.
func TestLatencyStatsConvertsEveryField(t *testing.T) {
	var seed int64
	var src engine.LatencyStats
	fill(t, reflect.ValueOf(&src).Elem(), &seed)

	got := latencyStatsFrom(src)
	gv, sv := reflect.ValueOf(got), reflect.ValueOf(src)
	for i := 0; i < sv.NumField(); i++ {
		name := sv.Type().Field(i).Name
		if name == "Stages" {
			continue // compared below: the pointed-to types differ by package
		}
		if !reflect.DeepEqual(gv.Field(i).Interface(), sv.Field(i).Interface()) {
			t.Errorf("%s: got %v, want %v", name, gv.Field(i), sv.Field(i))
		}
	}
	if got.Stages == nil {
		t.Fatal("Stages: got nil, want the converted stage medians")
	}
	sg, ss := reflect.ValueOf(*got.Stages), reflect.ValueOf(*src.Stages)
	for i := 0; i < ss.NumField(); i++ {
		if sg.Field(i).Interface() != ss.Field(i).Interface() {
			t.Errorf("Stages.%s: got %v, want %v", ss.Type().Field(i).Name, sg.Field(i), ss.Field(i))
		}
	}
}

// TestPublicTypesMirrorEngineTypes fails when a field is added, renamed or
// retyped on one side of the seam only. The public types are declared in this
// package rather than aliased so that pkg.go.dev documents them, which means
// nothing but this test keeps the two definitions in step.
func TestPublicTypesMirrorEngineTypes(t *testing.T) {
	for _, tc := range []struct{ pub, eng reflect.Type }{
		{reflect.TypeOf(StabilityParams{}), reflect.TypeOf(engine.StabilityParams{})},
		{reflect.TypeOf(LatencyStats{}), reflect.TypeOf(engine.LatencyStats{})},
		{reflect.TypeOf(StageMedians{}), reflect.TypeOf(engine.StageMedians{})},
	} {
		t.Run(tc.pub.Name(), func(t *testing.T) {
			if tc.pub.NumField() != tc.eng.NumField() {
				t.Fatalf("field count: public %d, engine %d", tc.pub.NumField(), tc.eng.NumField())
			}
			for i := 0; i < tc.pub.NumField(); i++ {
				p, e := tc.pub.Field(i), tc.eng.Field(i)
				if p.Name != e.Name {
					t.Errorf("field %d: public %q, engine %q", i, p.Name, e.Name)
					continue
				}
				// Types are compared with package qualifiers stripped,
				// because *netquality.StageMedians and *engine.StageMedians
				// are the same shape declared in two packages.
				if pt, et := unqualified(p.Type.String()), unqualified(e.Type.String()); pt != et {
					t.Errorf("%s: public %s, engine %s", p.Name, pt, et)
				}
				if p.Tag != e.Tag {
					t.Errorf("%s tag: public %q, engine %q", p.Name, p.Tag, e.Tag)
				}
			}
		})
	}
}

// unqualified drops package qualifiers so that two declarations of the same
// shape compare equal regardless of which package they were reached through.
func unqualified(typeName string) string {
	return qualifier.ReplaceAllString(typeName, "")
}

var qualifier = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*\.`)
