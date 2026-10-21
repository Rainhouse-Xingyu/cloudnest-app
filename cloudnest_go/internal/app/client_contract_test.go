package app

import (
	"encoding/json"
	"os"
	"testing"

	"cloudenest_go/internal/config"
	"github.com/gin-gonic/gin"
)

func TestExistingAppleClientRoutesAreRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// Registration is pure: no handlers are invoked or database connections made.
	router := NewContainer(config.Config{}, nil, nil).NewRouter()
	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	raw, err := os.ReadFile("../middleware/testdata/apple_client_routes.json")
	if err != nil {
		t.Fatal(err)
	}
	var routes []struct{ Method, Path string }
	if err := json.Unmarshal(raw, &routes); err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if !registered[route.Method+" "+route.Path] {
			t.Errorf("existing client calls an unregistered route: %s %s", route.Method, route.Path)
		}
	}
}
