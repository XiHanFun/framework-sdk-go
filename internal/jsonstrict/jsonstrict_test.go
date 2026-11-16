package jsonstrict_test

import (
	"testing"

	"github.com/xihanfun/framework-sdk-go/internal/jsonstrict"
)

func TestDuplicateMember(t *testing.T) {
	duplicates := map[string]string{
		`{"ok":true,"ok":false}`:                             "ok",
		`{"a":1,"b":{"c":1,"c":2}}`:                          "c",
		`{"a":[{"x":1},{"y":1,"y":2}]}`:                      "y",
		`{"a":1,"a":2}`:                                      "a",
		`[{"k":1,"k":2}]`:                                    "k",
		`{"inner":{"code":"A","message":"a","message":"b"}}`: "message",
	}
	for data, want := range duplicates {
		name, found, err := jsonstrict.DuplicateMember([]byte(data))
		if err != nil || !found || name != want {
			t.Errorf("%s: %q %v %v, want %q", data, name, found, err, want)
		}
	}
	for _, data := range []string{`{}`, `[]`, `1`, `null`, `{"a":1,"b":2}`, `{"a":{"a":{"a":1}}}`, `[{"k":1},{"k":2}]`, `{"a":[],"b":{},"c":[1,{"a":1}]}`} {
		if name, found, err := jsonstrict.DuplicateMember([]byte(data)); err != nil || found {
			t.Errorf("%s: %q %v %v", data, name, found, err)
		}
	}
	if _, _, err := jsonstrict.DuplicateMember([]byte(`{"a":`)); err == nil {
		t.Error("invalid JSON reports the parse error")
	}
}
