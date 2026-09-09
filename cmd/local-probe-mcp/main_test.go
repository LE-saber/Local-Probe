package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouteMCPLeavesOAuthMetadataAbsent(t *testing.T) {
	t.Parallel()

	handler := routeMCP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, test := range []struct {
		path string
		want int
	}{
		{path: "/mcp", want: http.StatusNoContent},
		{path: "/.well-known/oauth-protected-resource/mcp", want: http.StatusNotFound},
		{path: "/.well-known/oauth-authorization-server", want: http.StatusNotFound},
	} {
		req := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != test.want {
			t.Fatalf("GET %s status = %d, want %d", test.path, response.Code, test.want)
		}
	}
}
