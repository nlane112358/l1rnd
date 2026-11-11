package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"l1rnd/internal/beacon"
	"l1rnd/internal/harvest"
)

func main() {
	var (
		jsonOut  = flag.Bool("j", false, "output verifiable pulse as JSON")
		rawOut   = flag.Bool("r", false, "output raw random bytes to stdout")
		numBytes = flag.Int("n", 32, "number of raw bytes to output with -r")
		osMix    = flag.Bool("os-mix", false, "mix OS CSPRNG with cosmic seed for -r")
		verbose  = flag.Bool("v", false, "log harvested cosmic telemetry to stderr")
		raffle   = flag.String("raffle", "", "comma-separated entries (or '-' for stdin) for raffle draw")
		winners  = flag.Int("k", 1, "number of raffle winners to draw")
		worldOut = flag.Bool("world", false, "output deterministic procedural world parameters")
		verifyIn = flag.Bool("verify", false, "verify JSON pulse(s) from stdin")
		interval = flag.Duration("i", 0, "continuous polling interval (e.g. 1m); default 1m when -http is set")
		httpAddr = flag.String("http", "", "HTTP listen address for Cosmic Entropy Beacon API (e.g. :8080)")
		timeout  = flag.Duration("timeout", 10*time.Second, "HTTP timeout per cosmic harvest")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: l1rnd [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Heliospheric True Random Number Generator & Entropy Beacon\n")
		fmt.Fprintf(os.Stderr, "Sources: NOAA SWPC (L1) + NASA DSN + JPL CNEOS (Fireball/Scout)\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *verifyIn {
		if err := runVerify(os.Stdin, os.Stderr); err != nil {
			fmt.Fprintf(os.Stderr, "l1rnd: verify failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &http.Client{Timeout: *timeout}
	if *httpAddr != "" && *interval <= 0 {
		*interval = time.Minute
	}

	var (
		mu     sync.RWMutex
		latest *beacon.Pulse
		seq    uint64
		prev   = beacon.GenesisSeed
	)

	step := func() (*beacon.Pulse, error) {
		hCtx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		t, err := harvest.Harvest(hCtx, client)
		if err != nil {
			return nil, err
		}
		seq++
		p := beacon.NewPulse(seq, time.Now().UTC(), prev, t)
		prev = p.Seed
		mu.Lock()
		latest = p
		mu.Unlock()
		if *verbose {
			beacon.FormatSummary(os.Stderr, p)
		}
		return p, nil
	}

	p, err := step()
	if err != nil {
		fmt.Fprintf(os.Stderr, "l1rnd: harvest error: %v\n", err)
		os.Exit(1)
	}

	if err := emitOutput(os.Stdout, p, *jsonOut, *rawOut, *numBytes, *osMix, *raffle, *winners, *worldOut); err != nil {
		fmt.Fprintf(os.Stderr, "l1rnd: %v\n", err)
		os.Exit(1)
	}

	if *httpAddr != "" {
		srv := newHTTPServer(*httpAddr, &mu, &latest, *osMix)
		go func() {
			if *verbose {
				fmt.Fprintf(os.Stderr, "l1rnd: beacon API listening on %s (interval %s)\n", *httpAddr, *interval)
			}
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "l1rnd: http error: %v\n", err)
				os.Exit(1)
			}
		}()
		defer func() {
			sCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(sCtx)
		}()
	}

	if *interval <= 0 {
		return
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p, err := step()
			if err != nil {
				fmt.Fprintf(os.Stderr, "l1rnd: harvest error: %v\n", err)
				continue
			}
			if err := emitOutput(os.Stdout, p, *jsonOut, *rawOut, *numBytes, *osMix, *raffle, *winners, *worldOut); err != nil {
				fmt.Fprintf(os.Stderr, "l1rnd: %v\n", err)
			}
		}
	}
}

func emitOutput(w io.Writer, p *beacon.Pulse, jsonOut, rawOut bool, numBytes int, osMix bool, raffle string, k int, worldOut bool) error {
	seed, err := p.SeedBytes()
	if err != nil {
		return err
	}
	switch {
	case rawOut:
		sr, err := beacon.NewStreamReader(seed, osMix)
		if err != nil {
			return err
		}
		_, err = io.CopyN(w, sr, int64(numBytes))
		return err
	case raffle != "":
		items, err := parseRaffleItems(raffle)
		if err != nil {
			return err
		}
		drawn := beacon.DrawRaffle(seed, items, k)
		if jsonOut {
			return writeJSONLine(w, map[string]interface{}{
				"winners":    drawn,
				"pulse_seq":  p.Seq,
				"pulse_seed": p.Seed,
				"timestamp":  p.Timestamp,
			})
		}
		for _, item := range drawn {
			fmt.Fprintln(w, item)
		}
		return nil
	case worldOut:
		world := beacon.GenerateWorld(seed)
		if jsonOut {
			return writeJSONLine(w, world)
		}
		fmt.Fprintf(w, "%s  class=%s (%dK)  g=%.2fG  T=%dK  ocean=%.0f%%  atm=%s  biome=%q  seed=0x%016x\n",
			world.Designation, world.SpectralClass, world.StarTempK, world.GravityG,
			world.SurfaceTempK, world.OceanFraction*100, world.Atmosphere, world.PrimaryBiome, world.TerrainSeed)
		return nil
	case jsonOut:
		return writeJSONLine(w, p)
	default:
		fmt.Fprintln(w, p.Seed)
		return nil
	}
}

func parseRaffleItems(spec string) ([]string, error) {
	if spec != "-" {
		return strings.Split(spec, ","), nil
	}
	var out []string
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

func writeJSONLine(w io.Writer, v interface{}) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func runVerify(r io.Reader, errW io.Writer) error {
	count, prevSeed, err := beacon.VerifyChain(r)
	if err != nil {
		return err
	}
	fmt.Fprintf(errW, "verified %d cosmic pulse(s) OK (head %s)\n", count, prevSeed)
	return nil
}

func newHTTPServer(addr string, mu *sync.RWMutex, latest **beacon.Pulse, osMix bool) *http.Server {
	mux := http.NewServeMux()
	getPulse := func() *beacon.Pulse {
		mu.RLock()
		defer mu.RUnlock()
		return *latest
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/pulse" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = writeJSONLine(w, getPulse())
	})

	mux.HandleFunc("/seed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, getPulse().Seed)
	})

	mux.HandleFunc("/raw", func(w http.ResponseWriter, r *http.Request) {
		n := 32
		if q := r.URL.Query().Get("n"); q != "" {
			if v, err := strconv.Atoi(q); err == nil && v > 0 && v <= 1<<20 {
				n = v
			}
		}
		p := getPulse()
		seed, _ := p.SeedBytes()
		sr, err := beacon.NewStreamReader(seed, osMix)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Pulse-Seq", strconv.FormatUint(p.Seq, 10))
		w.Header().Set("X-Pulse-Seed", p.Seed)
		_, _ = io.CopyN(w, sr, int64(n))
	})

	mux.HandleFunc("/raffle", func(w http.ResponseWriter, r *http.Request) {
		items := strings.Split(r.URL.Query().Get("items"), ",")
		k := 1
		if q := r.URL.Query().Get("k"); q != "" {
			if v, err := strconv.Atoi(q); err == nil && v > 0 {
				k = v
			}
		}
		p := getPulse()
		seed, _ := p.SeedBytes()
		w.Header().Set("Content-Type", "application/json")
		_ = writeJSONLine(w, map[string]interface{}{
			"winners":    beacon.DrawRaffle(seed, items, k),
			"pulse_seq":  p.Seq,
			"pulse_seed": p.Seed,
			"timestamp":  p.Timestamp,
		})
	})

	mux.HandleFunc("/world", func(w http.ResponseWriter, r *http.Request) {
		p := getPulse()
		seed, _ := p.SeedBytes()
		w.Header().Set("Content-Type", "application/json")
		_ = writeJSONLine(w, beacon.GenerateWorld(seed))
	})

	mux.HandleFunc("/verify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var p beacon.Pulse
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := p.Verify(); err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = writeJSONLine(w, map[string]interface{}{"valid": false, "error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = writeJSONLine(w, map[string]interface{}{"valid": true, "seed": p.Seed, "seq": p.Seq})
	})

	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}
