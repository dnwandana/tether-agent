package tools

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// maxKnownArgs is the maximum number of known argument names in the note.
const maxKnownArgs = 30

// unknownArgs returns the top-level keys of args that the top-level properties
// of schema do not list, sorted, and the property names in schema order. The
// check looks only at the top level. ok is false when the check does not apply:
// args is not a JSON object, the schema has no properties object, or the schema
// lets other keys in with an additionalProperties value other than false.
func unknownArgs(args, schema json.RawMessage) (unknown, known []string, ok bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil || fields == nil {
		return nil, nil, false
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(schema, &top); err != nil {
		return nil, nil, false
	}
	if extra, found := top["additionalProperties"]; found && string(bytes.TrimSpace(extra)) != "false" {
		return nil, nil, false
	}
	props, found := top["properties"]
	if !found {
		return nil, nil, false
	}
	known, ok = propertyNames(props)
	if !ok {
		return nil, nil, false
	}
	for key := range fields {
		if !hasFoldName(known, key) {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	return unknown, known, true
}

// propertyNames returns the keys of the JSON object props in their order. A map
// loses that order, so it reads the tokens. ok is false when props is not an
// object.
func propertyNames(props json.RawMessage) (names []string, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(props))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, false
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		name, isKey := tok.(string)
		if !isKey {
			return nil, false
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, false
		}
		names = append(names, name)
	}
	return names, true
}

// hasFoldName reports whether names holds key in any letter case.
// json.Unmarshal matches a field in this way, so such a key is not unknown.
func hasFoldName(names []string, key string) bool {
	for _, n := range names {
		if strings.EqualFold(n, key) {
			return true
		}
	}
	return false
}

// unknownArgsNote returns the note for the model that names the unknown
// arguments and the known arguments. The list of known arguments stops after
// maxKnownArgs names.
func unknownArgsNote(unknown, known []string) string {
	list := known
	if len(list) > maxKnownArgs {
		list = append(append([]string{}, list[:maxKnownArgs]...), "…")
	}
	return "Tether ignored these unknown arguments: " + strings.Join(unknown, ", ") +
		". Put each argument at the top level of the call, and use only the known names. The known arguments are: " +
		strings.Join(list, ", ") + "."
}
