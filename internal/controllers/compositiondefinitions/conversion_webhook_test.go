package compositiondefinitions

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestConversionConfFor_Local(t *testing.T) {
	conf, strategy := conversionConfFor(false)
	if strategy != conversionService {
		t.Fatalf("strategy = %q, want %q", strategy, conversionService)
	}
	if conf.Strategy != apiextensionsv1.WebhookConverter {
		t.Fatalf("conf.Strategy = %q, want Webhook", conf.Strategy)
	}
	if conf.Webhook == nil || conf.Webhook.ClientConfig.Service == nil {
		t.Fatal("expected a Service-based webhook client config for local target")
	}
	if conf.Webhook.ClientConfig.URL != nil {
		t.Fatal("local target must not use a URL client config")
	}
}

func TestConversionConfFor_RemoteNoURL(t *testing.T) {
	prev := webhookURL
	webhookURL = ""
	defer func() { webhookURL = prev }()

	conf, strategy := conversionConfFor(true)
	if strategy != conversionNone {
		t.Fatalf("strategy = %q, want %q", strategy, conversionNone)
	}
	if conf.Strategy != apiextensionsv1.NoneConverter {
		t.Fatalf("conf.Strategy = %q, want None", conf.Strategy)
	}
	if conf.Webhook != nil {
		t.Fatal("None strategy must not carry a webhook config")
	}
}

func TestConversionConfFor_RemoteWithURL(t *testing.T) {
	prev := webhookURL
	defer func() { webhookURL = prev }()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"base url gets path appended", "https://core-provider.example.com", "https://core-provider.example.com/convert"},
		{"trailing slash trimmed", "https://core-provider.example.com/", "https://core-provider.example.com/convert"},
		{"full path kept as-is", "https://core-provider.example.com/convert", "https://core-provider.example.com/convert"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			webhookURL = tc.in
			conf, strategy := conversionConfFor(true)
			if strategy != conversionURL {
				t.Fatalf("strategy = %q, want %q", strategy, conversionURL)
			}
			if conf.Strategy != apiextensionsv1.WebhookConverter {
				t.Fatalf("conf.Strategy = %q, want Webhook", conf.Strategy)
			}
			cc := conf.Webhook.ClientConfig
			if cc.Service != nil {
				t.Fatal("remote target must not use a Service client config")
			}
			if cc.URL == nil || *cc.URL != tc.want {
				t.Fatalf("URL = %v, want %q", cc.URL, tc.want)
			}
		})
	}
}
