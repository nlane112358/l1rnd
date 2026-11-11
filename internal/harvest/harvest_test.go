package harvest_test

import (
	"encoding/json"
	"testing"

	"l1rnd/internal/harvest"
)

func TestTelemetryJSONMarshaling(t *testing.T) {
	dens := 2.5
	temp := 150000.0
	spd := 450.0

	original := harvest.Telemetry{
		L1: []harvest.L1Sample{{
			TimeTag:     "2026-11-11T12:00:00",
			Source:      "SOLAR1",
			Active:      true,
			Density:     &dens,
			Temperature: &temp,
			Speed:       &spd,
		}},
		DSNTime: "1794134265000",
		DSN: []harvest.DSNDish{{
			Station:   "gdscc",
			Name:      "DSS14",
			Azimuth:   "45.0",
			Elevation: "60.0",
			Signals: []harvest.DSNSignal{{
				Dir:        "down",
				Spacecraft: "VGR2",
				Band:       "X",
				PowerdBm:   "-155.0",
				DataRate:   "160",
			}},
		}},
		Fireballs: []harvest.Fireball{{
			Date:   "2026-11-01 04:00:00",
			Energy: "3.2",
			Vel:    "18.5",
		}},
		Scout: []harvest.ScoutObject{{
			Object: "NEO2026",
			VInf:   "12.4",
		}},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var decoded harvest.Telemetry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if len(decoded.L1) != 1 || decoded.L1[0].Source != "SOLAR1" {
		t.Errorf("L1 mismatch: %+v", decoded.L1)
	}
	if decoded.DSNTime != "1794134265000" {
		t.Errorf("DSNTime mismatch: %s", decoded.DSNTime)
	}
	if len(decoded.DSN) != 1 || decoded.DSN[0].Name != "DSS14" {
		t.Errorf("DSN mismatch: %+v", decoded.DSN)
	}
	if len(decoded.Fireballs) != 1 || decoded.Fireballs[0].Date != "2026-11-01 04:00:00" {
		t.Errorf("Fireballs mismatch: %+v", decoded.Fireballs)
	}
	if len(decoded.Scout) != 1 || decoded.Scout[0].Object != "NEO2026" {
		t.Errorf("Scout mismatch: %+v", decoded.Scout)
	}
}
