package middleware

import (
	"encoding/json"
	"os"
	"testing"
)

// Snapshot of actual APIEndpoint declarations/calls in the frozen iOS client.
// Sources are recorded beside each method/path. This tests authorization policy,
// not UI behavior or the complete business implementation of every route.
func TestExistingAppleClientDefaultPermissions(t *testing.T) {
	raw, err := os.ReadFile("testdata/apple_client_routes.json")
	if err != nil {
		t.Fatal(err)
	}
	var routes []struct {
		Method, Path, Access string
		Sources              []string
	}
	if err := json.Unmarshal(raw, &routes); err != nil {
		t.Fatal(err)
	}
	if len(routes) == 0 {
		t.Fatal("empty client contract")
	}
	for _, route := range routes {
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			if len(route.Sources) == 0 {
				t.Fatal("client route lacks source evidence")
			}
			switch route.Access {
			case "user", "self":
				if !defaultAllowPath(route.Method, route.Path) {
					t.Fatal("existing client route is absent from default user policy")
				}
			case "public", "admin", "explicit":
				if defaultAllowPath(route.Method, route.Path) {
					t.Fatal("public/restricted route unexpectedly granted as ordinary business capability")
				}
			default:
				t.Fatal("unknown client access expectation")
			}
			if route.Access == "self" && !isSelfSessionRoute(route.Method, route.Path) {
				t.Fatal("client logout requires a business grant")
			}
		})
	}
}
