package flatten

import "fmt"

func uniqueName[M ~map[string]V, V any](m M, name string) string {
	idx := 1
	altName := name

	for {
		// check if the name already exists
		if _, ok := m[altName]; !ok {
			return altName
		}

		// increase the number next to the name
		idx++
		altName = fmt.Sprintf("%s%d", name, idx)
	}
}
