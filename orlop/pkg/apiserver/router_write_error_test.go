package apiserver

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/conversion"
)

type failedStatusWriter struct {
	header      http.Header
	statusCodes []int
	writeCalls  int
	partial     bool
}

func (w *failedStatusWriter) Header() http.Header  { return w.header }
func (w *failedStatusWriter) WriteHeader(code int) { w.statusCodes = append(w.statusCodes, code) }
func (w *failedStatusWriter) Write(data []byte) (int, error) {
	w.writeCalls++
	n := 0
	if w.partial {
		n = len(data) / 2
	}
	return n, errors.New("SECRET writer error with private-host.example and token")
}

func TestUnsupportedResponseWriteFailure(t *testing.T) {
	for _, route := range []struct {
		name, method               string
		namespaced, crossNamespace bool
	}{
		{name: "cluster create", method: http.MethodPost},
		{name: "cluster watch", method: http.MethodGet},
		{name: "namespaced create", method: http.MethodPost, namespaced: true},
		{name: "namespaced watch", method: http.MethodGet, namespaced: true},
		{name: "cross-namespace watch", method: http.MethodGet, namespaced: true, crossNamespace: true},
	} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/partial=%t", route.name, partial), func(t *testing.T) {
				var logs string
				logger := funcr.New(func(prefix, args string) { logs += prefix + args + "\n" }, funcr.Options{})
				privateScheme, publicScheme := makePrivateScheme(t), makePublicScheme(t)
				privateRegistry := NewResourceRegistry(privateScheme)
				publicRegistry := NewResourceRegistry(publicScheme, WithLogger(logger))
				privateInfo, publicInfo := privateResources()[0], publicResources()[0]
				privateInfo.Namespaced = route.namespaced
				publicInfo.Namespaced = route.namespaced
				publicInfo.Verbs = []string{"get", "list"}
				if err := privateRegistry.Register(privateInfo); err != nil {
					t.Fatal(err)
				}
				if err := publicRegistry.Register(publicInfo); err != nil {
					t.Fatal(err)
				}
				router, err := setupConvertingRouter(publicRegistry, privateRegistry, conversion.NewConverter(publicScheme, privateScheme, ""), privateScheme, nil, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				path := "/apis/" + publicInfo.GVK.GroupVersion().String()
				if route.namespaced && !route.crossNamespace {
					path += "/namespaces/SECRET-project"
				}
				path += "/" + publicInfo.Plural
				if route.method == http.MethodGet {
					path += "?watch=true&secret=SECRET-query"
				}
				req := httptest.NewRequest(route.method, path, nil)
				response := &failedStatusWriter{header: make(http.Header), partial: partial}
				router.ServeHTTP(response, req)
				if len(response.statusCodes) != 1 || response.statusCodes[0] != http.StatusNotImplemented || response.writeCalls != 1 {
					t.Fatalf("response retried or status changed: statuses=%v writes=%d", response.statusCodes, response.writeCalls)
				}
				if response.Header().Get("Content-Type") != "application/json" {
					t.Fatal("response content type changed")
				}
				diagnostic := logs
				if !strings.Contains(diagnostic, "failed to write unsupported-operation response") || !strings.Contains(diagnostic, "response_write_failed") {
					t.Fatalf("missing write-failure diagnostic: %s", diagnostic)
				}
				for _, sensitive := range []string{"SECRET", "private-host.example", "token", path} {
					if strings.Contains(diagnostic, sensitive) {
						t.Fatalf("sensitive value %q leaked in diagnostic: %s", sensitive, diagnostic)
					}
				}
			})
		}
	}
}
