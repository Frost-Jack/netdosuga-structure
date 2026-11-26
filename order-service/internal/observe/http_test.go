package observe

import (
	"errors"
	"io"
	"net/http"
	"testing"
)

type failedBody struct{ err error }

func (b failedBody) Read([]byte) (int, error) { return 0, b.err }
func (b failedBody) Close() error             { return b.err }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPTransportPreservesDependencyFailures(t *testing.T) {
	failure := errors.New("dependency failed")
	request, err := http.NewRequest(http.MethodGet, "http://fixture.invalid/unmatched", nil)
	if err != nil {
		t.Fatal(err)
	}
	unreachable := HTTPTransport{Base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, failure
	})}
	if response, err := unreachable.RoundTrip(request); response != nil || !errors.Is(err, failure) {
		t.Fatalf("transport failure returned %v, %v", response, err)
	}
	for _, method := range []string{"read", "close"} {
		t.Run(method, func(t *testing.T) {
			transport := HTTPTransport{Base: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 502, Body: failedBody{failure}}, nil
			})}
			response, err := transport.RoundTrip(request)
			if err != nil || response.StatusCode != 502 {
				t.Fatalf("unexpected response: %v, %v", response, err)
			}
			if method == "read" {
				if _, err := io.ReadAll(response.Body); !errors.Is(err, failure) {
					t.Fatalf("read returned %v", err)
				}
			}
			if err := response.Body.Close(); !errors.Is(err, failure) {
				t.Fatalf("close returned %v", err)
			}
		})
	}
}
