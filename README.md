# worldtime

Print local time for any city in the world. Single static binary, no API key required.

```
$ worldtime Tokyo
Wed Mar 25 04:00:00 JST 2026

$ worldtime 'Manchester, NH'
→ resolved "Manchester, NH" to Manchester, Hillsborough County, New Hampshire, United States (America/New_York)
Tue Mar 24 15:00:00 EDT 2026

$ worldtime 'London, Ontario'
→ resolved "London, Ontario" to London, Ontario, Canada (America/Toronto)
Tue Mar 24 15:00:00 EDT 2026

$ worldtime Tokyo London 'New York' 'Paris, TX'
Tokyo                   Wed Mar 25 04:00:00 JST 2026
London                  Tue Mar 24 20:00:00 GMT 2026
New York                Tue Mar 24 15:00:00 EDT 2026
Paris, TX               Tue Mar 24 14:00:00 CDT 2026
```

## Install

```bash
brew tap dnj12345/tap
brew install worldtime
```

Or build from source — see [Build](#build) below.

## How it works

City resolution happens in two stages:

**Stage 1 — Local lookup (instant, offline):**
At startup, worldtime walks `/usr/share/zoneinfo` and derives a city name from every timezone file path (e.g. `America/Los_Angeles` → `Los Angeles`), giving ~450 cities. A small alias table covers well-known cities that share a zone with a differently-named IANA representative (e.g. San Francisco → `America/Los_Angeles`). Common cities like Tokyo, London, and New York hit this stage and return immediately with no network call.

**Stage 2 — Geocode fallback (for anything else):**
If Stage 1 fails, the query is sent to [Nominatim](https://nominatim.openstreetmap.org/) (OpenStreetMap's free geocoding API — no key needed) to resolve it to coordinates. The [`tzf`](https://github.com/ringsaturn/tzf) library then converts those coordinates to an IANA timezone using embedded timezone boundary data. This handles any city, neighbourhood, address, or `"city, state/country"` disambiguation string — if OpenStreetMap knows it, worldtime can look it up.

## Usage

```
worldtime <city> [city2 ...]
worldtime --list
```

City names are matched case-insensitively. The `--list` flag prints all cities in the local database.

## Examples

```bash
# Common cities — resolved instantly from local DB, no network needed
worldtime Tokyo
worldtime London Paris Berlin

# Disambiguation with state or country — uses geocoding
worldtime 'Manchester, NH'
worldtime 'London, Ontario'
worldtime 'Springfield, IL'
worldtime 'Paris, TX'

# Mix of both in one command
worldtime Tokyo 'Manchester, NH' Dubai
```

## Build

Requires Go 1.21+.

```bash
git clone https://github.com/dnj12345/worldtime
cd worldtime
go mod tidy
make
```

| Target | Output |
|--------|--------|
| `make` | Binary for current platform |
| `make mac-arm` | macOS Apple Silicon |
| `make mac` | macOS Intel |
| `make linux` | Linux x86-64 |
| `make windows` | Windows x86-64 |
| `make all-platforms` | All of the above |
| `sudo make install` | Install to `/usr/local/bin` |

Or build directly:

```bash
go build -buildvcs=false -trimpath -ldflags="-s -w" -o worldtime .
```

## Adding a city to the local DB

If a city isn't found locally and you'd rather not rely on geocoding for it, add one line to the `aliases` table at the top of `main.go`:

```go
{"Timbuktu", "Africa/Bamako"},
```

The tz value must be a valid IANA timezone ID — see the [full list](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones).

## Dependencies

| Dependency | Purpose |
|------------|---------|
| [`github.com/ringsaturn/tzf`](https://github.com/ringsaturn/tzf) | Converts lat/lon coordinates to IANA timezone (embedded, ~5MB) |
| [Nominatim / OpenStreetMap](https://nominatim.openstreetmap.org/) | Geocodes city names to coordinates (network, only used as fallback) |
| Go's `time/tzdata` | Embeds IANA timezone rules — no host `/usr/share/zoneinfo` needed |

The binary is self-contained. The only runtime network call is the Nominatim geocode request, and only when a city isn't found in the local database.

## Notes

- Nominatim's [usage policy](https://operations.osmfoundation.org/policies/nominatim/) requires a descriptive `User-Agent` and a maximum of 1 request/second. worldtime sets a proper `User-Agent` and is designed for interactive use, so this is not a concern in practice.
- The resolved location is printed to `stderr` so `stdout` stays clean for scripting.
