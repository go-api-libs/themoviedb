package merge

func mergeString(a, b string) string {
	const placeholder = "TODO"

	if a == "" || a == placeholder {
		return b // overwrite a with b (which might also be empty or placeholder)
	}

	return a // keep a
}
