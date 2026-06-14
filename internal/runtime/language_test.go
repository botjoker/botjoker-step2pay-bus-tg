package runtime

import "testing"

func TestSimpleDetect(t *testing.T) {
	cases := map[string]string{
		"Привет, как дела?":          "ru",
		"Hello, how are you doing?":  "en",
		"Сәлеметсіз бе, қалайсыз?":   "kk",
		"":                          "",
	}
	for in, want := range cases {
		if got := simpleDetect(in); got != want {
			t.Errorf("simpleDetect(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectLanguage_AllowedFilter(t *testing.T) {
	a := &Agent{cfg: AgentConfig{
		DefaultLang:      "ru",
		AutoDetectLang:   true,
		AllowedLanguages: []string{"ru", "en"},
	}}

	if got := a.detectLanguage("Hello there friend"); got != "en" {
		t.Errorf("en allowed → %q", got)
	}
	// казахский не в allowed → откат на default ru
	if got := a.detectLanguage("Сәлеметсіз бе қалайсыз бүгін"); got != "ru" {
		t.Errorf("kk not allowed → want ru, got %q", got)
	}
}

func TestDetectLanguage_AutoDetectOff(t *testing.T) {
	a := &Agent{cfg: AgentConfig{DefaultLang: "ru", AutoDetectLang: false}}
	if got := a.detectLanguage("Hello world this is english"); got != "ru" {
		t.Errorf("auto-detect off → want default ru, got %q", got)
	}
}

func TestIsAllowed(t *testing.T) {
	if !isAllowed("ru", []string{"en", "ru"}) {
		t.Error("ru should be allowed")
	}
	if isAllowed("kk", []string{"ru", "en"}) {
		t.Error("kk should not be allowed")
	}
}
