package manifest_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/xihanfun/framework-spec/conformance"

	"github.com/xihanfun/framework-sdk-go/manifest"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestManifestsFollowTheVectors 回放 manifests.json（RFC-0002 解析规则 2 与「写出」，ADR-0233、ADR-0235、ADR-0236）：合法的清单读得出，
// 写出的全文与 written 逐字相等，再读再写得到同一份文本；不合法的清单的错误码、消息键与参数与 error 逐字相等
func TestManifestsFollowTheVectors(t *testing.T) {
	vectors, err := conformance.Manifests()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range vectors.Valid {
		read, err := manifest.Read(manifestText(t, c))
		if err != nil {
			t.Errorf("%s must be read: %v", c.Name, err)
			continue
		}
		written, err := manifest.Write(read)
		if err != nil {
			t.Errorf("%s must be written: %v", c.Name, err)
			continue
		}
		if c.Written == "" || string(written) != c.Written {
			t.Errorf("%s: written\n%s\nwant\n%s", c.Name, written, c.Written)
		}
		again, err := manifest.Read(written)
		if err != nil {
			t.Errorf("%s: the written manifest must be read again: %v", c.Name, err)
			continue
		}
		if rewritten, _ := manifest.Write(again); !bytes.Equal(rewritten, written) {
			t.Errorf("%s: writing the manifest read again gives %q, want %q", c.Name, rewritten, written)
		}
	}
	for _, c := range vectors.Invalid {
		if c.Error == nil {
			t.Fatalf("%s: the vector gives no error", c.Name)
		}
		_, err := manifest.Read(manifestText(t, c))
		var failure *primitives.Error
		if !errors.As(err, &failure) {
			t.Errorf("%s: must be rejected with %s, got %v", c.Name, c.Error.MessageKey, err)
			continue
		}
		if failure.Code != c.Error.Code || failure.MessageKey != c.Error.MessageKey || !sameArguments(failure.Details, c.Error.Arguments) {
			t.Errorf("%s: %s %s %v, want %s %s %v", c.Name, failure.Code, failure.MessageKey, failure.Details, c.Error.Code, c.Error.MessageKey, c.Error.Arguments)
		}
	}
}

// manifestText 返回清单的原文：给了 text（可以是空串）时用它，否则用 manifest 的 JSON 值的紧凑写法，成员顺序照写
func manifestText(t *testing.T, c conformance.ManifestCase) []byte {
	t.Helper()
	if c.Text != nil {
		return []byte(*c.Text)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, c.Manifest); err != nil {
		t.Fatalf("%s: %v", c.Name, err)
	}
	return compact.Bytes()
}

// sameArguments 比较错误的参数与向量的参数：名字相同，值的文本逐字相等
func sameArguments(details map[string]any, arguments map[string]string) bool {
	if len(details) != len(arguments) {
		return false
	}
	for name, want := range arguments {
		if got, ok := details[name].(string); !ok || got != want {
			return false
		}
	}
	return true
}
