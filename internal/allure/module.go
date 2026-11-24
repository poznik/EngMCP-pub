package allure

import (
	"context"
	"net/url"

	"github.com/poznik/EngMCP-pub/internal/integration"
)

const report = "/api/rs/"

func Module() integration.Module {
	launches := integration.ReadOperation("allure_list_launches", "Список запусков", "Запуски проекта Allure TestOps. Нумерация страниц с нуля.", []integration.Parameter{integration.ID("project_id", "ID проекта TestOps."), integration.Page("page", 0), integration.Limit("size")}, func(q url.Values) (string, url.Values, error) {
		q.Set("projectId", q.Get("project_id"))
		q.Del("project_id")
		return report + "launch", q, nil
	})
	byID := func(resource string) integration.Route {
		return func(q url.Values) (string, url.Values, error) { return report + resource + "/" + q.Get("id"), nil, nil }
	}
	launch := integration.ReadOperation("allure_get_launch", "Прочитать запуск", "Карточка запуска Allure TestOps по ID.", []integration.Parameter{integration.ID("id", "ID запуска.")}, byID("launch"))
	testcase := integration.ReadOperation("allure_get_test_case", "Прочитать тест-кейс", "Карточка тест-кейса Allure TestOps по ID.", []integration.Parameter{integration.ID("id", "ID тест-кейса.")}, byID("testcase"))
	return integration.Module{ID: "allure", Title: "Allure TestOps", Operations: []integration.Operation{launches, launch, testcase}, Check: func(ctx context.Context, read integration.Reader) (string, integration.Result, error) {
		return integration.User(ctx, read, "/api/uaa/me")
	}}
}
