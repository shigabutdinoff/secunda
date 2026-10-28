package httpapi

//go:generate go run github.com/swaggo/swag/cmd/swag@v1.16.6 init --dir .,../../cmd/server --generalInfo doc.go --parseFuncBody --outputTypes json,yaml --output ../../api

// @title Secunda
// @version 1.0
// @description Сервис управления задачами команд: регистрация и вход, команды и участники,
// @description задачи с историей изменений и комментариями, статистика команды.
// @BasePath /
// @securityDefinitions.apikey Bearer
// @in header
// @name Authorization
// @description Bearer и токен из /api/v1/login.
