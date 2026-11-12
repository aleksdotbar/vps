package cloudflareflow

import (
	"errors"
	"net/http"
	"testing"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/zones"
)

func TestRejectionSeparatesDefiniteRefusalsFromOutages(t *testing.T) {
	for status, definite := range map[int]bool{
		http.StatusBadRequest: true, http.StatusUnauthorized: true, http.StatusForbidden: true, http.StatusNotFound: true,
		http.StatusTooManyRequests: false, http.StatusRequestTimeout: false, http.StatusInternalServerError: false, http.StatusBadGateway: false,
	} {
		err := rejection(&cloudflare.Error{StatusCode: status}, "guidance")
		var rejected RejectedError
		if errors.As(err, &rejected) != definite || definite && (rejected.Guidance != "guidance" || !errors.Is(err, ErrScope)) || !definite && !errors.Is(err, ErrProvider) {
			t.Fatalf("status %d classified as %v", status, err)
		}
	}
	if !errors.Is(rejection(ErrProvider, "guidance"), ErrProvider) || !errors.Is(rejection(nil, "guidance"), ErrProvider) {
		t.Fatal("transport failure was treated as a refusal")
	}
}

func TestZoneProblemExplainsUnusableZones(t *testing.T) {
	id, account := "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"
	good := zones.Zone{ID: id, Name: "example.com", Status: zones.ZoneStatusActive, Type: zones.TypeFull}
	good.Account.ID = account
	if problem := zoneProblem(&good, id, account); problem != "" {
		t.Fatalf("eligible zone rejected: %s", problem)
	}
	for name, change := range map[string]func(*zones.Zone){
		"other account": func(zone *zones.Zone) { zone.Account.ID = id },
		"pending":       func(zone *zones.Zone) { zone.Status = zones.ZoneStatusPending },
		"partial":       func(zone *zones.Zone) { zone.Type = zones.TypePartial },
		"paused":        func(zone *zones.Zone) { zone.Paused = true },
		"wrong id":      func(zone *zones.Zone) { zone.ID = account },
	} {
		zone := good
		change(&zone)
		if zoneProblem(&zone, id, account) == "" {
			t.Errorf("%s zone accepted", name)
		}
	}
}
