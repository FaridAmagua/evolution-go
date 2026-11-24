package instance_model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResumeIntentCannotBeReadOrChangedThroughPublicJSON(t *testing.T) {
	resume := true
	instance := Instance{ResumeOnStartup: &resume}
	data, err := json.Marshal(instance)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(data)), "resume") {
		t.Fatal("server-owned intent leaked through public JSON")
	}
	if err := json.Unmarshal([]byte(`{"resume_on_startup":false,"ResumeOnStartup":false}`), &instance); err != nil {
		t.Fatal(err)
	}
	if instance.ResumeOnStartup == nil || !*instance.ResumeOnStartup {
		t.Fatal("public JSON changed the server-owned resume state")
	}
}
