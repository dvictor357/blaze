package blaze

import (
	"net/http"
	"strings"
)

// node represents a node in the radix tree
type node struct {
	path     string      // path segment (compressed)
	handler  HandlerFunc // handler if this node is an endpoint
	children []*node     // child nodes (sorted by first char for binary search potential)
	param    string      // parameter name if this is a :param node
	wildcard bool        // true if this is a *wildcard node
}

// Router is a high-performance radix tree based router
type Router struct {
	trees map[string]*node // per-method trees for O(1) method lookup
}

func newRouter() *Router {
	return &Router{
		trees: make(map[string]*node),
	}
}

// handle registers a new route
func (r *Router) handle(method, path string, handler HandlerFunc) {
	if r.trees[method] == nil {
		r.trees[method] = &node{}
	}
	r.insert(r.trees[method], path, handler)
}

// insert adds a path to the radix tree
func (r *Router) insert(root *node, path string, handler HandlerFunc) {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		root.handler = handler
		return
	}

	segments := splitPath(path)
	current := root

	for _, seg := range segments {
		child := r.findChild(current, seg)
		if child == nil {
			child = &node{}
			if strings.HasPrefix(seg, ":") {
				child.param = seg[1:]
				child.path = ":"
			} else if strings.HasPrefix(seg, "*") {
				child.wildcard = true
				child.path = "*"
				child.param = seg[1:]
			} else {
				child.path = seg
			}
			current.children = append(current.children, child)
		}
		current = child
	}
	current.handler = handler
}

// findChild finds a matching child node
func (r *Router) findChild(n *node, seg string) *node {
	for _, child := range n.children {
		if child.path == seg {
			return child
		}
		// Match param nodes
		if child.path == ":" && strings.HasPrefix(seg, ":") {
			return child
		}
	}
	return nil
}

// lookup finds a handler and extracts params
func (r *Router) lookup(method, path string) (HandlerFunc, map[string]string) {
	root := r.trees[method]
	if root == nil {
		return nil, nil
	}

	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return root.handler, map[string]string{}
	}

	segments := splitPath(path)
	params := make(map[string]string)
	current := root

	for i, seg := range segments {
		child := r.matchChild(current, seg, params)
		if child == nil {
			return nil, nil
		}
		// Wildcard captures rest of path
		if child.wildcard {
			params[child.param] = strings.Join(segments[i:], "/")
			return child.handler, params
		}
		current = child
	}

	return current.handler, params
}

// matchChild finds a child that matches the segment
func (r *Router) matchChild(n *node, seg string, params map[string]string) *node {
	// First try exact match (fastest)
	for _, child := range n.children {
		if child.path == seg {
			return child
		}
	}
	// Then try param match
	for _, child := range n.children {
		if child.path == ":" {
			params[child.param] = seg
			return child
		}
	}
	// Finally try wildcard
	for _, child := range n.children {
		if child.wildcard {
			return child
		}
	}
	return nil
}

// splitPath splits path into segments
func splitPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

// ServeHTTP implements http.Handler
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	handler, params := r.lookup(req.Method, req.URL.Path)
	if handler == nil {
		http.NotFound(w, req)
		return
	}

	ctx := &Context{
		ResponseWriter: w,
		Request:        req,
		params:         params,
	}

	if err := handler(ctx); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
