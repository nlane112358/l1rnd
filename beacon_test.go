package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPulseVerifyAndChain(t *testing.T) {
	dens := 1.93
	temp := 307087.0
	spd := 698.8
	bt := 5.26

	tel := &Telemetry{
		L1: []L1Sample{{
			TimeTag:     "2026-11-08T10:34:00",
			Source:      "SOLAR1",
			Active:      true,
			Density:     &dens,
			Temperature: &temp,
			Speed:       &spd,
		}},
		L1Mag: []L1MagSample{{
			TimeTag: "2026-11-08T10:34:00",
			Source:  "SOLAR1",
			Active:  true,
			Bt:      &bt,
		}},
		DSNTime: "1794134265000",
		DSN: []DSNDish{{
			Station:   "mdscc",
			Name:      "DSS63",
			Azimuth:   "103",
			Elevation: "33",
			Signals: []DSNSignal{{
				Dir:        "down",
				Spacecraft: "VGR1",
				Band:       "X",
				PowerdBm:   "-160",
				DataRate:   "160",
				RTLT:       "173000",
			}},
		}},
		Fireballs: []Fireball{{
			Date:   "2026-10-04 03:14:45",
			Energy: "5.4",
			Vel:    "13.8",
			Vx:     "14.8",
			Vy:     "2.9",
			Vz:     "2.2",
			Alt:    "26.9",
		}},
		Scout: []ScoutObject{{
			Object: "6L71921",
			TEphem: "2026-11-08 10:30",
			VInf:   "28.0",
			Rate:   "2.5",
			RA:     "02:41",
			Dec:    "-19",
			RMS:    "0.01",
		}},
	}

	ts := time.Date(2026, 11, 8, 10, 35, 0, 0, time.UTC)
	p1 := NewPulse(1, ts, GenesisSeed, tel)
	if err := p1.Verify(); err != nil {
		t.Fatalf("p1 verify failed: %v", err)
	}

	p2 := NewPulse(2, ts.Add(time.Minute), p1.Seed, tel)
	if err := p2.Verify(); err != nil {
		t.Fatalf("p2 verify failed: %v", err)
	}
	if p1.Seed == p2.Seed {
		t.Fatalf("chained pulses produced identical seeds")
	}

	var stream bytes.Buffer
	_ = json.NewEncoder(&stream).Encode(p1)
	_ = json.NewEncoder(&stream).Encode(p2)

	var errBuf bytes.Buffer
	if err := runVerify(&stream, &errBuf); err != nil {
		t.Fatalf("runVerify failed on valid chain: %v", err)
	}

	// Tamper with telemetry and ensure Verify rejects it.
	tampered := *p1
	tampered.Seed = strings.ReplaceAll(p1.Seed, "a", "b")
	if err := tampered.Verify(); err == nil {
		t.Fatalf("expected tampered pulse to fail verification")
	}
}

func TestStreamRaffleAndWorldDeterminism(t *testing.T) {
	p := NewPulse(1, time.Unix(1700000000, 0).UTC(), GenesisSeed, &Telemetry{DSNTime: "12345"})
	seed, err := p.SeedBytes()
	if err != nil {
		t.Fatal(err)
	}

	r1, _ := NewStreamReader(seed, false)
	r2, _ := NewStreamReader(seed, false)
	b1 := make([]byte, 128)
	b2 := make([]byte, 128)
	_, _ = r1.Read(b1)
	_, _ = r2.Read(b2)
	if !bytes.Equal(b1, b2) {
		t.Fatalf("StreamReader not deterministic")
	}

	entries := []string{"io", "europa", "ganymede", "callisto"}
	w1 := DrawRaffle(seed, entries, 2)
	w2 := DrawRaffle(seed, entries, 2)
	if len(w1) != 2 || w1[0] != w2[0] || w1[1] != w2[1] {
		t.Fatalf("DrawRaffle mismatch: %v vs %v", w1, w2)
	}

	world1 := GenerateWorld(seed)
	world2 := GenerateWorld(seed)
	if world1 != world2 {
		t.Fatalf("GenerateWorld not deterministic: %+v vs %+v", world1, world2)
	}
}
