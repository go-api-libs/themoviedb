package flatten

import (
	"iter"
	"slices"

	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

func parameters(d *openapi.Document, ps openapi.Parameters) error {
	for name, p := range ps.ByIndex() {
		// NOTE: We are *not* calling parameterRef here,
		// because we are calling this function from Components,
		// where the parameter should already be.
		if err := parameter(d, p.Value); err != nil {
			return &errpath.ErrKey{Key: name, Err: err}
		}
	}

	return nil
}

func hoistParams(d *openapi.Document) {
	for _, pi := range d.Paths {
		candidates := openapi.ParameterList{}
		for _, op := range pi.Operations {
			candidates = append(candidates, op.Parameters...)
		}

		for _, candidate := range candidates {
			alreadyHoisted := slices.ContainsFunc(pi.Parameters, func(param *openapi.ParameterRef) bool {
				return candidate.Value == param.Value
			})

			if alreadyHoisted || allOpsHave(pi.Operations, candidate.Value) {
				if !alreadyHoisted {
					pi.Parameters = append(pi.Parameters, candidate)
				}

				for _, op := range pi.Operations {
					op.Parameters = slices.DeleteFunc(op.Parameters, func(p *openapi.ParameterRef) bool {
						return p.Value == candidate.Value
					})
				}
			}
		}
	}
}

func allOpsHave(ops iter.Seq2[string, *openapi.Operation], candidate *openapi.Parameter) bool {
	for _, op := range ops {
		if !slices.ContainsFunc(op.Parameters, func(p *openapi.ParameterRef) bool {
			return candidate == p.Value
		}) {
			return false
		}
	}

	return true
}
