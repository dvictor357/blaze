module github.com/dvictor357/blaze/mcp

go 1.25.5

require (
	github.com/dvictor357/blaze v0.1.0
	github.com/modelcontextprotocol/go-sdk v1.8.0
)

require (
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

// Develop against the blaze module in this repository. Replace directives
// only apply when this module is the main module, so published versions of
// blaze/mcp resolve blaze from its tag.
replace github.com/dvictor357/blaze => ../
