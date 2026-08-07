package agentstore

import "testing"

func TestNormalizeVK(t *testing.T) {
	cases := map[string]string{
		"@ivan_petrov":               "https://vk.com/ivan_petrov",
		"vk.com/ivan.petrov":         "https://vk.com/ivan.petrov",
		"https://vk.com/id123456789": "https://vk.com/id123456789",
	}
	for input, want := range cases {
		got, err := normalizeVK(input)
		if err != nil {
			t.Fatalf("normalizeVK(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("normalizeVK(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeVKRejectsInvalidValue(t *testing.T) {
	if _, err := normalizeVK("12"); err == nil {
		t.Fatal("expected invalid VK identifier to be rejected")
	}
}
