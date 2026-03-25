# worldtime

Print local time for any city in the world. Single static binary, no API key required.

```
$ worldtime Tokyo London 'New York' 'Paris, TX'
Tokyo                   Wed Mar 25 04:00:00 JST 2026
London                  Tue Mar 24 20:00:00 GMT 2026
New York                Tue Mar 24 15:00:00 EDT 2026
Paris, TX               Tue Mar 24 14:00:00 CDT 2026

$ worldtime --at "10:00am" Tokyo London 'New York' 'Paris, TX'
Tokyo                   Thu Mar 26 02:00:00 JST 2026
London                  Wed Mar 25 17:00:00 GMT 2026
New York                Wed Mar 25 13:00:00 EDT 2026
Paris, TX               Wed Mar 25 12:00:00 CDT 2026

$ worldtime 'Manchester, NH'
→ resolved "Manchester, NH" to Manchester, Hillsborough County, New Hampshire, United States (America/New_York)
Wed Mar 25 15:00:00 EDT 2026
```

## Install

```bash
brew tap dnj12345/tap
brew install worldtime
```

Or build from source — see [Build](#build) below.

## Usage

```
worldtime <city> [city2 ...]                    show current local time in each city
worldtime --at <time> <city> [city2 ...]        convert a time to each city's local time
worldtime --set-tz <ABBREV> <IANA_ID>           save a timezone abbreviation preference
worldtime --list                                list all cities in the local DB
```

## Time conversion with `--at`

`--at` converts a specific time into the local time of each city. The source
time is either your **local time** (no suffix) or **UTC**:

```bash
# From your local time
worldtime --at "10:00am" Tokyo London 'New York'
worldtime --at "14:30" Sydney Seoul Mumbai
worldtime --at "9:30pm" Berlin Paris Rome

# From UTC
worldtime --at "14:30 UTC" Tokyo London 'New York'
worldtime --at "9am UTC" Sydney Seoul
worldtime --at "14:30 z" Dubai Karachi          # z is shorthand for UTC
```

**Supported time formats** (case-insensitive):

| Input | Meaning |
|-------|---------|
| `10am` | 10:00 AM |
| `10:00am` | 10:00 AM |
| `2:30pm` | 14:30 |
| `14:30` | 14:30 (24-hour) |
| `9:00 AM` | 9:00 AM |

**UTC shorthands:** `UTC`, `utc`, `u`, `z`, `GMT` are all equivalent.

## City lookup

City names are matched case-insensitively. You can use:

- Plain city names: `Tokyo`, `London`, `Paris`
- City + state/country for disambiguation: `'London, Ontario'`, `'Manchester, NH'`, `'Springfield, IL'`
- Neighbourhood or area names recognised by OpenStreetMap

Common cities are resolved instantly from a local database with no network
call. Anything not in the local DB is looked up via OpenStreetMap geocoding —
if OSM knows it, worldtime can find it.

## Ambiguous timezone abbreviations

Some abbreviations mean different things in different regions (IST = India,
Ireland, or Israel; CST = US Central or China). The first time you use one,
worldtime prompts you to choose:

```
"IST" is ambiguous. Which timezone do you mean?

  [1] India Standard Time (Asia/Kolkata)
  [2] Ireland Standard Time (Europe/Dublin)
  [3] Israel Standard Time (Asia/Jerusalem)

Enter number (this will be saved to ~/.config/worldtime/prefs): 1
Saved: IST=Asia/Kolkata
```

Your choice is saved to `~/.config/worldtime/prefs` and used silently from
then on. You can also set preferences directly without being prompted:

```bash
worldtime --set-tz IST Asia/Kolkata
worldtime --set-tz CST America/Chicago
```

In non-interactive use (scripts, pipes, cron), worldtime falls back to the
most common interpretation and prints a warning to stderr.

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

## How it works

**City → timezone resolution happens in two stages:**

1. **Local DB (instant, offline):** worldtime has an embedded list of ~450
   IANA timezone IDs (e.g. `America/Los_Angeles`), from which city names are
   derived at startup. A small alias table covers well-known cities that share
   a zone with a differently-named IANA representative (e.g. San Francisco
   shares `America/Los_Angeles` with Los Angeles). No filesystem access,
   works on any OS.

2. **Geocode fallback:** If Stage 1 fails, the query goes to
   [Nominatim](https://nominatim.openstreetmap.org/) (OpenStreetMap's free
   geocoding API — no key needed) to get coordinates. The
   [`tzf`](https://github.com/ringsaturn/tzf) library then converts those
   coordinates to an IANA timezone using timezone boundary polygons embedded
   in the binary. This handles any city, address, or `"city, state"` string.

**Timezone data** is embedded in the binary via Go's `time/tzdata` import — no
`/usr/share/zoneinfo` directory needed. Works on Alpine, scratch Docker
containers, Windows, and macOS regardless of system timezone package state.

## Adding a city to the local DB

If a city isn't found locally and you'd rather not rely on geocoding for it,
add one line to the `aliases` table at the top of `main.go`:

```go
{"Timbuktu", "Africa/Bamako"},
```

The tz value must be a valid IANA timezone ID — see the
[full list](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones).

## Keeping up to date

worldtime has no hardcoded timezone data to maintain. To pick up any IANA
timezone changes (typically 0–3 per year), just upgrade Go:

```bash
go get -u && go mod tidy && make
```

## Dependencies

| Dependency | Purpose |
|------------|---------|
| [`github.com/ringsaturn/tzf`](https://github.com/ringsaturn/tzf) | Lat/lon → IANA timezone (embedded, ~5 MB) |
| [Nominatim / OpenStreetMap](https://nominatim.openstreetmap.org/) | Geocoding city names (network, fallback only) |
| Go `time/tzdata` | Embedded IANA timezone rules |

The only runtime network call is the Nominatim geocode request, and only when
a city isn't in the local database.

Nominatim's [usage policy](https://operations.osmfoundation.org/policies/nominatim/)
asks for a descriptive `User-Agent` and a maximum of 1 request/second.
worldtime sets a proper `User-Agent` and is designed for interactive use, so
this is not a concern in practice.
