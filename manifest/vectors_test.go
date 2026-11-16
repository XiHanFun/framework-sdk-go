package manifest_test

import (
	"reflect"
	"testing"

	"github.com/xihanfun/framework-spec/conformance"

	"github.com/xihanfun/framework-sdk-go/manifest"
)

// TestManifestsFollowTheVectors 回放 manifests.json：合法的清单读得出，写出后再读一遍得到同一份清单；不合法的清单被拒绝
// （RFC-0002 解析规则 2，ADR-0233、ADR-0235）
func TestManifestsFollowTheVectors(t *testing.T) {
	vectors, err := conformance.Manifests()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range vectors.Valid {
		read, err := manifest.Read(manifestText(c))
		if err != nil {
			t.Errorf("%s must be read: %v", c.Name, err)
			continue
		}
		written, err := manifest.Write(read)
		if err != nil {
			t.Errorf("%s must be written: %v", c.Name, err)
			continue
		}
		again, err := manifest.Read(written)
		if err != nil || !reflect.DeepEqual(read, again) {
			t.Errorf("%s: reading the written manifest gives %+v (%v), want %+v", c.Name, again, err, read)
		}
	}
	for _, c := range vectors.Invalid {
		if _, err := manifest.Read(manifestText(c)); err == nil {
			t.Errorf("%s must be rejected: %s", c.Name, c.Reason)
		}
	}
}

// manifestText 返回清单的原文：表达重复成员的样例用 text，其余用 manifest 的 JSON 值
func manifestText(c conformance.ManifestCase) []byte {
	if c.Text != "" {
		return []byte(c.Text)
	}
	return c.Manifest
}
