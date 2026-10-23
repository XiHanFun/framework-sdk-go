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
