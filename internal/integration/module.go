// Package integration определяет контракт встроенного модуля EngMCP.
package integration

import (
	"context"
	"encoding/json"
)

type Result struct {
	JSON              json.RawMessage
	HTTPStatus, Bytes int
	Stage             string
	RedirectOrigin    string
	LoginPage         bool
}

type Operation struct {
	Name, Title, Description string
	ReadOnly                 bool
	Schema                   map[string]any
	Call                     func(context.Context, Reader, json.RawMessage) (Result, error)
}

type Module struct {
	ID, Title       string
	SupportsGateway bool
	Operations      []Operation
	Check           func(context.Context, Reader) (string, Result, error)
}
