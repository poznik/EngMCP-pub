package gitlab

import (
	"context"
	"net/http"
	"net/url"

	"github.com/poznik/EngMCP-pub/internal/integration"
)

const api = "/api/v4/"

func Reader(base, pat string) integration.Reader {
	return integration.JSONReader(base, api, nil, true, func(req *http.Request) { req.Header.Set("PRIVATE-TOKEN", pat) })
}

func Module() integration.Module {
	project := integration.ID("project_id", "Числовой ID проекта GitLab.")
	paging := []integration.Parameter{integration.Page("page", 1), integration.Limit("per_page")}
	projects := integration.ReadOperation("gitlab_list_projects", "Найти проекты", "Доступные проекты GitLab. Поиск по имени или пути, страницы с 1.", append([]integration.Parameter{{Name: "search", Description: "Часть имени или пути проекта."}}, paging...), func(q url.Values) (string, url.Values, error) {
		return api + "projects", q, nil
	})
	projectList := func(resource string) integration.Route {
		return func(q url.Values) (string, url.Values, error) {
			path := api + "projects/" + q.Get("project_id") + "/" + resource
			q.Del("project_id")
			return path, q, nil
		}
	}
	scoped := append([]integration.Parameter{project}, paging...)
	mrs := integration.ReadOperation("gitlab_list_merge_requests", "Список merge requests", "Merge requests указанного проекта, страницы с 1.", scoped, projectList("merge_requests"))
	mr := integration.ReadOperation("gitlab_get_merge_request", "Прочитать merge request", "Сведения о merge request по номеру внутри проекта.", []integration.Parameter{project, integration.ID("iid", "Номер merge request внутри проекта.")}, func(q url.Values) (string, url.Values, error) {
		return api + "projects/" + q.Get("project_id") + "/merge_requests/" + q.Get("iid"), nil, nil
	})
	pipelines := integration.ReadOperation("gitlab_list_pipelines", "Список пайплайнов", "Пайплайны проекта и их статусы, страницы с 1.", scoped, projectList("pipelines"))
	return integration.Module{ID: "gitlab", Title: "GitLab", Operations: []integration.Operation{projects, mrs, mr, pipelines}, Check: func(ctx context.Context, read integration.Reader) (string, integration.Result, error) {
		return integration.User(ctx, read, api+"user")
	}}
}
