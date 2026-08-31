package httptransport

import (
	"encoding/json"
	"testing"

	configdomain "github.com/lihongjie0209/config-service/internal/dynamicconfig"
)

func TestConfigEntryResponsePreservesJSONValue(t *testing.T) {
	response := configEntryResponse(configdomain.Entry{ID: "config-1", Value: []byte(`{"enabled":true,"limit":5}`)})
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	value, ok := body["value"].(map[string]any)
	if !ok || value["enabled"] != true || value["limit"] != float64(5) {
		t.Fatalf("value = %#v, want JSON object", body["value"])
	}
}
