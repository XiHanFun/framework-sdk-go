package manifest_test

import (
	"bytes"
	"testing"

	"github.com/xihanfun/framework-spec/conformance"

	"github.com/xihanfun/framework-sdk-go/manifest"
)

// TestManifestsFollowTheVectors 回放 manifests.json：合法的清单读得出，写出后再读再写得到同一份文本；不合法的清单被拒绝
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
		// 写出的文本含缺省值（role、activation、空列表），再读再写得到同一份文本
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
