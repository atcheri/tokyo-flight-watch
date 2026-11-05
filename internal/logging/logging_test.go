package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, "json", "warn").Info("hidden")
	New(&buf, "JSON", "warn").Warn("shown", "watch", 1)
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil || line["msg"] != "shown" || line["watch"] != 1.0 {
		t.Fatalf("json output %q: %v", buf.String(), err)
	}

	buf.Reset()
	New(&buf, "", "nonsense").Info("scan done", "kind", "any", "date_pairs", 0)
	if out := buf.String(); !strings.Contains(out, `msg="scan done" kind=any date_pairs=0`) {
		t.Fatalf("text output %q", out)
	}
}
