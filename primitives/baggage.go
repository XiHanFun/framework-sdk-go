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

// MaxBaggageValueLength 是已登记键的值按 UTF-16 码元计的最大长度（conventions/baggage-keys.md，ADR-0204、ADR-0233）
const MaxBaggageValueLength = 256

// IsBaggageValueAcceptable 判断已登记键的值能否进入调用上下文：非空，按 UTF-16 码元计不超过 MaxBaggageValueLength
// （基本多文种平面以外的字符算两个，与 .NET、JavaScript、Java 的字符串长度相同），不含 U+0000–U+001F 与 U+007F–U+009F
func IsBaggageValueAcceptable(value string) bool {
	if value == "" {
		return false
	}
	units := 0
	for _, r := range value {
		if r <= 0x1F || (r >= 0x7F && r <= 0x9F) {
			return false
		}
		if r >= 0x10000 {
			units += 2
		} else {
			units++
		}
	}
	return units <= MaxBaggageValueLength
}

// IsBaggageEntryAcceptable 判断一个 Baggage 成员能否进入调用上下文：未登记的键不在这里管（载体与插件协议各自剥离），
// 幂等键例外（丢了它重复的调用会执行两次，不合法的幂等键由 Idempotency 阶段以校验错误拒绝），其余已登记的键按值核对
func IsBaggageEntryAcceptable(key, value string) bool {
	return !IsBaggageKeyRegistered(key) || key == BaggageIdempotencyKey || IsBaggageValueAcceptable(value)
}
