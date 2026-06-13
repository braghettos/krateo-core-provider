package crd

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func newCRD() *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{}
}

func TestInjectConversionConf_Local(t *testing.T) {
	crd := newCRD()
	injectConversionConfToCRD(crd, ApplyOpts{
		CABundle:                []byte("ca"),
		WebhookServiceName:      "core-provider-webhook-service",
		WebhookServiceNamespace: "krateo-system",
	})

	conv := crd.Spec.Conversion
	if conv == nil || conv.Strategy != apiextensionsv1.WebhookConverter {
		t.Fatalf("expected webhook converter, got %+v", conv)
	}
	if conv.Webhook.ClientConfig.Service == nil {
		t.Fatal("local target must use a Service client config")
	}
	if conv.Webhook.ClientConfig.URL != nil {
		t.Fatal("local target must not use a URL client config")
	}
}

func TestInjectConversionConf_RemoteNoURL(t *testing.T) {
	crd := newCRD()
	injectConversionConfToCRD(crd, ApplyOpts{Remote: true})

	conv := crd.Spec.Conversion
	if conv == nil || conv.Strategy != apiextensionsv1.NoneConverter {
		t.Fatalf("expected NoneConverter for remote target without URL, got %+v", conv)
	}
	if conv.Webhook != nil {
		t.Fatal("None strategy must not carry a webhook config")
	}
}

func TestInjectConversionConf_RemoteWithURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"base url gets path appended", "https://cp.example.com", "https://cp.example.com/convert"},
		{"trailing slash trimmed", "https://cp.example.com/", "https://cp.example.com/convert"},
		{"full path kept", "https://cp.example.com/convert", "https://cp.example.com/convert"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			crd := newCRD()
			injectConversionConfToCRD(crd, ApplyOpts{Remote: true, WebhookURL: tc.in, CABundle: []byte("ca")})

			conv := crd.Spec.Conversion
			if conv == nil || conv.Strategy != apiextensionsv1.WebhookConverter {
				t.Fatalf("expected webhook converter, got %+v", conv)
			}
			cc := conv.Webhook.ClientConfig
			if cc.Service != nil {
				t.Fatal("remote target must not use a Service client config")
			}
			if cc.URL == nil || *cc.URL != tc.want {
				t.Fatalf("URL = %v, want %q", cc.URL, tc.want)
			}
		})
	}
}
