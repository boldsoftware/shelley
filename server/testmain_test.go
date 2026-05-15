package server

import (
	"errors"
	"net/http"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	exeDevDefaultPortHTTPClient = &http.Client{Transport: defaultPortTestTransport{}}
	os.Exit(m.Run())
}

type defaultPortTestTransport struct{}

func (defaultPortTestTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("reflection disabled in tests")
}
