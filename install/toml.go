package install

import (
	"github.com/pelletier/go-toml/v2"
)

func mergeTOMLDocument(rendered, existing []byte) []byte {
	var want, found map[string]any
	if err := toml.Unmarshal(rendered, &want); err != nil {
		return rendered
	}
	if err := toml.Unmarshal(existing, &found); err != nil {
		return rendered
	}
	merged := mergeTOMLMaps(want, found)
	out, err := toml.Marshal(merged)
	if err != nil {
		return rendered
	}
	return out
}

func normalizeTOMLDocument(raw []byte) []byte {
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return raw
	}
	out, err := toml.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}

func mergeTOMLMaps(rendered, existing map[string]any) map[string]any {
	out := cloneTOMLMap(existing)
	for key, value := range rendered {
		if existingValue, ok := out[key]; ok {
			if renderedMap, ok := value.(map[string]any); ok {
				if existingMap, ok := existingValue.(map[string]any); ok {
					out[key] = mergeTOMLMaps(renderedMap, existingMap)
					continue
				}
			}
		}
		out[key] = value
	}
	return out
}

func cloneTOMLMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
