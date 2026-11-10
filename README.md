# l1rnd

> Heliospheric true random number generator and verifiable entropy beacon.

`l1rnd` synthesizes non-deterministic physical entropy from deep space instrumentation across the solar system into a cryptographically chained, publicly auditable entropy beacon.

Zero external dependencies. Pure Go standard library.

---

### Physical Entropy Sources

Every pulse concurrently harvests live telemetry from three independent heliospheric domains:

- **Sun–Earth Lagrange Point 1 (L1)** · NOAA SWPC  
  In-situ solar wind plasma dynamics (proton density, bulk speed, thermal velocity) and interplanetary magnetic field vectors ($B_t, B_x, B_y, B_z$) measured $\approx 1.5 \times 10^6\text{ km}$ upwind of Earth.
- **Deep Space Network (DSN)** · NASA JPL  
  Ground antenna kinematics (azimuth, elevation, wind), carrier signal fluctuations, downlink signal-to-noise ratios, and round-trip light times from deep-space probes across Goldstone, Madrid, and Canberra.
- **Near-Earth Kinematics** · NASA JPL CNEOS  
  Atmospheric bolide entry vectors from the Fireball network combined with real-time orbital perturbations and astrometric fit residuals from the Scout NEO tracking system.

---

### Verification & Chain Model

Each pulse $P_i$ serializes observations into a canonical, deterministic preimage binding its sequence, UTC timestamp, previous pulse seed, and full telemetry payload:

$$H_i = \text{SHA-256}\bigl(\text{preimage}(P_i) \parallel H_{i-1}\bigr)$$

The chain anchors at genesis $H_0 = 0^{64}$. Any alteration to observation timestamps, link metrics, or physical values invalidates the pulse digest.

---

### Quick Start

```bash
# Build
go build -o l1rnd .

# Emit 256-bit cosmic hex digest
./l1rnd

# Emit verifiable pulse JSON
./l1rnd -j

# Verbose telemetry diagnostic
./l1rnd -v
```

#### Stream Binary Entropy
Expands the 256-bit seed into arbitrary-length pseudo-random bytes via SHA-256 counter mode:
```bash
# Output 64 raw bytes
./l1rnd -r -n 64 > entropy.bin

# Mix cosmic seed with OS CSPRNG (/dev/urandom)
./l1rnd -r -n 32 -os-mix
```

#### Cryptographic Chain Verification
```bash
# Continuously verify pulse integrity and chain continuity
./l1rnd -j -i 1m | ./l1rnd -verify
```

#### Built-in Primitives
```bash
# Verifiable Fisher-Yates cosmic raffle
./l1rnd -raffle "alice,bob,carol,david" -k 2

# Deterministic procedural exoplanet generation
./l1rnd -world
```

---

### HTTP Beacon Daemon

Serve a continuous, verifiable entropy beacon over HTTP:

```bash
./l1rnd -http :8080 -i 1m
```

| Endpoint | Method | Description |
|---|---|---|
| `/` or `/pulse` | `GET` | Latest verifiable pulse JSON |
| `/seed` | `GET` | Plaintext 256-bit hex digest |
| `/raw?n=32` | `GET` | Binary entropy stream |
| `/raffle?items=a,b&k=1` | `GET` | Verifiable Fisher-Yates draw |
| `/world` | `GET` | Deterministic procedural world parameters |
| `/verify` | `POST` | Audit external pulse JSON |

---

### License

[MIT](LICENSE)
