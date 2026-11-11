package beacon

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"l1rnd/internal/harvest"
)

const GenesisSeed = "0000000000000000000000000000000000000000000000000000000000000000"

// Pulse is a verifiable, hash-chained cosmic entropy beacon frame.
type Pulse struct {
	Version   string             `json:"version"`
	Seq       uint64             `json:"seq"`
	Timestamp string             `json:"timestamp"`
	PrevSeed  string             `json:"prev_seed"`
	Seed      string             `json:"seed"`
	Preimage  string             `json:"preimage"`
	Telemetry *harvest.Telemetry `json:"telemetry"`
}

// NewPulse constructs a verifiable Pulse from harvested telemetry and the previous seed.
func NewPulse(seq uint64, ts time.Time, prevSeed string, t *harvest.Telemetry) *Pulse {
	if prevSeed == "" {
		prevSeed = GenesisSeed
	}
	p := &Pulse{
		Version:   "l1rnd/v1",
		Seq:       seq,
		Timestamp: ts.UTC().Format(time.RFC3339),
		PrevSeed:  prevSeed,
		Telemetry: t,
	}
	p.Preimage = p.CanonicalPreimage()
	sum := sha256.Sum256([]byte(p.Preimage))
	p.Seed = hex.EncodeToString(sum[:])
	return p
}

// CanonicalPreimage serializes the pulse header and telemetry into a deterministic byte string.
func (p *Pulse) CanonicalPreimage() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|seq=%d|ts=%s|prev=%s\n", p.Version, p.Seq, p.Timestamp, p.PrevSeed)
	if p.Telemetry == nil {
		return b.String()
	}
	for _, s := range p.Telemetry.L1 {
		fmt.Fprintf(&b, "l1:wind|%s|%s|active=%t|n=%s|T=%s|v=%s|vx=%s|vy=%s|vz=%s\n",
			s.TimeTag, s.Source, s.Active,
			fmtFloat(s.Density), fmtFloat(s.Temperature), fmtFloat(s.Speed),
			fmtFloat(s.VxGSE), fmtFloat(s.VyGSE), fmtFloat(s.VzGSE))
	}
	for _, m := range p.Telemetry.L1Mag {
		fmt.Fprintf(&b, "l1:mag|%s|%s|active=%t|bt=%s|bx=%s|by=%s|bz=%s\n",
			m.TimeTag, m.Source, m.Active,
			fmtFloat(m.Bt), fmtFloat(m.BxGSE), fmtFloat(m.ByGSE), fmtFloat(m.BzGSE))
	}
	fmt.Fprintf(&b, "dsn:ts|%s\n", p.Telemetry.DSNTime)
	for _, d := range p.Telemetry.DSN {
		fmt.Fprintf(&b, "dsn:dish|%s|%s|az=%s|el=%s|wind=%s\n",
			d.Station, d.Name, d.Azimuth, d.Elevation, d.WindSpeed)
		for _, sig := range d.Signals {
			fmt.Fprintf(&b, "dsn:sig|%s|%s|%s|%s|pwr=%s|rate=%s|rtlt=%s\n",
				d.Name, sig.Dir, sig.Spacecraft, sig.Band, sig.PowerdBm, sig.DataRate, sig.RTLT)
		}
	}
	for _, fb := range p.Telemetry.Fireballs {
		fmt.Fprintf(&b, "cneos:fireball|%s|e=%s|v=%s|vx=%s|vy=%s|vz=%s|alt=%s\n",
			fb.Date, fb.Energy, fb.Vel, fb.Vx, fb.Vy, fb.Vz, fb.Alt)
	}
	for _, sc := range p.Telemetry.Scout {
		fmt.Fprintf(&b, "cneos:scout|%s|%s|vinf=%s|rate=%s|ra=%s|dec=%s|rms=%s\n",
			sc.Object, sc.TEphem, sc.VInf, sc.Rate, sc.RA, sc.Dec, sc.RMS)
	}
	return b.String()
}

// Verify checks both canonical preimage reconstruction and the SHA-256 digest.
func (p *Pulse) Verify() error {
	expectedPre := p.CanonicalPreimage()
	if p.Preimage != expectedPre {
		return errors.New("preimage mismatch with canonical telemetry")
	}
	sum := sha256.Sum256([]byte(p.Preimage))
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(p.Seed, got) {
		return fmt.Errorf("seed mismatch: expected %s, got %s", got, p.Seed)
	}
	return nil
}

// SeedBytes decodes the 256-bit hex seed into a 32-byte array.
func (p *Pulse) SeedBytes() ([32]byte, error) {
	var out [32]byte
	raw, err := hex.DecodeString(p.Seed)
	if err != nil || len(raw) != 32 {
		return out, fmt.Errorf("invalid 32-byte hex seed: %q", p.Seed)
	}
	copy(out[:], raw)
	return out, nil
}

// VerifyChain reads a sequence of Pulse JSON objects from r, verifying each pulse's
// canonical preimage and cryptographic hash chain continuity.
// It returns the number of verified pulses and the head seed.
func VerifyChain(r io.Reader) (int, string, error) {
	dec := json.NewDecoder(r)
	var (
		count    int
		prevSeed string
	)
	for {
		var p Pulse
		if err := dec.Decode(&p); err != nil {
			if err == io.EOF {
				break
			}
			return 0, "", err
		}
		if err := p.Verify(); err != nil {
			return 0, "", fmt.Errorf("pulse #%d: %w", p.Seq, err)
		}
		if prevSeed != "" && p.PrevSeed != prevSeed {
			return 0, "", fmt.Errorf("pulse #%d: broken chain: prev_seed=%s, expected=%s", p.Seq, p.PrevSeed, prevSeed)
		}
		prevSeed = p.Seed
		count++
	}
	if count == 0 {
		return 0, "", fmt.Errorf("no pulse JSON found")
	}
	return count, prevSeed, nil
}

func fmtFloat(v *float64) string {
	if v == nil {
		return "null"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

// StreamReader expands a 32-byte cosmic seed into an arbitrary-length byte stream
// using SHA-256 in counter mode: block_i = SHA256(key || uint64_be(i)).
type StreamReader struct {
	key     [32]byte
	counter uint64
	buf     [32]byte
	off     int
}

// NewStreamReader creates a deterministic byte stream from seed.
// If mixOS is true, 32 bytes from crypto/rand are hashed together with seed.
func NewStreamReader(seed [32]byte, mixOS bool) (*StreamReader, error) {
	key := seed
	if mixOS {
		var osBytes [32]byte
		if _, err := io.ReadFull(rand.Reader, osBytes[:]); err != nil {
			return nil, err
		}
		h := sha256.New()
		h.Write(seed[:])
		h.Write(osBytes[:])
		copy(key[:], h.Sum(nil))
	}
	return &StreamReader{key: key, off: 32}, nil
}

func (r *StreamReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if r.off >= 32 {
			var blk [40]byte
			copy(blk[:32], r.key[:])
			binary.BigEndian.PutUint64(blk[32:], r.counter)
			r.buf = sha256.Sum256(blk[:])
			r.counter++
			r.off = 0
		}
		copied := copy(p[n:], r.buf[r.off:])
		r.off += copied
		n += copied
	}
	return n, nil
}

func (r *StreamReader) Uint64() uint64 {
	var b [8]byte
	_, _ = r.Read(b[:])
	return binary.BigEndian.Uint64(b[:])
}

// DrawRaffle performs a verifiable Fisher-Yates shuffle seeded by the pulse.
func DrawRaffle(seed [32]byte, items []string, k int) []string {
	pool := make([]string, 0, len(items))
	for _, it := range items {
		if s := strings.TrimSpace(it); s != "" {
			pool = append(pool, s)
		}
	}
	if len(pool) == 0 {
		return nil
	}
	rng, _ := NewStreamReader(seed, false)
	for i := len(pool) - 1; i > 0; i-- {
		j := int(rng.Uint64() % uint64(i+1))
		pool[i], pool[j] = pool[j], pool[i]
	}
	if k <= 0 || k > len(pool) {
		k = len(pool)
	}
	return pool[:k]
}

// World holds deterministic procedural world parameters derived from a cosmic seed.
type World struct {
	Designation   string  `json:"designation"`
	SpectralClass string  `json:"spectral_class"`
	StarTempK     int     `json:"star_temp_k"`
	GravityG      float64 `json:"gravity_g"`
	SurfaceTempK  int     `json:"surface_temp_k"`
	OceanFraction float64 `json:"ocean_fraction"`
	Atmosphere    string  `json:"atmosphere"`
	PrimaryBiome  string  `json:"primary_biome"`
	TerrainSeed   uint64  `json:"terrain_seed"`
	PulseSeed     string  `json:"pulse_seed"`
}

// GenerateWorld deterministically maps a 32-byte cosmic seed to a procedural world.
func GenerateWorld(seed [32]byte) World {
	rng, _ := NewStreamReader(seed, false)
	classes := []string{"O", "B", "A", "F", "G", "K", "M"}
	atms := []string{"N2-O2breathable", "CO2-dense", "CH4-N2haze", "Ar-CO2thin", "H2-Heprimordial", "SO2-volcanic"}
	biomes := []string{"Pelagic Archipelago", "Basalt Shieldlands", "Cryo-Methane Dunes", "Bioluminescent Karst", "Hyper-Arid Regolith", "Subglacial Ocean", "Temperate Steppe"}

	cIdx := int(rng.Uint64() % uint64(len(classes)))
	starTemps := []int{32000, 16000, 8500, 6600, 5778, 4400, 3100}
	starTemp := starTemps[cIdx] + int(rng.Uint64()%900) - 450
	grav := 0.35 + float64(rng.Uint64()%240)/100.0
	surfTemp := 140 + int(rng.Uint64()%260)
	ocean := float64(rng.Uint64()%101) / 100.0
	tSeed := rng.Uint64()

	return World{
		Designation:   fmt.Sprintf("L1-%s-%04X", classes[cIdx], tSeed&0xFFFF),
		SpectralClass: fmt.Sprintf("%s%dV", classes[cIdx], rng.Uint64()%10),
		StarTempK:     starTemp,
		GravityG:      grav,
		SurfaceTempK:  surfTemp,
		OceanFraction: ocean,
		Atmosphere:    atms[rng.Uint64()%uint64(len(atms))],
		PrimaryBiome:  biomes[rng.Uint64()%uint64(len(biomes))],
		TerrainSeed:   tSeed,
		PulseSeed:     hex.EncodeToString(seed[:]),
	}
}

// FormatSummary writes a concise Unix-style telemetry diagnostic report.
func FormatSummary(w io.Writer, p *Pulse) {
	fmt.Fprintf(w, "pulse #%d @ %s\n", p.Seq, p.Timestamp)
	if p.Telemetry != nil {
		for i, s := range p.Telemetry.L1 {
			if i >= 2 {
				break
			}
			fmt.Fprintf(w, "  l1/wind   %-6s %s  n=%s p/cm3  T=%s K  v=%s km/s\n",
				s.Source, s.TimeTag, fmtFloat(s.Density), fmtFloat(s.Temperature), fmtFloat(s.Speed))
		}
		if len(p.Telemetry.L1Mag) > 0 {
			m := p.Telemetry.L1Mag[0]
			fmt.Fprintf(w, "  l1/mag    %-6s %s  Bt=%s nT  B=(%s, %s, %s) nT\n",
				m.Source, m.TimeTag, fmtFloat(m.Bt), fmtFloat(m.BxGSE), fmtFloat(m.ByGSE), fmtFloat(m.BzGSE))
		}
		seenPerStation := make(map[string]int)
		for _, d := range p.Telemetry.DSN {
			for _, sig := range d.Signals {
				if sig.Dir == "down" && seenPerStation[d.Station] < 2 {
					fmt.Fprintf(w, "  dsn/%-5s %-5s  %-5s (%-2s) %4s dBm  %8s bps  az=%s el=%s\n",
						d.Station, d.Name, sig.Spacecraft, sig.Band, sig.PowerdBm, sig.DataRate, d.Azimuth, d.Elevation)
					seenPerStation[d.Station]++
				}
			}
		}
		if len(p.Telemetry.Fireballs) > 0 {
			fb := p.Telemetry.Fireballs[0]
			fmt.Fprintf(w, "  cneos/fb  %s  v=%s km/s  vec=(%s, %s, %s)  E=%s\n",
				fb.Date, fb.Vel, fb.Vx, fb.Vy, fb.Vz, fb.Energy)
		}
		if len(p.Telemetry.Scout) > 0 {
			sc := p.Telemetry.Scout[0]
			fmt.Fprintf(w, "  cneos/neo %-7s %s  vInf=%s km/s  rate=%s  ra=%s dec=%s\n",
				sc.Object, sc.TEphem, sc.VInf, sc.Rate, sc.RA, sc.Dec)
		}
	}
	fmt.Fprintf(w, "  prev      %s\n", p.PrevSeed)
	fmt.Fprintf(w, "  sha256    %s\n", p.Seed)
}
