package flatten

import (
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
	"github.com/ettle/strcase"
)

func parameterRef(d *openapi.Document, p *openapi.ParameterRef) error {
	if p.Ref != nil {
		return nil
	}

	// reference the parameter in the components
	paramName := uniqueName(d.Components.Parameters, p.Value.Name)
	d.Components.Parameters.Set(paramName, &openapi.ParameterRef{Value: p.Value})
	p.Ref = newRef("parameters", paramName)

	return parameter(d, p.Value)
}

func parameter(d *openapi.Document, p *openapi.Parameter) error {
	paramName := strcase.ToGoPascal(p.Name)

	if p.Schema != nil {
		if err := inlineSchema(d, p.Schema, paramName, false); err != nil {
			return &errpath.ErrField{Field: "schema", Err: err}
		}
	}

	if err := content(d, p.Content, paramName, "Parameter", false); err != nil {
		return &errpath.ErrField{Field: "content", Err: err}
	}

	return nil
}
