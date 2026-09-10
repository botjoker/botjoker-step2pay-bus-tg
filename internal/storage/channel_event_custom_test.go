package storage

import (
	"encoding/json"
	"testing"
)

func TestChannelEventContextPatchPreservesReference(t *testing.T) {
	patch, err := channelEventContextPatch("telegram:update:42", []byte(`{"update_id":42,"chat_id":"masked"}`))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]map[string]any
	if err := json.Unmarshal(patch, &decoded); err != nil {
		t.Fatal(err)
	}
	event := decoded["marketing_channel_event"]
	if event["external_event_id"] != "telegram:update:42" {
		t.Fatalf("unexpected external event id: %#v", event["external_event_id"])
	}
	metadata, ok := event["provider_metadata_reference"].(map[string]any)
	if !ok || metadata["update_id"] != float64(42) {
		t.Fatalf("unexpected provider metadata: %#v", event["provider_metadata_reference"])
	}
}

func TestChannelEventContextPatchRejectsInvalidMetadata(t *testing.T) {
	if _, err := channelEventContextPatch("event-1", []byte(`{"broken"`)); err == nil {
		t.Fatal("expected invalid JSON to fail")
	}
}

func TestChannelEventContextPatchSupportsLegacyEmptyMetadata(t *testing.T) {
	patch, err := channelEventContextPatch("web:message:1", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]map[string]any
	if err := json.Unmarshal(patch, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["marketing_channel_event"]["external_event_id"] != "web:message:1" {
		t.Fatalf("legacy event id was not preserved: %s", patch)
	}
}
