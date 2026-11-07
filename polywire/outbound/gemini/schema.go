package gemini

import (
	"bytes"
	"encoding/json"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func geminiSchemaParameters(raw json.RawMessage) (map[string]any, error) {
	schema, conversionErr := model.ConvertJSONSchemaToGemini(raw)
	if schema == nil {
		return nil, conversionErr
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var parameters map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&parameters); err != nil {
		return nil, err
	}
	return parameters, conversionErr
}
