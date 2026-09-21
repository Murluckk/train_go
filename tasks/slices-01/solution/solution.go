package solution

// Dedup возвращает значения xs без повторов, в порядке первого появления.
func Dedup(xs []string) []string {
	if len(xs) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(xs))
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if _, ok := seen[x]; ok {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	return out
}
