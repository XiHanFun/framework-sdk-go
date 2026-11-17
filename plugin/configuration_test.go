package plugin

import (
	"encoding/json"
	"testing"
)

type nestedSettings struct {
	Enabled bool    `json:"enabled"`
	Ratio   float64 `json:"ratio"`
}

type baseSettings struct {
	Name string `json:"name"`
}

type decodeSettings struct {
	baseSettings
	MaxEntries int            `json:"maxEntries"`
	Timeout    int64          `json:"timeoutMs"`
	Port       uint16         `json:"port"`
	Verbose    *bool          `json:"verbose"`
	Nested     nestedSettings `json:"nested"`
	Weights    []float64      `json:"weights"`
	Limits     map[string]int `json:"limits"`
	Ignored    string         `json:"-"`
	Code       string         `json:"code"`
}

func TestConfigurationDecodeCoercesStringLeaves(t *testing.T) {
	section := `{"Name":"cache","MaxEntries":"100000","timeoutMs":"2500","Port":"8080","Verbose":"True","Nested":{"Enabled":"false","Ratio":"0.5"},"Weights":["1","2.5"],"Limits":{"a":"1","b":"2"},"Code":"007"}`
	var settings decodeSettings
	if err := (Configuration{Section: json.RawMessage(section)}).Decode(&settings); err != nil {
		t.Fatal(err)
	}
	if settings.Name != "cache" || settings.MaxEntries != 100000 || settings.Timeout != 2500 || settings.Port != 8080 || settings.Verbose == nil || !*settings.Verbose {
		t.Errorf("decoded %+v", settings)
	}
	if settings.Nested.Enabled || settings.Nested.Ratio != 0.5 || len(settings.Weights) != 2 || settings.Weights[1] != 2.5 || settings.Limits["b"] != 2 {
		t.Errorf("nested %+v", settings)
	}
	if settings.Code != "007" {
		t.Error("string fields keep their text even when numeric")
	}
}

func TestConfigurationDecodeAcceptsNativeJSONAndEmptySections(t *testing.T) {
	var settings decodeSettings
	if err := (Configuration{Section: json.RawMessage(`{"maxEntries":5,"nested":{"enabled":true}}`)}).Decode(&settings); err != nil || settings.MaxEntries != 5 || !settings.Nested.Enabled {
		t.Errorf("native JSON: %v %+v", err, settings)
	}
	var empty decodeSettings
	if err := (Configuration{}).Decode(&empty); err != nil || empty.MaxEntries != 0 {
		t.Errorf("empty: %v", err)
	}
	if err := (Configuration{Section: json.RawMessage(`{"maxEntries":"lots"}`)}).Decode(&settings); err == nil {
		t.Error("a string that is not a number is still an error")
	}
	if err := (Configuration{Section: json.RawMessage(`{}`)}).Decode(nil); err == nil {
		t.Error("nil target")
	}
	var value decodeSettings
	if err := (Configuration{Section: json.RawMessage(`{}`)}).Decode(value); err == nil {
		t.Error("non-pointer target")
	}
}

// TestConfigurationDecodeFollowsTheTypedReadingRules 核对 lifecycle.md §7.2 的类型化读取：成员名按 ASCII 不区分大小写（Unicode 的大小写折叠不算），
// 字符串叶子读成数字时按 JSON 数字的文法，读成布尔时只认 true、false（ASCII 不区分大小写）。
func TestConfigurationDecodeFollowsTheTypedReadingRules(t *testing.T) {
	decode := func(section string) (decodeSettings, error) {
		var settings decodeSettings
		err := (Configuration{Section: json.RawMessage(section)}).Decode(&settings)
		return settings, err
	}
	if settings, err := decode(`{"MAXENTRIES":"7","NAME":"a","nested":{"RATIO":"1e-1"}}`); err != nil || settings.MaxEntries != 7 || settings.Name != "a" || settings.Nested.Ratio != 0.1 {
		t.Errorf("ASCII case-insensitive names: %v %+v", err, settings)
	}
	// U+212A（开尔文符号）按 Unicode 折叠是 k，按 ASCII 不是：不匹配 code
	if settings, err := decode("{\"\u212Aode\":\"x\",\"code\":\"y\"}"); err != nil || settings.Code != "y" {
		t.Errorf("a non-ASCII fold must not match a field: %v %+v", err, settings)
	}
	if settings, err := decode("{\"\u212Aode\":\"x\"}"); err != nil || settings.Code != "" {
		t.Errorf("a non-ASCII fold must not match a field: %v %+v", err, settings)
	}
	// 大小写不同的同名成员按文本顺序，后出现的为准
	if settings, err := decode(`{"maxEntries":"1","MaxEntries":"2"}`); err != nil || settings.MaxEntries != 2 {
		t.Errorf("the later member wins: %v %+v", err, settings)
	}
	if settings, err := decode(`{"timeoutMs":"-12","nested":{"ratio":"12e1"}}`); err != nil || settings.Timeout != -12 || settings.Nested.Ratio != 120 {
		t.Errorf("JSON numbers in strings: %v %+v", err, settings)
	}
	// 合 JSON 数字的文法之后照目标类型的常规读法：整数字段不收指数写法
	if _, err := decode(`{"timeoutMs":"12e1"}`); err == nil {
		t.Error("12e1 is a JSON number but not an integer literal")
	}
	for _, rejected := range []string{`" 5"`, `"5 "`, `"+5"`, `"007"`, `"0x10"`, `"1_000"`, `"Infinity"`, `"NaN"`, `""`} {
		if _, err := decode(`{"timeoutMs":` + rejected + `}`); err == nil {
			t.Errorf("%s is not a JSON number and must not be read into an integer", rejected)
		}
	}
	if _, err := decode(`{"maxEntries":"1.5"}`); err == nil {
		t.Error("1.5 is a JSON number but not an integer")
	}
	for text, want := range map[string]bool{`"true"`: true, `"TRUE"`: true, `"tRuE"`: true, `"false"`: false, `"False"`: false} {
		if settings, err := decode(`{"verbose":` + text + `}`); err != nil || settings.Verbose == nil || *settings.Verbose != want {
			t.Errorf("%s is %v: %v %+v", text, want, err, settings)
		}
	}
	for _, rejected := range []string{`"1"`, `"t"`, `"yes"`, `" true"`, `"true "`} {
		if _, err := decode(`{"verbose":` + rejected + `}`); err == nil {
			t.Errorf("%s is not a boolean", rejected)
		}
	}
}

// customSetting 自己解码 JSON：类型化读取把它的那段配置节原样交给它。
type customSetting struct {
	raw string
}

// UnmarshalJSON 记下原文。
func (c *customSetting) UnmarshalJSON(data []byte) error {
	c.raw = string(data)
	return nil
}

func TestConfigurationDecodeLeavesUnmarshalersAlone(t *testing.T) {
	var settings struct {
		Custom customSetting `json:"custom"`
	}
	if err := (Configuration{Section: json.RawMessage(`{"Custom":{"A":"1"}}`)}).Decode(&settings); err != nil || settings.Custom.raw != `{"A":"1"}` {
		t.Errorf("custom %v %q", err, settings.Custom.raw)
	}
}
