package netquality

import (
	"reflect"
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
// field, or when the engine grows one the public type never surfaces: a number
// measured but never reported. Fields are matched by name, not position, so
// reordering either declaration is free.
func TestLatencyStatsConvertsEveryField(t *testing.T) {
	var seed int64
	var src engine.LatencyStats
	fill(t, reflect.ValueOf(&src).Elem(), &seed)

	compare(t, "", reflect.ValueOf(latencyStatsFrom(src)), reflect.ValueOf(src))
}

// compare asserts that every field of the engine value src has a public
// counterpart of the same name carrying the same value, recursing through the
// pointers to stage medians.
func compare(t *testing.T, prefix string, pub, src reflect.Value) {
	t.Helper()
	for i := 0; i < src.NumField(); i++ {
		name := src.Type().Field(i).Name
		got := pub.FieldByName(name)
		if !got.IsValid() {
			t.Errorf("%s%s: the engine has this field and the public type does not", prefix, name)
			continue
		}
		want := src.Field(i)
		if want.Kind() == reflect.Pointer {
			if got.IsNil() != want.IsNil() {
				t.Errorf("%s%s: got nil=%v, want nil=%v", prefix, name, got.IsNil(), want.IsNil())
				continue
			}
			if !want.IsNil() {
				compare(t, prefix+name+".", got.Elem(), want.Elem())
			}
			continue
		}
		if got.Interface() != want.Interface() {
			t.Errorf("%s%s: got %v, want %v", prefix, name, got, want)
		}
	}
}
