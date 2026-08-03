package generation

import (
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func crdWithStatus() *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: "v1-0-0",
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"status": {Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{
								"helmChartUrl": {Type: "string"},
							}},
						},
					},
				},
			}},
		},
	}
}

func TestInjectStatusFields(t *testing.T) {
	crd := crdWithStatus()
	fields := []StatusField{
		{ForPath: "url", Type: "string"},
		{ForPath: "replicas", Type: "integer"},
		{ForPath: "network.host", Type: "string"},     // nested
		{ForPath: "raw", PreserveUnknownFields: true}, // escape hatch
		{ForPath: "blob", Type: "object"},             // object w/o schema -> preserve-unknown
		{ForPath: "untyped"},                          // -> string fallback
		{ForPath: "endpoints", Schema: &apiextensionsv1.JSONSchemaProps{Type: "array", Items: &apiextensionsv1.JSONSchemaPropsOrArray{Schema: &apiextensionsv1.JSONSchemaProps{Type: "string"}}}},
	}
	if err := InjectStatusFields(crd, fields); err != nil {
		t.Fatalf("InjectStatusFields: %v", err)
	}
	props := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"].Properties

	// baseline preserved
	if _, ok := props["helmChartUrl"]; !ok {
		t.Error("baseline helmChartUrl dropped")
	}
	if props["url"].Type != "string" {
		t.Errorf("url type = %q", props["url"].Type)
	}
	if props["replicas"].Type != "integer" {
		t.Errorf("replicas type = %q", props["replicas"].Type)
	}
	if props["untyped"].Type != "string" {
		t.Errorf("untyped fallback type = %q, want string", props["untyped"].Type)
	}
	// nested
	host := props["network"].Properties["host"]
	if props["network"].Type != "object" || host.Type != "string" {
		t.Errorf("network.host = %+v / %+v", props["network"], host)
	}
	// preserve-unknown (explicit + object-without-schema)
	if props["raw"].XPreserveUnknownFields == nil || !*props["raw"].XPreserveUnknownFields {
		t.Error("raw should be preserve-unknown")
	}
	if props["blob"].XPreserveUnknownFields == nil || !*props["blob"].XPreserveUnknownFields {
		t.Error("blob (object w/o schema) should fall back to preserve-unknown")
	}
	// explicit complex schema used verbatim
	if props["endpoints"].Type != "array" || props["endpoints"].Items == nil {
		t.Errorf("endpoints schema not used verbatim: %+v", props["endpoints"])
	}
}

func TestInjectStatusFields_AllVersions(t *testing.T) {
	crd := crdWithStatus()
	// add a second version sharing the same status shape
	crd.Spec.Versions = append(crd.Spec.Versions, apiextensionsv1.CustomResourceDefinitionVersion{
		Name: "v2-0-0",
		Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
			Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{"status": {Type: "object"}},
		}},
	})
	if err := InjectStatusFields(crd, []StatusField{{ForPath: "url", Type: "string"}}); err != nil {
		t.Fatal(err)
	}
	for _, v := range crd.Spec.Versions {
		if _, ok := v.Schema.OpenAPIV3Schema.Properties["status"].Properties["url"]; !ok {
			t.Errorf("version %s missing injected url", v.Name)
		}
	}
}

func TestValidateStatusFields(t *testing.T) {
	cases := []struct {
		name    string
		fields  []StatusField
		wantErr string // substring; "" = no error
	}{
		{"ok", []StatusField{{ForPath: "url", Expression: `${ .self.spec.host }`}}, ""},
		{"ok literal", []StatusField{{ForPath: "region", Expression: "eu-west"}}, ""},
		{"reserved", []StatusField{{ForPath: "managed", Expression: `${ . }`}}, "reserved baseline"},
		{"reserved nested", []StatusField{{ForPath: "conditions.foo", Expression: `${ . }`}}, "reserved baseline"},
		{"dup", []StatusField{{ForPath: "a", Expression: "x"}, {ForPath: "a", Expression: "y"}}, "duplicate"},
		{"mutually exclusive", []StatusField{{ForPath: "a", Expression: "x", Type: "string", PreserveUnknownFields: true}}, "cannot be combined"},
		{"schema+preserve", []StatusField{{ForPath: "a", Expression: "x", Schema: &apiextensionsv1.JSONSchemaProps{}, PreserveUnknownFields: true}}, "mutually exclusive"},
		{"bad jq", []StatusField{{ForPath: "a", Expression: `${ .foo | (( }`}}, "invalid jq"},
		{"empty expr", []StatusField{{ForPath: "a"}}, "expression is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateStatusFields(c.fields)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, c.wantErr)
			}
		})
	}
}
