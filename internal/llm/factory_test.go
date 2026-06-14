package llm

import "testing"

func TestCheckAllowed(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		policy   ProfilePolicy
		wantErr  bool
	}{
		{
			name:     "allowed in list",
			provider: "openai",
			policy:   ProfilePolicy{AllowedLLMProviders: []string{"openai", "yandex_gpt"}},
			wantErr:  false,
		},
		{
			name:     "not in list",
			provider: "anthropic",
			policy:   ProfilePolicy{AllowedLLMProviders: []string{"openai"}},
			wantErr:  true,
		},
		{
			name:     "sovereign blocks non-RU even if listed",
			provider: "openai",
			policy:   ProfilePolicy{AllowedLLMProviders: []string{"openai"}, SovereignMode: true},
			wantErr:  true,
		},
		{
			name:     "sovereign allows RU provider",
			provider: "yandex_gpt",
			policy:   ProfilePolicy{AllowedLLMProviders: []string{"yandex_gpt"}, SovereignMode: true},
			wantErr:  false,
		},
		{
			name:     "empty allow-list denies",
			provider: "openai",
			policy:   ProfilePolicy{AllowedLLMProviders: nil},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckAllowed(tt.provider, tt.policy)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CheckAllowed(%q) error = %v, wantErr = %v", tt.provider, err, tt.wantErr)
			}
		})
	}
}

func TestNewProviderUnknown(t *testing.T) {
	_, err := NewProvider("does_not_exist", Credentials{}, ProfilePolicy{
		AllowedLLMProviders: []string{"does_not_exist"},
	})
	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
}

func TestNewProviderRespectsPolicy(t *testing.T) {
	_, err := NewProvider("openai", Credentials{}, ProfilePolicy{
		AllowedLLMProviders: []string{"yandex_gpt"},
	})
	if err == nil {
		t.Fatal("expected policy rejection for openai, got nil")
	}
}
