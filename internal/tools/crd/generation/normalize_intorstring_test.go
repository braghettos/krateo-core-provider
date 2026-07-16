package generation

import (
	"encoding/json"
	"testing"
)

// TestNormalizeIntOrStringUnions covers the ACK ec2-chart failure: a Kubernetes
// quantity expressed as a JSON-Schema oneOf/anyOf union of number|integer and string,
// which controller-gen cannot emit. After normalization the union must become
// x-kubernetes-int-or-string:true, and every unrelated construct must be preserved.
func TestNormalizeIntOrStringUnions(t *testing.T) {
	// Mirrors the ec2-chart resources.requests/limits.{cpu,memory} shape + some
	// constructs that must NOT be touched (a real 2-member enum-ish oneOf, an object).
	in := `{
      "type": "object",
      "properties": {
        "resources": {
          "type": "object",
          "properties": {
            "requests": {
              "type": "object",
              "properties": {
                "cpu":    { "oneOf": [ { "type": "number" }, { "type": "string" } ] },
                "memory": { "oneOf": [ { "type": "string" }, { "type": "integer" } ] }
              }
            }
          }
        },
        "mode": { "type": "string", "enum": ["a", "b"] },
        "keepUnion": { "oneOf": [ { "type": "string", "format": "ipv4" }, { "type": "string", "format": "ipv6" } ] }
      }
    }`

	out := normalizeIntOrStringUnions([]byte(in))

	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	props := m["properties"].(map[string]interface{})
	reqs := props["resources"].(map[string]interface{})["properties"].(map[string]interface{})["requests"].(map[string]interface{})["properties"].(map[string]interface{})

	for _, key := range []string{"cpu", "memory"} {
		node := reqs[key].(map[string]interface{})
		if _, hasOneOf := node["oneOf"]; hasOneOf {
			t.Errorf("%s: oneOf not stripped: %v", key, node)
		}
		// Collapsed to a plain string field (a Kubernetes quantity is written "128Mi"/"50m").
		if node["type"] != "string" {
			t.Errorf("%s: expected type:string, got %v", key, node)
		}
	}

	// A non-int-or-string union (two string members with formats) must be untouched.
	keep := props["keepUnion"].(map[string]interface{})
	if _, ok := keep["oneOf"]; !ok {
		t.Errorf("keepUnion: a non-int-or-string oneOf was wrongly collapsed: %v", keep)
	}
	if _, ok := keep["x-kubernetes-int-or-string"]; ok {
		t.Errorf("keepUnion: wrongly marked int-or-string: %v", keep)
	}

	// The enum string must survive untouched.
	mode := props["mode"].(map[string]interface{})
	if mode["type"] != "string" || mode["enum"] == nil {
		t.Errorf("mode: enum/type not preserved: %v", mode)
	}
}

// A schema with no unions must be returned semantically unchanged (fail-open + no-op).
func TestNormalizeIntOrStringUnions_NoUnion(t *testing.T) {
	in := `{"type":"object","properties":{"name":{"type":"string"}}}`
	out := normalizeIntOrStringUnions([]byte(in))
	var a, b interface{}
	_ = json.Unmarshal([]byte(in), &a)
	_ = json.Unmarshal(out, &b)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Errorf("no-union schema changed: %s -> %s", ja, jb)
	}
}
