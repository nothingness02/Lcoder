package config

// ProviderInfo describes a built-in provider entry surfaced in the TUI picker
// and used to map a provider to the standard env var holding its api key.
type ProviderInfo struct {
	Name        string // internal id, e.g. "openai"
	Display     string // human-facing name for the TUI
	KeyEnv      string // standard api key environment variable
	DefaultBase string // non-standard base_url; empty when the engine's default applies
}

// BuiltinProviders is the curated list of common providers shown in the TUI.
// The engine's base-URL table already covers most providers' base_url; this
// table only adds what the UI needs (display name, key env) plus a few
// non-standard bases.
var BuiltinProviders = []ProviderInfo{
	{Name: "openai", Display: "OpenAI", KeyEnv: "OPENAI_API_KEY"},
	{Name: "anthropic", Display: "Anthropic", KeyEnv: "ANTHROPIC_API_KEY"},
	{Name: "deepseek", Display: "DeepSeek", KeyEnv: "DEEPSEEK_API_KEY"},
	{Name: "moonshot", Display: "Moonshot (Kimi)", KeyEnv: "MOONSHOT_API_KEY", DefaultBase: "https://api.moonshot.cn/v1"},
	{Name: "kimi-code", Display: "Kimi Code", KeyEnv: "KIMI_CODE_API_KEY", DefaultBase: "https://api.kimi.com/coding/v1"},
	{Name: "gemini", Display: "Google Gemini", KeyEnv: "GEMINI_API_KEY"},
	{Name: "xai", Display: "xAI", KeyEnv: "XAI_API_KEY"},
	{Name: "alibaba", Display: "Alibaba (Qwen)", KeyEnv: "ALIBABA_API_KEY"},
	{Name: "zhipu", Display: "Zhipu AI (GLM)", KeyEnv: "ZHIPU_API_KEY"},
	{Name: "xiaomi", Display: "Xiaomi (MiMo)", KeyEnv: "MIMO_API_KEY"},
	{Name: "openrouter", Display: "OpenRouter", KeyEnv: "OPENROUTER_API_KEY"},
}

// BuiltinProvider returns the built-in entry for the given provider name.
func BuiltinProvider(name string) (ProviderInfo, bool) {
	for _, p := range BuiltinProviders {
		if p.Name == name {
			return p, true
		}
	}
	return ProviderInfo{}, false
}
