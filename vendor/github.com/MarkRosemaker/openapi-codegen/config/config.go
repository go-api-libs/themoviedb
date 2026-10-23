package config

type Generate struct {
	// File selection: if all are false, client files are generated.
	// Set one or more to true to generate only those files.
	Types      bool // generate types.gen.go
	Client     bool // generate client.gen.go
	ClientTest bool // generate client.gen_test.go
	Server     bool // generate server.gen.go
	JS         bool // generate api.js
}

func (g *Generate) setDefaults() {
	if g.JS || g.Client || g.Types || g.ClientTest || g.Server {
		return
	}

	// no flag is true: generate all client files
	g.Types = true
	g.Client = true
	g.ClientTest = true
}

func (g Generate) GoFiles() bool {
	g.setDefaults()
	return g.Types || g.Client || g.ClientTest || g.Server
}

// shouldGenerate reports whether filename f should be written given cfg.
func (g Generate) ShouldGenerate(f string) bool {
	g.setDefaults()

	switch f {
	case "types.gen.go":
		return g.Types
	case "client.gen.go":
		return g.Client
	case "client.gen_test.go":
		return g.ClientTest
	case "server.gen.go":
		return g.Server
	case "api.js":
		return g.JS
	default:
		return false
	}
}
