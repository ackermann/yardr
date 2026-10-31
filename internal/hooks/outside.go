package hooks

// OutsiderKey is the field a source's normalize command sets to yes on a
// delivery whose text was written by someone who is none of the source's own
// people. Who those are is the source's pack to say, and no core code judges
// it: the yard marks such a delivery as information, not instructions, and
// leaves its text out of the notes it writes.
const OutsiderKey = "outsider"

// Outsider reports whether data, a delivery as event data, says its text is
// an outsider's: its fields hold yes under OutsiderKey. A delivery whose
// source says nothing of its writer (a run of CI, a push) is not.
func Outsider(data map[string]any) bool {
	return fieldValue(data, OutsiderKey) == "yes"
}

// fieldValue is the first value of key in data's fields, "" for none. A
// stored event has them as []any, an envelope built in process as []string.
func fieldValue(data map[string]any, key string) string {
	var values []string
	switch fields := data["fields"].(type) {
	case map[string]any:
		switch v := fields[key].(type) {
		case []any:
			for _, e := range v {
				if s, ok := e.(string); ok {
					values = append(values, s)
				}
			}
		case []string:
			values = v
		}
	case map[string][]string:
		values = fields[key]
	}
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
