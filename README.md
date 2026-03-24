# worldtime

Print local time for any city in the world. Single static binary, no dependencies.

```
$ worldtime 'San Francisco'
Tue Mar 24 12:00:00 PDT 2026

$ worldtime Tokyo London 'New York'
Tokyo                   Wed Mar 25 04:00:00 JST 2026
London                  Tue Mar 24 20:00:00 GMT 2026
New York                Tue Mar 24 15:00:00 EDT 2026
```

## Build

Requires Go 1.21+.

```bash
# Current platform
make

# Specific platforms
make linux       # Linux x86-64
make mac-arm     # macOS Apple Silicon
make mac         # macOS Intel
make windows     # Windows x86-64
make all-platforms

# Install to /usr/local/bin
sudo make install
```

Or build directly:

```bash
CGO_ENABLED=0 go build -ldflags="-s -w" -o worldtime .
```

## How it works

**No hardcoded city list.** City→timezone resolution works in two layers:

1. **tzdata (automatic):** At startup, walks `/usr/share/zoneinfo` and derives
   a city name from every timezone file path (e.g. `America/Los_Angeles` →
   `Los Angeles`). This gives ~450 cities and updates automatically when the OS
   tzdata package is upgraded.

2. **Alias table (tiny, in `main.go`):** IANA intentionally uses one
   representative city per timezone zone. Cities that share a zone with a
   differently-named representative (e.g. San Francisco shares
   `America/Los_Angeles` with Los Angeles) need an explicit alias. This table
   has ~106 entries and almost never needs updating.

3. **Embedded tzdata:** The binary imports `time/tzdata`, which embeds Go's own
   copy of the IANA timezone database. This means the binary works even on
   systems with no `/usr/share/zoneinfo` (Windows, Alpine, scratch containers).
   Update it by upgrading your Go toolchain.

## Lookup

City names are matched case-insensitively with four fallback tiers:
exact match → prefix match → substring match → substring in tz ID.

So `SF`, `san francisco`, `francisco`, and `angeles` all work.

## Adding a city

If a city isn't found, add one line to the `aliases` table in `main.go`:

```go
{"Timbuktu", "Africa/Bamako"},
```

Then rebuild. The tz value must be a valid IANA timezone ID
(see [the full list](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones)).
