package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sync"
)

const (
	urlL1Wind   = "https://services.swpc.noaa.gov/json/rtsw/rtsw_wind_1m.json"
	urlL1Mag    = "https://services.swpc.noaa.gov/json/rtsw/rtsw_mag_1m.json"
	urlDSN      = "https://eyes.nasa.gov/dsn/data/dsn.xml"
	urlFireball = "https://ssd-api.jpl.nasa.gov/fireball.api?vel-comp=1&limit=5"
	urlScout    = "https://ssd-api.jpl.nasa.gov/scout.api"
)

// Telemetry holds a point-in-time snapshot from all three cosmic domains.
type Telemetry struct {
	L1        []L1Sample    `json:"l1"`
	L1Mag     []L1MagSample `json:"l1_mag,omitempty"`
	DSNTime   string        `json:"dsn_timestamp"`
	DSN       []DSNDish     `json:"dsn"`
	Fireballs []Fireball    `json:"fireballs"`
	Scout     []ScoutObject `json:"scout"`
}

// L1Sample represents 1-minute solar wind plasma readings at the L1 Lagrange point.
type L1Sample struct {
	TimeTag     string   `json:"time_tag"`
	Source      string   `json:"source"`
	Active      bool     `json:"active"`
	Density     *float64 `json:"proton_density"`
	Temperature *float64 `json:"proton_temperature"`
	Speed       *float64 `json:"proton_speed"`
	VxGSE       *float64 `json:"proton_vx_gse,omitempty"`
	VyGSE       *float64 `json:"proton_vy_gse,omitempty"`
	VzGSE       *float64 `json:"proton_vz_gse,omitempty"`
}

// L1MagSample represents 1-minute interplanetary magnetic field vectors at L1.
type L1MagSample struct {
	TimeTag string   `json:"time_tag"`
	Source  string   `json:"source"`
	Active  bool     `json:"active"`
	Bt      *float64 `json:"bt"`
	BxGSE   *float64 `json:"bx_gse"`
	ByGSE   *float64 `json:"by_gse"`
	BzGSE   *float64 `json:"bz_gse"`
}

// DSNDish represents a Deep Space Network antenna and its live RF signals.
type DSNDish struct {
	Station   string      `json:"station"`
	Name      string      `json:"dish"`
	Azimuth   string      `json:"azimuth"`
	Elevation string      `json:"elevation"`
	WindSpeed string      `json:"wind_speed,omitempty"`
	Signals   []DSNSignal `json:"signals,omitempty"`
}

// DSNSignal captures downlink/uplink carrier power (dBm) and telemetry rate.
type DSNSignal struct {
	Dir        string `json:"dir"`
	Spacecraft string `json:"spacecraft"`
	Band       string `json:"band"`
	PowerdBm   string `json:"power_dbm"`
	DataRate   string `json:"data_rate"`
	RTLT       string `json:"rtlt,omitempty"`
}

// Fireball represents a JPL CNEOS bolide event with 3D velocity vector (km/s).
type Fireball struct {
	Date   string `json:"date"`
	Energy string `json:"energy"`
	Vel    string `json:"vel"`
	Vx     string `json:"vx"`
	Vy     string `json:"vy"`
	Vz     string `json:"vz"`
	Alt    string `json:"alt"`
}

// ScoutObject represents a newly detected near-Earth object from JPL Scout.
type ScoutObject struct {
	Object string `json:"object"`
	TEphem string `json:"t_ephem"`
	VInf   string `json:"v_inf"`
	Rate   string `json:"rate"`
	RA     string `json:"ra"`
	Dec    string `json:"dec"`
	RMS    string `json:"rms"`
}

// Harvest queries NOAA SWPC (L1), NASA DSN, and JPL CNEOS concurrently.
func Harvest(ctx context.Context, client *http.Client) (*Telemetry, error) {
	var (
		t    Telemetry
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	recordErr := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}

	wg.Add(5)
	go func() {
		defer wg.Done()
		samples, err := fetchL1Wind(ctx, client)
		if err != nil {
			recordErr(fmt.Errorf("l1 wind: %w", err))
			return
		}
		mu.Lock()
		t.L1 = samples
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		mag, err := fetchL1Mag(ctx, client)
		if err != nil {
			recordErr(fmt.Errorf("l1 mag: %w", err))
			return
		}
		mu.Lock()
		t.L1Mag = mag
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		ts, dishes, err := fetchDSN(ctx, client)
		if err != nil {
			recordErr(fmt.Errorf("dsn: %w", err))
			return
		}
		mu.Lock()
		t.DSNTime = ts
		t.DSN = dishes
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		fb, err := fetchFireballs(ctx, client)
		if err != nil {
			recordErr(fmt.Errorf("fireball: %w", err))
			return
		}
		mu.Lock()
		t.Fireballs = fb
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		sc, err := fetchScout(ctx, client)
		if err != nil {
			recordErr(fmt.Errorf("scout: %w", err))
			return
		}
		mu.Lock()
		t.Scout = sc
		mu.Unlock()
	}()

	wg.Wait()
	if len(t.L1) == 0 && len(t.DSN) == 0 && len(t.Fireballs) == 0 {
		return nil, fmt.Errorf("all cosmic sources failed: %v", errs)
	}
	return &t, nil
}

func getBody(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "l1rnd/1.0 (Heliospheric-Entropy-Beacon)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

func fetchL1Wind(ctx context.Context, client *http.Client) ([]L1Sample, error) {
	b, err := getBody(ctx, client, urlL1Wind)
	if err != nil {
		return nil, err
	}
	var raw []L1Sample
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	var out []L1Sample
	for _, s := range raw {
		if s.Density != nil || s.Temperature != nil || s.Speed != nil {
			out = append(out, s)
			if len(out) >= 6 {
				break
			}
		}
	}
	return out, nil
}

func fetchL1Mag(ctx context.Context, client *http.Client) ([]L1MagSample, error) {
	b, err := getBody(ctx, client, urlL1Mag)
	if err != nil {
		return nil, err
	}
	var raw []L1MagSample
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	var out []L1MagSample
	for _, s := range raw {
		if s.Bt != nil {
			out = append(out, s)
			if len(out) >= 4 {
				break
			}
		}
	}
	return out, nil
}

func fetchDSN(ctx context.Context, client *http.Client) (string, []DSNDish, error) {
	b, err := getBody(ctx, client, urlDSN)
	if err != nil {
		return "", nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	var (
		station string
		dsnTime string
		dishes  []DSNDish
	)
	type xmlSig struct {
		Active     string `xml:"active,attr"`
		Band       string `xml:"band,attr"`
		Power      string `xml:"power,attr"`
		DataRate   string `xml:"dataRate,attr"`
		Spacecraft string `xml:"spacecraft,attr"`
	}
	type xmlTarget struct {
		Name string `xml:"name,attr"`
		RTLT string `xml:"rtlt,attr"`
	}
	type xmlDish struct {
		Name      string      `xml:"name,attr"`
		Azimuth   string      `xml:"azimuthAngle,attr"`
		Elevation string      `xml:"elevationAngle,attr"`
		WindSpeed string      `xml:"windSpeed,attr"`
		Down      []xmlSig    `xml:"downSignal"`
		Up        []xmlSig    `xml:"upSignal"`
		Targets   []xmlTarget `xml:"target"`
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "station":
			for _, a := range se.Attr {
				if a.Name.Local == "name" {
					station = a.Value
				}
			}
		case "dish":
			var xd xmlDish
			if err := dec.DecodeElement(&xd, &se); err != nil {
				return "", nil, err
			}
			rtltBySC := make(map[string]string, len(xd.Targets))
			for _, tgt := range xd.Targets {
				rtltBySC[tgt.Name] = tgt.RTLT
			}
			d := DSNDish{
				Station:   station,
				Name:      xd.Name,
				Azimuth:   xd.Azimuth,
				Elevation: xd.Elevation,
				WindSpeed: xd.WindSpeed,
			}
			for _, s := range xd.Down {
				if s.Power != "" && s.Power != "0" {
					d.Signals = append(d.Signals, DSNSignal{
						Dir:        "down",
						Spacecraft: s.Spacecraft,
						Band:       s.Band,
						PowerdBm:   s.Power,
						DataRate:   s.DataRate,
						RTLT:       rtltBySC[s.Spacecraft],
					})
				}
			}
			for _, s := range xd.Up {
				if s.Power != "" && s.Power != "0" {
					d.Signals = append(d.Signals, DSNSignal{
						Dir:        "up",
						Spacecraft: s.Spacecraft,
						Band:       s.Band,
						PowerdBm:   s.Power,
						DataRate:   s.DataRate,
						RTLT:       rtltBySC[s.Spacecraft],
					})
				}
			}
			dishes = append(dishes, d)
		case "timestamp":
			var ts string
			if err := dec.DecodeElement(&ts, &se); err == nil {
				dsnTime = ts
			}
		}
	}
	return dsnTime, dishes, nil
}

func fetchFireballs(ctx context.Context, client *http.Client) ([]Fireball, error) {
	b, err := getBody(ctx, client, urlFireball)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Fields []string        `json:"fields"`
		Data   [][]interface{} `json:"data"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, err
	}
	idx := make(map[string]int, len(resp.Fields))
	for i, f := range resp.Fields {
		idx[f] = i
	}
	get := func(row []interface{}, k string) string {
		if i, ok := idx[k]; ok && i < len(row) && row[i] != nil {
			return fmt.Sprint(row[i])
		}
		return ""
	}
	var out []Fireball
	for _, row := range resp.Data {
		out = append(out, Fireball{
			Date:   get(row, "date"),
			Energy: get(row, "energy"),
			Vel:    get(row, "vel"),
			Vx:     get(row, "vx"),
			Vy:     get(row, "vy"),
			Vz:     get(row, "vz"),
			Alt:    get(row, "alt"),
		})
	}
	return out, nil
}

func fetchScout(ctx context.Context, client *http.Client) ([]ScoutObject, error) {
	b, err := getBody(ctx, client, urlScout)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []struct {
			ObjectName string `json:"objectName"`
			TEphem     string `json:"tEphem"`
			VInf       string `json:"vInf"`
			Rate       string `json:"rate"`
			RA         string `json:"ra"`
			Dec        string `json:"dec"`
			RMSN       string `json:"rmsN"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, err
	}
	var out []ScoutObject
	for i, d := range resp.Data {
		if i >= 6 {
			break
		}
		out = append(out, ScoutObject{
			Object: d.ObjectName,
			TEphem: d.TEphem,
			VInf:   d.VInf,
			Rate:   d.Rate,
			RA:     d.RA,
			Dec:    d.Dec,
			RMS:    d.RMSN,
		})
	}
	return out, nil
}
