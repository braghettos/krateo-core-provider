package deploy

import (
	"net"
	"net/url"
	"strings"
)

// IsClusterLocalChartURL reports whether a resolved chart package URL is reachable only from inside
// the management cluster — a Kubernetes Service DNS name (*.svc / *.cluster.local), an mDNS *.local
// name, localhost, or a loopback / RFC-1918 private / link-local IP. Such a URL is fine on the hub
// (where core-provider resolves it) but a projected cdc on a remote spoke cannot reach it, so the
// composition would silently fail. Path A / Inc 2's C2 check uses this to surface a
// RemoteChartUnreachable condition instead of projecting an unresolvable URL.
//
// A URL whose host cannot be determined returns false (fail open — do not block on uncertainty).
func IsClusterLocalChartURL(rawURL string) bool {
	host := hostFromChartURL(rawURL)
	if host == "" {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))

	if host == "localhost" {
		return true
	}
	for _, suffix := range []string{".svc", ".svc.cluster.local", ".cluster.local", ".local"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
	}
	return false
}

// hostFromChartURL extracts the host from a chart URL, tolerating oci://, http(s):// and
// scheme-less registry references.
func hostFromChartURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	if !strings.Contains(rawURL, "://") {
		// scheme-less (e.g. "registry.example.com/chart:1.0") — give url.Parse a scheme so it
		// populates Host rather than Path.
		rawURL = "oci://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
