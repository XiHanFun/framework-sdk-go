package primitives

import "regexp"

// 已登记的 Baggage 键（conventions/baggage-keys.md）。
const (
	// BaggageTenant 是当前租户 ID。
	BaggageTenant = "xihan.tenant"
	// BaggagePrincipal 是当前主体 ID。
	BaggagePrincipal = "xihan.principal"
	// BaggageCorrelation 是业务关联 ID。
	BaggageCorrelation = "xihan.correlation"
	// BaggageIdempotencyKey 是幂等键。
	BaggageIdempotencyKey = "xihan.idempotency-key"
	// BaggageLocale 是请求文化（BCP 47）。
	BaggageLocale = "xihan.locale"
)

// baggageKeyPattern 是键格式 xihan.<name>，小写，连字符分隔。
var baggageKeyPattern = regexp.MustCompile(`^xihan\.[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// registeredBaggageKeys 是已登记的键，按登记顺序。
var registeredBaggageKeys = []string{BaggageTenant, BaggagePrincipal, BaggageCorrelation, BaggageIdempotencyKey, BaggageLocale}

// RegisteredBaggageKeys 返回已登记的键的副本。
func RegisteredBaggageKeys() []string {
	keys := make([]string, len(registeredBaggageKeys))
	copy(keys, registeredBaggageKeys)
	return keys
}

// IsBaggageKeyWellFormed 判断键是否符合 xihan.<name>。
func IsBaggageKeyWellFormed(key string) bool { return baggageKeyPattern.MatchString(key) }

// IsBaggageKeyRegistered 判断键是否已登记。
func IsBaggageKeyRegistered(key string) bool {
	for _, registered := range registeredBaggageKeys {
		if registered == key {
			return true
		}
	}
	return false
}
