package agentstore

import "testing"

func TestNormalizeContactChannelKeepsChannelSeparateFromSource(t *testing.T) {
	tests := map[string]string{
		"telegram": "telegram",
		" VK ":     "vk",
		"MAX":      "max",
		"whatsapp": "whatsapp",
		"email":    "email",
		"web":      "samba_widget",
		"unknown":  "other",
	}
	for input, expected := range tests {
		if actual := normalizeContactChannel(input); actual != expected {
			t.Fatalf("normalizeContactChannel(%q) = %q, want %q", input, actual, expected)
		}
	}
}
