package jenkins

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/poznik/EngMCP-pub/internal/integration"
)

func Reader(base, user, token string) integration.Reader {
	return integration.JSONReader(base, "/", nil, false, func(req *http.Request) { req.SetBasicAuth(user, token) })
}

func jobPath(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	var path strings.Builder
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "%\\") || strings.IndexFunc(part, unicode.IsControl) >= 0 {
			return "", fmt.Errorf("Укажи имя задания или путь из имён папок через /.")
		}
		path.WriteString("/job/")
		path.WriteString(url.PathEscape(part))
	}
	return path.String(), nil
}

const buildFields = "number,url,result,building,timestamp,duration,displayName"

func listRoute(field, tree string) integration.Route {
	return func(args url.Values) (string, url.Values, error) {
		path, err := jobPath(args.Get(field))
		if err != nil {
			return "", nil, err
		}
		start, _ := strconv.ParseInt(args.Get("start"), 10, 64)
		limit, _ := strconv.ParseInt(args.Get("limit"), 10, 64)
		return path + "/api/json", url.Values{"tree": {fmt.Sprintf("%s{%d,%d}", tree, start, start+limit)}}, nil
	}
}

func Module() integration.Module {
	job := integration.Parameter{Name: "job", Description: "Полное имя задания, например team/service.", Required: true}
	paging := []integration.Parameter{{Name: "start", Description: "Смещение с нуля.", Integer: true, Min: 0, Max: 2147483547, Default: "0"}, integration.Limit("limit")}
	jobs := integration.ReadOperation("jenkins_list_jobs", "Список заданий", "Задания верхнего уровня или указанной папки.", append([]integration.Parameter{{Name: "folder", Description: "Полное имя папки."}}, paging...), listRoute("folder", "jobs[name,fullName,url,color]"))
	builds := integration.ReadOperation("jenkins_list_builds", "Список сборок", "Недавние сборки одного задания и их результаты.", append([]integration.Parameter{job}, paging...), listRoute("job", "builds["+buildFields+"]"))
	build := integration.ReadOperation("jenkins_get_build", "Прочитать сборку", "Результат, время и длительность сборки по номеру.", []integration.Parameter{job, integration.ID("number", "Номер сборки.")}, func(q url.Values) (string, url.Values, error) {
		path, err := jobPath(q.Get("job"))
		return path + "/" + q.Get("number") + "/api/json", url.Values{"tree": {buildFields}}, err
	})
	return integration.Module{ID: "jenkins", Title: "Jenkins", Operations: []integration.Operation{jobs, builds, build}, Check: func(ctx context.Context, read integration.Reader) (string, integration.Result, error) {
		return integration.User(ctx, read, "/whoAmI/api/json")
	}}
}
