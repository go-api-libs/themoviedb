package flatten

import (
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
	"github.com/ettle/strcase"
)

func nameRequestBody(opID string) string {
	return strcase.ToGoPascal(opID)
}

func requestBody(d *openapi.Document, r *openapi.RequestBody, reqBodyName string) error {
	if err := content(d, r.Content, reqBodyName, "RequestBody", false); err != nil {
		return &errpath.ErrField{Field: "content", Err: err}
	}

	return nil
}

func requestBodyRef(d *openapi.Document, r *openapi.RequestBodyRef, reqBodyName string) error {
	if r.Ref != nil {
		return nil
	}

	// reference the request body in the components
	reqBodyName = uniqueName(d.Components.RequestBodies, reqBodyName)
	d.Components.RequestBodies.Set(reqBodyName, &openapi.RequestBodyRef{Value: r.Value})
	r.Ref = newRef("requestBodies", reqBodyName)

	return requestBody(d, r.Value, reqBodyName)
}
