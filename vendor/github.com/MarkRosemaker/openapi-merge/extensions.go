package merge

import (
	"fmt"

	"github.com/MarkRosemaker/openapi"
)

func extensions(a, b openapi.Extensions) error {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}

	// m := map[string]any{}
	// if err := json.Unmarshal(ext, &m); err != nil {
	// 	return err
	// }

	// for k := range m {
	// 	if !strings.HasPrefix(k, "x-") {
	// 		return &errpath.ErrField{Field: k, Err: ErrUnknownField}
	// 	}
	// }

	return fmt.Errorf("extensions unimplemented")
}
