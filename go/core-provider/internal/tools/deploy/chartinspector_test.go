package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseChartInspectorURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantN   string
		wantNS  string
		wantP   int32
		wantErr bool
	}{
		{
			name:   "full svc dns with port and trailing slash",
			in:     "http://installer-core-provider-chart-inspector.krateo-system.svc.cluster.local:8081/",
			wantN:  "installer-core-provider-chart-inspector",
			wantNS: "krateo-system",
			wantP:  8081,
		},
		{
			name:   "no explicit port defaults to 8081",
			in:     "http://ci.krateo-system.svc",
			wantN:  "ci",
			wantNS: "krateo-system",
			wantP:  8081,
		},
		{
			name:   "whitespace is trimmed",
			in:     "  http://ci.ns.svc.cluster.local:9000/  ",
			wantN:  "ci",
			wantNS: "ns",
			wantP:  9000,
		},
		{
			name:    "bare host without namespace is rejected",
			in:      "http://chart-inspector:8081",
			wantErr: true,
		},
		{
			name:    "non-numeric port is rejected",
			in:      "http://ci.ns.svc:abc",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseChartInspectorURL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %+v", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Name != tc.wantN || got.Namespace != tc.wantNS || got.Port != tc.wantP {
				t.Fatalf("got {%q,%q,%d}, want {%q,%q,%d}", got.Name, got.Namespace, got.Port, tc.wantN, tc.wantNS, tc.wantP)
			}
		})
	}
}

func TestChartInspectorCoordsFromConfigmapTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "configmap.yaml")
	cm := `apiVersion: v1
kind: ConfigMap
metadata:
  name: cdc-config
data:
  URL_SNOWPLOW: http://snowplow.krateo-system.svc.cluster.local:8081/
  URL_CHART_INSPECTOR: http://installer-core-provider-chart-inspector.krateo-system.svc.cluster.local:8081/
`
	if err := os.WriteFile(path, []byte(cm), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ChartInspectorCoordsFromConfigmapTemplate(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "installer-core-provider-chart-inspector" || got.Namespace != "krateo-system" || got.Port != 8081 {
		t.Fatalf("got %+v", got)
	}

	// missing key -> error
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("apiVersion: v1\nkind: ConfigMap\ndata: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ChartInspectorCoordsFromConfigmapTemplate(bad); err == nil {
		t.Fatal("expected error when URL_CHART_INSPECTOR is absent")
	}
}
