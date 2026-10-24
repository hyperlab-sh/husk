package manifest

import (
	"encoding/json"
	"io"
)

// WriteDiagnostics writes the converter's stderr JSON lines. Informational
// notes intentionally have no code and are ignored by the host converter seam.
func WriteDiagnostics(w io.Writer, problems []Problem, notes []string) error {
	encoder := json.NewEncoder(w)
	for _, problem := range problems {
		if err := encoder.Encode(problem); err != nil {
			return err
		}
	}
	for _, note := range notes {
		if err := encoder.Encode(struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}{"info", note}); err != nil {
			return err
		}
	}
	return nil
}
