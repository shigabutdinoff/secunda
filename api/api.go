// Package api хранит описание API сервиса в формате Swagger 2.0. Описание
// строит swag из аннотаций обработчиков internal/httpapi командой
// go generate ./internal/httpapi.
package api

import _ "embed"

// Swagger содержит описание API в JSON.
//
//go:embed swagger.json
var Swagger []byte
