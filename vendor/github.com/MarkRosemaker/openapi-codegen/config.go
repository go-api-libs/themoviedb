package codegen

import (
	"github.com/MarkRosemaker/cassette"
	"github.com/MarkRosemaker/openapi"
	"github.com/MarkRosemaker/openapi-codegen/config"
	"github.com/spf13/afero"
)

// Config holds the parameters for a single code-generation run.
type Config struct {
	Debug            bool                  // record responses that failed to unmarshal
	SpecPath         string                // path to the OpenAPI spec file (JSON or YAML)
	Spec             *openapi.Document     // OpenAPI spec, if already parsed
	InteractionsPath string                // path to the HTTP interactions with the API
	Interactions     cassette.Interactions // sample HTTP interactions with the API
	OutputDir        string                // directory to write the generated Go files
	OutputFs         afero.Fs              // Filesystem to write the generated Go files
	PackageName      string                // Go package name for the generated code
	UserAgent        string                // User-Agent header value for the generated client
	Production       bool                  // Render production ready JS without development features

	config.Generate
}
