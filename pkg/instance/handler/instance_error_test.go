package instance_handler

import (
	"errors"
	"fmt"
	whatsmeow_service "github.com/evolution-foundation/evolution-go/pkg/whatsmeow/service"
	"net/http"
	"testing"
)

func TestBusyActionsReturnConflictNotServerFailure(t *testing.T) {
	for _, err := range []error{whatsmeow_service.ErrClientBusy, fmt.Errorf("start: %w", whatsmeow_service.ErrClientBusy)} {
		if instanceErrorStatus(err, http.StatusInternalServerError) != http.StatusConflict {
			t.Fatal("busy action must return 409")
		}
	}
	if instanceErrorStatus(errors.New("database unavailable"), http.StatusInternalServerError) != http.StatusInternalServerError {
		t.Fatal("unexpected errors must remain errors")
	}
}
