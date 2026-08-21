package agentspec_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/prav-j/dark-factory/internal/agentspec"
)

// TestGoldenFixturesMatchJSONSchema validates every golden fixture against
// the published JSON Schema (api/agentspec.v1.schema.json). The schema is the
// external contract for GitOps users; the Go structs are the runtime truth —
// this test keeps them in lockstep.
func TestGoldenFixturesMatchJSONSchema(t *testing.T) {
	schemaRaw, err := os.ReadFile("../../api/agentspec.v1.schema.json")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	schema, err := jsonschema.CompileString("agentspec.v1.json", string(schemaRaw))
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}

	fixtures, err := os.ReadDir("testdata/golden")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no golden fixtures found")
	}
	for _, f := range fixtures {
		t.Run(f.Name(), func(t *testing.T) {
			raw, err := os.ReadFile("testdata/golden/" + f.Name())
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			doc, err := agentspec.Parse(raw) // must also pass the strict Go parser
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			// Validate the full document (envelope + spec) against the schema.
			raw2, err := json.Marshal(doc)
			if err != nil {
				t.Fatalf("marshal doc: %v", err)
			}
			var v interface{}
			if err := json.Unmarshal(raw2, &v); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if err := schema.Validate(v); err != nil {
				t.Fatalf("fixture violates JSON Schema: %v", err)
			}
		})
	}
}
