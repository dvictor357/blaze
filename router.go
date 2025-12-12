package blaze

import (
	"net/http"
	"strings"
)

type route struct {
	method  string
	path    string
	handler HandlerFunc
}

type Router struct {
	routes      []route
	paramRoutes []route
}

func newRouter() *Router {
	return &Router{}
}

func (r *Router) handle(method, path string, handler HandlerFunc) {
	if strings.Contains(path, ":") {
		r.paramRoutes = append(r.paramRoutes, route{method, path, handler})
	} else {
		r.routes = append(r.routes, route{method, path, handler})
	}
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	for _, route := range r.routes {
		if req.Method == route.method && req.URL.Path == route.path {
			ctx := &Context{ResponseWriter: w, Request: req, params: map[string]string{}}
			if err := route.handler(ctx); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
	}

	for _, route := range r.paramRoutes {
		if req.Method != route.method {
			continue
		}
		if params := match(route.path, req.URL.Path); params != nil {
			ctx := &Context{ResponseWriter: w, Request: req, params: params}
			if err := route.handler(ctx); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
	}

	http.NotFound(w, req)
}

func match(pattern, path string) map[string]string {
	pp := strings.Split(strings.Trim(pattern, "/"), "/")
	pa := strings.Split(strings.Trim(path, "/"), "/")
	if len(pp) != len(pa) {
		return nil
	}

	params := make(map[string]string)
	for i, part := range pp {
		if after, ok := strings.CutPrefix(part, ":"); ok {
			params[after] = pa[i]
		} else if part != pa[i] {
			return nil
		}
	}
	return params
}
