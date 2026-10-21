package middleware

import "testing"

func TestPermissionMatching(t *testing.T) {
	cases := []struct {
		method, path, permission string
		want                     bool
	}{
		{"GET", "/api/files", "GET:/api/files", true},
		{"POST", "/api/files", "GET:/api/files", false},
		{"GET", "/api/files/1", "GET:/api/files/*", true},
		{"GET", "/api/admin/users", "GET:/api/files/*", false},
		{"DELETE", "/api/files/1", "*:/api/files/*", true},
		{"GET", "/api/files", "malformed", false},
	}
	for _, tc := range cases {
		if got := matchPermission(tc.method, tc.path, tc.permission); got != tc.want {
			t.Errorf("%s %s with %q: got %v, want %v", tc.method, tc.path, tc.permission, got, tc.want)
		}
	}
}

func TestExtractBearerToken(t *testing.T) {
	for header, want := range map[string]string{"": "", "Basic x": "", "Bearer": "", "Bearer ": "", "bearer test-token": "test-token"} {
		if got := extractBearerToken(header); got != want {
			t.Errorf("unexpected bearer parsing for %q", header)
		}
	}
}

func TestDefaultCapabilitiesDenyUnlistedRoutes(t *testing.T) {
	for _, tc := range []struct {
		method, route string
		want          bool
	}{
		{"GET", "/api/files", true},
		{"GET", "/api/files/:id/download", true},
		{"POST", "/api/auth/logout", true},
		{"DELETE", "/api/files", false},
		{"POST", "/api/new-sensitive-action", false},
		{"PUT", "/api/settings", false},
		{"POST", "/api/ai/assistant/settings", false},
		{"GET", "/api/admin/users", false},
		{"GET", "", false},
	} {
		if got := defaultAllowPath(tc.method, tc.route); got != tc.want {
			t.Errorf("%s %s: got %v, want %v", tc.method, tc.route, got, tc.want)
		}
	}
}
