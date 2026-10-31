package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type modelsResponseBody struct {
	io.Reader
	closed bool
}

func (b *modelsResponseBody) Close() error {
	b.closed = true
	return nil
}

type modelsResponseTransport struct {
	body   *modelsResponseBody
	status int
}

func (tr modelsResponseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: tr.status, Body: tr.body, Header: make(http.Header)}, nil
}

func TestListModelsClosesResponseBodyOnEveryReturn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		payload string
		wantErr bool
	}{
		{name: "success", status: http.StatusOK, payload: `{"data":[{"id":"local-model"}]}`},
		{name: "invalid_json", status: http.StatusOK, payload: `invalid-json`, wantErr: true},
		{name: "upstream_error", status: http.StatusBadGateway, payload: `upstream unavailable`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &modelsResponseBody{Reader: strings.NewReader(tc.payload)}
			c := New(Config{BaseURL: "https://example.invalid/v1", APIKey: "fake", HTTPClient: &http.Client{Transport: modelsResponseTransport{body: body, status: tc.status}}})
			models, err := c.ListModels(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("unexpected response result: models=%v err=%v", models, err)
			}
			if !tc.wantErr && (len(models) != 1 || models[0] != "local-model") {
				t.Fatalf("unexpected models: %v", models)
			}
			if !body.closed {
				t.Fatal("response body was not closed")
			}
		})
	}
}
